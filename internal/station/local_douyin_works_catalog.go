package station

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/fishidentity"
	"wechat-bot/internal/stationbrowser"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

// The catalog is a private, owner-bound view of creator-management responses.
// It contains no browser profile data, CDN URLs, cookies, or third-party feed.
const douyinWorksCatalogSchema = `
CREATE TABLE IF NOT EXISTS catalog (
  id INTEGER PRIMARY KEY CHECK(id=1), owner TEXT NOT NULL, profile TEXT NOT NULL,
  grant TEXT NOT NULL, account TEXT NOT NULL, state TEXT NOT NULL,
  has_more INTEGER, pages INTEGER NOT NULL, next_page INTEGER NOT NULL,
  next_offset INTEGER NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS works (
  id TEXT PRIMARY KEY, title TEXT NOT NULL, description TEXT NOT NULL,
  published_at TEXT NOT NULL, visibility TEXT NOT NULL,
  review_status TEXT NOT NULL, media_type TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS staging_works (
  id TEXT PRIMARY KEY, title TEXT NOT NULL, description TEXT NOT NULL,
  published_at TEXT NOT NULL, visibility TEXT NOT NULL,
  review_status TEXT NOT NULL, media_type TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS catalog_active (
  id INTEGER PRIMARY KEY CHECK(id=1), pages INTEGER NOT NULL, updated_at TEXT NOT NULL
);`

type localDouyinCatalogWork struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	PublishedAt    string `json:"published_at"`
	Visibility     string `json:"visibility"`
	ReviewStatus   string `json:"review_status"`
	MediaType      string `json:"media_type"`
	VisualSummary  string `json:"visual_summary,omitempty"`
	ImageSummary   string `json:"image_summary,omitempty"`
	VisualCoverage string `json:"visual_coverage,omitempty"`
	ImageCoverage  string `json:"image_coverage,omitempty"`
	VisualComplete bool   `json:"visual_complete,omitempty"`
	ImageComplete  bool   `json:"image_complete,omitempty"`
	VisualError    string `json:"visual_error,omitempty"`
}

type localDouyinCatalogCoverage struct {
	State            string `json:"state"`
	Loaded           int    `json:"loaded"`
	Pages            int    `json:"pages"`
	UpdatedAt        string `json:"updated_at"`
	HasMore          *bool  `json:"has_more"`
	PreviousComplete bool   `json:"previous_complete,omitempty"`
	SyncLoaded       int    `json:"sync_loaded,omitempty"`
}

type localDouyinCatalogResponse struct {
	AccountID string                     `json:"account_id"`
	Works     []localDouyinCatalogWork   `json:"works"`
	Coverage  localDouyinCatalogCoverage `json:"coverage"`
}

func douyinWorksCatalogGrant(r localDouyinWebRecord) string {
	if r.WorksCatalogGrant != "" {
		return r.WorksCatalogGrant
	}
	// Existing saved profiles predate the catalog grant. The first explicit
	// revocation/regrant writes a fresh random token, so old rows stay hidden.
	return localCheckpointKey("douyin-works-catalog-legacy", r.FishID, r.Profile, r.ProfileEpoch, r.ConfirmedAt)
}

func douyinWorksCatalogPath(home *localHomeService, fish string) string {
	return filepath.Join(stationApplicationsDir(home.dataDir), "douyin", "web-login", fish, "works-catalog.sqlite")
}

func openDouyinWorksCatalog(ctx context.Context, home *localHomeService, fish string, create bool) (*sql.DB, error) {
	path := douyinWorksCatalogPath(home, fish)
	dir := filepath.Dir(path)
	open := workspacefs.OpenPath
	if create {
		open = workspacefs.EnsurePath
	}
	root, err := open(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(filepath.Base(path))
	if err != nil && (!create || !errors.Is(err, os.ErrNotExist)) {
		return nil, err
	}
	if info != nil && !info.Mode().IsRegular() {
		return nil, apphost.ErrObservationWebStorageUnavailable
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err == nil && create {
		if _, err = db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err == nil {
			_, err = db.ExecContext(ctx, douyinWorksCatalogSchema)
		}
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	if create {
		if err = os.Chmod(path, 0600); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

func douyinCatalogReviewStatus(work stationbrowser.DouyinCreatorWork) string {
	if work.Reviewing {
		return "reviewing"
	}
	return "not_reviewing"
}

func douyinCatalogMediaType(work stationbrowser.DouyinCreatorWork) string {
	if work.Video {
		return "video"
	}
	if len(work.Images) > 0 {
		return "images"
	}
	return "unknown"
}

// captureWorksCatalog is called only after the current creator account and
// observation grant are rechecked. The same login mutex serializes owner
// revocation, catalog writes, and local catalog reads.
func (m *localDouyinWebLogin) captureWorksCatalog(ctx context.Context, a localDouyinWebAccess, cursor localCreatorWorksCursor, page stationbrowser.DouyinCreatorWorksPage, rows []stationbrowser.DouyinCreatorWork, end int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	identity, err := m.home.identity.Read()
	if err != nil || identity.LocalOwner == nil || identity.FishID != a.record.FishID {
		return apphost.ErrObservationForbidden
	}
	dir := filepath.Dir(a.profile)
	root, err := workspacefs.OpenPath(dir)
	if err != nil {
		return apphost.ErrObservationWebStorageUnavailable
	}
	current, err := readLocalDouyinWebRecord(root, a.record.FishID)
	root.Close()
	if err != nil || current != a.record || current.Paused || current.Detached || !current.WorksEnabled || !creatorOwnerCurrent(a, page.Account) {
		return apphost.ErrObservationForbidden
	}
	db, err := openDouyinWorksCatalog(ctx, m.home, a.record.FishID, true)
	if err != nil {
		return apphost.ErrObservationWebStorageUnavailable
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return apphost.ErrObservationWebStorageUnavailable
	}
	defer tx.Rollback()
	first := cursor.Request.Page == 0 && cursor.Offset == 0
	grant := douyinWorksCatalogGrant(current)
	var savedOwner, savedProfile, savedGrant, savedAccount string
	var pages, nextPage, nextOffset int
	err = tx.QueryRowContext(ctx, "SELECT owner,profile,grant,account,pages,next_page,next_offset FROM catalog WHERE id=1").Scan(&savedOwner, &savedProfile, &savedGrant, &savedAccount, &pages, &nextPage, &nextOffset)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return apphost.ErrObservationWebStorageUnavailable
	}
	if first {
		// A refresh fills a separate generation. The last completed catalog
		// remains visible until the new final page is committed atomically.
		if _, err = tx.ExecContext(ctx, "DELETE FROM staging_works"); err != nil {
			return apphost.ErrObservationWebStorageUnavailable
		}
		if savedOwner != identity.LocalOwner.ID || savedProfile != a.connection.AccountID || savedGrant != grant || savedAccount != page.Account {
			if _, err = tx.ExecContext(ctx, "DELETE FROM works"); err != nil {
				return apphost.ErrObservationWebStorageUnavailable
			}
			if _, err = tx.ExecContext(ctx, "DELETE FROM catalog_active"); err != nil {
				return apphost.ErrObservationWebStorageUnavailable
			}
		}
		pages = 0
	} else if err != nil || savedOwner != identity.LocalOwner.ID || savedProfile != a.connection.AccountID || savedGrant != grant || savedAccount != page.Account || nextPage != cursor.Request.Page || nextOffset != cursor.Offset {
		return apphost.ErrObservationWebSnapshotChanged
	}
	for _, work := range rows {
		if _, err = tx.ExecContext(ctx, `INSERT INTO staging_works(id,title,description,published_at,visibility,review_status,media_type)
VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET title=excluded.title,description=excluded.description,
published_at=excluded.published_at,visibility=excluded.visibility,review_status=excluded.review_status,media_type=excluded.media_type`,
			work.ID, work.Title, work.Caption, work.CreatedAt, work.Visibility, douyinCatalogReviewStatus(work), douyinCatalogMediaType(work)); err != nil {
			return apphost.ErrObservationWebStorageUnavailable
		}
	}
	if pages < cursor.Request.Page+1 {
		pages = cursor.Request.Page + 1
	}
	state := "partial"
	var hasMore any
	if end < len(page.Works) {
		hasMore = true
	} else if !page.CoverageKnown {
		state = "unknown"
	} else {
		hasMore = page.HasMore
		if !page.HasMore {
			state = "complete"
		}
	}
	if end < len(page.Works) {
		nextPage, nextOffset = cursor.Request.Page, end
	} else {
		nextPage, nextOffset = cursor.Request.Page+1, 0
	}
	if state == "complete" {
		nextPage, nextOffset = -1, -1
		if _, err = tx.ExecContext(ctx, "DELETE FROM works"); err != nil {
			return apphost.ErrObservationWebStorageUnavailable
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO works(id,title,description,published_at,visibility,review_status,media_type)
SELECT id,title,description,published_at,visibility,review_status,media_type FROM staging_works`); err != nil {
			return apphost.ErrObservationWebStorageUnavailable
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM staging_works"); err != nil {
			return apphost.ErrObservationWebStorageUnavailable
		}
	}
	updatedAt := time.Now().UTC().Format(time.RFC3339)
	_, err = tx.ExecContext(ctx, `INSERT INTO catalog(id,owner,profile,grant,account,state,has_more,pages,next_page,next_offset,updated_at)
VALUES(1,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET owner=excluded.owner,profile=excluded.profile,
grant=excluded.grant,account=excluded.account,state=excluded.state,has_more=excluded.has_more,
pages=excluded.pages,next_page=excluded.next_page,next_offset=excluded.next_offset,updated_at=excluded.updated_at`,
		identity.LocalOwner.ID, a.connection.AccountID, grant, page.Account, state, hasMore, pages, nextPage, nextOffset, updatedAt)
	if err != nil {
		return apphost.ErrObservationWebStorageUnavailable
	}
	if state == "complete" {
		_, err = tx.ExecContext(ctx, `INSERT INTO catalog_active(id,pages,updated_at) VALUES(1,?,?)
ON CONFLICT(id) DO UPDATE SET pages=excluded.pages,updated_at=excluded.updated_at`, pages, updatedAt)
	}
	if err != nil || tx.Commit() != nil {
		return apphost.ErrObservationWebStorageUnavailable
	}
	return nil
}

func (m *localDouyinWebLogin) readWorksCatalog(ctx context.Context, fish, owner string) (localDouyinCatalogResponse, localDouyinWebRecord, error) {
	result := localDouyinCatalogResponse{Works: []localDouyinCatalogWork{}, Coverage: localDouyinCatalogCoverage{State: "unknown"}}
	m.mu.Lock()
	defer m.mu.Unlock()
	identity, err := m.home.identity.Read()
	if err != nil || identity.LocalOwner == nil || identity.FishID != fish || identity.LocalOwner.ID != owner {
		return result, localDouyinWebRecord{}, apphost.ErrObservationForbidden
	}
	dir := filepath.Join(stationApplicationsDir(m.home.dataDir), "douyin", "web-login", fish)
	root, err := workspacefs.OpenPath(dir)
	if err != nil {
		return result, localDouyinWebRecord{}, apphost.ErrObservationForbidden
	}
	record, err := readLocalDouyinWebRecord(root, fish)
	root.Close()
	if err != nil || record.Paused || record.Detached || !record.WorksEnabled || record.Profile == "" || record.ConfirmedAt == "" {
		return result, localDouyinWebRecord{}, apphost.ErrObservationForbidden
	}
	db, err := openDouyinWorksCatalog(ctx, m.home, fish, false)
	if errors.Is(err, os.ErrNotExist) {
		return result, record, nil
	}
	if err != nil {
		return result, localDouyinWebRecord{}, apphost.ErrObservationWebStorageUnavailable
	}
	defer db.Close()
	var savedOwner, savedProfile, savedGrant string
	var hasMore sql.NullBool
	a := (&localDouyinWebObservation{home: m.home}).access(fish, owner, record, localDouyinWebView{})
	err = db.QueryRowContext(ctx, "SELECT owner,profile,grant,account,state,has_more,pages,updated_at FROM catalog WHERE id=1").Scan(
		&savedOwner, &savedProfile, &savedGrant, &result.AccountID, &result.Coverage.State, &hasMore, &result.Coverage.Pages, &result.Coverage.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return localDouyinCatalogResponse{Works: []localDouyinCatalogWork{}, Coverage: localDouyinCatalogCoverage{State: "unknown"}}, record, nil
	}
	if err != nil {
		return result, localDouyinWebRecord{}, apphost.ErrObservationWebStorageUnavailable
	}
	if savedOwner != owner || savedProfile != a.connection.AccountID || savedGrant != douyinWorksCatalogGrant(record) || !creatorOwnerCurrent(a, result.AccountID) {
		return localDouyinCatalogResponse{Works: []localDouyinCatalogWork{}, Coverage: localDouyinCatalogCoverage{State: "unknown"}}, record, nil
	}
	if result.Coverage.State != "complete" && result.Coverage.State != "partial" && result.Coverage.State != "unknown" || result.Coverage.Pages < 0 {
		return result, localDouyinWebRecord{}, apphost.ErrObservationWebStorageUnavailable
	}
	if hasMore.Valid {
		result.Coverage.HasMore = &hasMore.Bool
	}
	rowsTable := "staging_works"
	var activePages int
	err = db.QueryRowContext(ctx, "SELECT pages FROM catalog_active WHERE id=1").Scan(&activePages)
	if err == nil {
		rowsTable = "works"
		if result.Coverage.State != "complete" {
			result.Coverage.PreviousComplete = true
			if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM staging_works").Scan(&result.Coverage.SyncLoaded); err != nil {
				return result, localDouyinWebRecord{}, apphost.ErrObservationWebStorageUnavailable
			}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return result, localDouyinWebRecord{}, apphost.ErrObservationWebStorageUnavailable
	}
	rows, err := db.QueryContext(ctx, "SELECT id,title,description,published_at,visibility,review_status,media_type FROM "+rowsTable+" ORDER BY published_at DESC,id DESC")
	if err != nil {
		return result, localDouyinWebRecord{}, apphost.ErrObservationWebStorageUnavailable
	}
	defer rows.Close()
	for rows.Next() {
		var work localDouyinCatalogWork
		if rows.Scan(&work.ID, &work.Title, &work.Description, &work.PublishedAt, &work.Visibility, &work.ReviewStatus, &work.MediaType) != nil {
			return result, localDouyinWebRecord{}, apphost.ErrObservationWebStorageUnavailable
		}
		result.Works = append(result.Works, work)
	}
	if rows.Err() != nil {
		return result, localDouyinWebRecord{}, apphost.ErrObservationWebStorageUnavailable
	}
	result.Coverage.Loaded = len(result.Works)
	return result, record, nil
}

func (m *localDouyinWebLogin) worksCatalogStillAuthorized(fish, owner string, record localDouyinWebRecord, account string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	identity, err := m.home.identity.Read()
	if err != nil || identity.LocalOwner == nil || identity.FishID != fish || identity.LocalOwner.ID != owner {
		return false
	}
	root, err := workspacefs.OpenPath(filepath.Dir(douyinWorksCatalogPath(m.home, fish)))
	if err != nil {
		return false
	}
	current, err := readLocalDouyinWebRecord(root, fish)
	root.Close()
	if err != nil || current != record || current.Paused || current.Detached || !current.WorksEnabled {
		return false
	}
	a := (&localDouyinWebObservation{home: m.home}).access(fish, owner, current, localDouyinWebView{})
	return account == "" || creatorOwnerCurrent(a, account)
}

func (m *localDouyinWebLogin) worksCatalogRoutes(owner *websession.Store, mux *http.ServeMux) {
	mux.HandleFunc("GET /api/local/apps/douyin/works", owner.Protect(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		identity, err := m.home.identity.Read()
		if err != nil || identity.LocalOwner == nil || !fishidentity.ValidFishID(identity.FishID) {
			websession.Fail(w, http.StatusConflict, "请先准备本机主人身份")
			return
		}
		catalog, record, err := m.readWorksCatalog(r.Context(), identity.FishID, identity.LocalOwner.ID)
		if err != nil {
			if errors.Is(err, apphost.ErrObservationForbidden) {
				websession.Fail(w, http.StatusForbidden, "本人作品目录当前未获授权")
			} else {
				websession.Fail(w, http.StatusServiceUnavailable, "本人作品目录暂不可读")
			}
			return
		}
		// Visual evidence is read only from the existing, separately authorized
		// private works memory. It never accesses the platform in this route.
		if record.Memory.Works != "" {
			eligible := make([]stationbrowser.DouyinCreatorWork, 0, len(catalog.Works))
			for _, work := range catalog.Works {
				if work.Visibility == "公开" && work.ReviewStatus == "not_reviewing" {
					eligible = append(eligible, stationbrowser.DouyinCreatorWork{ID: work.ID, Title: work.Title,
						Caption: work.Description, CreatedAt: work.PublishedAt, Visibility: work.Visibility, Video: work.MediaType == "video"})
				}
			}
			saved, visualErr := m.readSavedWorksVisual(r.Context(), record, identity.FishID, identity.LocalOwner.ID, catalog.AccountID, eligible)
			for i := range catalog.Works {
				work := &catalog.Works[i]
				if visualErr != nil {
					if work.Visibility == "公开" && work.ReviewStatus == "not_reviewing" {
						work.VisualError = "本机视觉证据暂不可读"
					}
					continue
				}
				if evidence, ok := saved[work.ID]; ok {
					work.VisualSummary, work.ImageSummary = evidence.VideoSummary, evidence.ImageSummary
					work.VisualCoverage, work.ImageCoverage = evidence.VideoCoverage, evidence.ImageCoverage
					work.VisualComplete, work.ImageComplete = evidence.VideoComplete, evidence.ImageComplete
				}
			}
		}
		if !m.worksCatalogStillAuthorized(identity.FishID, identity.LocalOwner.ID, record, catalog.AccountID) {
			websession.Fail(w, http.StatusForbidden, "本人作品目录授权已变化")
			return
		}
		websession.Respond(w, http.StatusOK, catalog)
	}))
}
