package station

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/stationbrowser"
)

// Visual summaries are retained observations, never proof that the current
// platform media still has the same bytes. The caller supplies a work from a
// current, account-checked creator list; the works-memory grant is checked here.
type localDouyinSavedWorkVisual struct {
	VideoSummary  string `json:"video_summary,omitempty"`
	ImageSummary  string `json:"image_summary,omitempty"`
	VideoCoverage string `json:"video_coverage,omitempty"`
	ImageCoverage string `json:"image_coverage,omitempty"`
	VideoSHA256   string `json:"video_sha256,omitempty"`
	ImageKey      string `json:"image_key,omitempty"`
	VideoComplete bool   `json:"video_complete"`
	ImageComplete bool   `json:"image_complete"`
	VideoPages    int    `json:"video_pages"`
	ImagePages    int    `json:"image_pages"`
}

type localDouyinStoredImagePage struct {
	Version  int                     `json:"version"`
	WorkID   string                  `json:"work_id"`
	ImageKey string                  `json:"image_key"`
	Total    int                     `json:"total"`
	Start    int                     `json:"start"`
	End      int                     `json:"end"`
	Page     apphost.ObservationPage `json:"page"`
}

func (p *localDouyinWebObservation) captureCreatorImageContext(ctx context.Context, a localDouyinWebAccess,
	snapshot localCreatorWorkSnapshot, page apphost.ObservationPage) error {
	if a.record.Memory.Works == "" {
		return nil
	}
	if !a.record.WorksEnabled || !a.record.ImagesEnabled || snapshot.account == "" ||
		!creatorOwnerCurrent(a, snapshot.account) || snapshot.work.Video || snapshot.work.Reviewing ||
		snapshot.work.Visibility != "公开" || snapshot.imageCount < 1 ||
		!validLocalDouyinCommentID(snapshot.work.ID) || snapshot.imageKey == "" ||
		len(page.Chunks) != 1 || page.Chunks[0].SourceID != snapshot.work.ID {
		return apphost.ErrObservationForbidden
	}
	start, end := page.Chunks[0].Position, snapshot.imageCount
	if !page.Complete {
		var err error
		end, err = strconv.Atoi(page.NextCursor)
		if err != nil {
			return apphost.ErrObservationInvalid
		}
	}
	if start < 0 || end <= start || end > snapshot.imageCount || page.Complete != (end == snapshot.imageCount) {
		return apphost.ErrObservationInvalid
	}
	stored := localDouyinStoredImagePage{Version: 1, WorkID: snapshot.work.ID, ImageKey: snapshot.imageKey,
		Total: snapshot.imageCount, Start: start, End: end, Page: page}
	raw, err := json.Marshal(stored)
	if err != nil || len(raw) > 128<<10 {
		return apphost.ErrObservationWebStorageUnavailable
	}
	return p.home.douyinWeb.captureMemory(ctx, a.record, douyinMemoryWorks, snapshot.account,
		[]localDouyinMemoryRecord{{ID: fmt.Sprintf("image:%s:%d", snapshot.imageKey, start), Work: snapshot.work.ID,
			Label: "本人公开图文图片分析", Text: string(raw), URL: stationbrowser.DouyinCreatorManageURL,
			Coverage: page.Coverage, At: time.Now().UTC().Format(localDouyinMemoryTime)}})
}

func (m *localDouyinWebLogin) readSavedWorkVisual(ctx context.Context, record localDouyinWebRecord,
	fish, owner, creatorAccount string, work stationbrowser.DouyinCreatorWork) (localDouyinSavedWorkVisual, error) {
	all, err := m.readSavedWorksVisual(ctx, record, fish, owner, creatorAccount, []stationbrowser.DouyinCreatorWork{work})
	return all[work.ID], err
}

// A catalog request opens the works-memory store once. Each returned work
// still needs its own retained creator row and the matching media permission.
func (m *localDouyinWebLogin) readSavedWorksVisual(ctx context.Context, record localDouyinWebRecord,
	fish, owner, creatorAccount string, works []stationbrowser.DouyinCreatorWork) (map[string]localDouyinSavedWorkVisual, error) {
	result := map[string]localDouyinSavedWorkVisual{}
	if creatorAccount == "" || len(works) == 0 {
		return result, nil
	}
	videoAllowed := record.ReadEnabled && localDouyinWebScope(record) == "home_and_videos" && record.VisualEnabled
	imageAllowed := record.ImagesEnabled
	if !videoAllowed && !imageAllowed {
		return result, nil
	}
	source, granted := douyinMemorySource(record, fish, owner, douyinMemoryWorks)
	if !granted {
		return result, nil
	}
	identity, err := m.home.identity.Read()
	if err != nil || identity.LocalOwner == nil || identity.FishID != fish || identity.LocalOwner.ID != owner {
		return result, nil
	}
	current, _, err := m.noticeState(fish)
	if err != nil || current != record {
		return result, nil
	}
	access := (&localDouyinWebObservation{home: m.home}).access(fish, owner, record, m.view(record))
	if !creatorOwnerCurrent(access, creatorAccount) {
		return result, nil
	}
	db, err := openLocalDouyinMemory(ctx, m.home, false)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer db.Close()
	var pinned string
	err = db.QueryRowContext(ctx, `SELECT account FROM bindings WHERE id=?`, source.binding).Scan(&pinned)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if pinned != localCheckpointKey(source.binding, creatorAccount) {
		return result, nil
	}
	for _, work := range works {
		if !validLocalDouyinCommentID(work.ID) || work.Visibility != "公开" || work.Reviewing {
			continue
		}
		var creatorText string
		err = db.QueryRowContext(ctx, `SELECT text FROM records WHERE scope=? AND id=? AND work=?`,
			source.Scope, work.ID, work.ID).Scan(&creatorText)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return map[string]localDouyinSavedWorkVisual{}, err
		}
		if len(creatorText) > 128<<10 || !strings.HasPrefix(creatorText, "作品编号 "+work.ID+"\n") {
			continue
		}
		var item localDouyinSavedWorkVisual
		if work.Video && videoAllowed {
			if err := readSavedDouyinVideoVisual(ctx, db, source, work.ID, &item); err != nil {
				return map[string]localDouyinSavedWorkVisual{}, err
			}
		}
		if !work.Video && imageAllowed {
			if err := readSavedDouyinImageVisual(ctx, db, source, work.ID, &item); err != nil {
				return map[string]localDouyinSavedWorkVisual{}, err
			}
		}
		result[work.ID] = item
	}
	identity, err = m.home.identity.Read()
	if err != nil || identity.LocalOwner == nil || identity.FishID != fish || identity.LocalOwner.ID != owner {
		return map[string]localDouyinSavedWorkVisual{}, nil
	}
	current, _, err = m.noticeState(fish)
	if err != nil || current != record || !creatorOwnerCurrent(access, creatorAccount) {
		return map[string]localDouyinSavedWorkVisual{}, nil
	}
	return result, nil
}

func readSavedDouyinVideoVisual(ctx context.Context, db *sql.DB, source localDouyinMemorySource,
	workID string, result *localDouyinSavedWorkVisual) error {
	var id, raw, observedAt string
	err := db.QueryRowContext(ctx, `SELECT id,text,observed_at FROM records WHERE scope=? AND work=? AND id LIKE 'media:%' ORDER BY observed_at DESC,id LIMIT 1`,
		source.Scope, workID).Scan(&id, &raw, &observedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	latest, ok := decodeLocalDouyinWorkMedia(id, raw, observedAt, workID)
	if !ok {
		return nil
	}
	result.VideoSHA256 = latest.hash
	rows, err := db.QueryContext(ctx, `SELECT id,text,observed_at FROM records WHERE scope=? AND work=? AND id LIKE ?`,
		source.Scope, workID, "media:visual:"+latest.hash+":%")
	if err != nil {
		return err
	}
	defer rows.Close()
	var pages []localDouyinStoredWorkMedia
	valid := true
	for rows.Next() {
		var id, raw, at string
		if err := rows.Scan(&id, &raw, &at); err != nil {
			return err
		}
		page, ok := decodeLocalDouyinWorkMedia(id, raw, at, workID)
		if !ok || page.hash != latest.hash || page.page.VisualProof == nil {
			valid = false
			continue
		}
		pages = append(pages, page)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].startFrame < pages[j].startFrame })
	var lines []string
	for _, page := range pages {
		lines = append(lines, fmt.Sprintf("%s：%s", page.page.Coverage, page.page.Text))
	}
	result.VideoSummary = strings.Join(lines, "\n\n")
	result.VideoPages = len(pages)
	_, result.VideoComplete = completeLocalDouyinWorkMedia(pages)
	result.VideoComplete = result.VideoComplete && valid
	if len(pages) > 0 {
		result.VideoCoverage = fmt.Sprintf("已存视频采样画面 %d 页；仅表示旧媒体 SHA %s 的采样范围，不代表逐帧或当前平台原件", len(pages), latest.hash)
	}
	return nil
}

func readSavedDouyinImageVisual(ctx context.Context, db *sql.DB, source localDouyinMemorySource,
	workID string, result *localDouyinSavedWorkVisual) error {
	var latestID, latestRaw string
	err := db.QueryRowContext(ctx, `SELECT id,text FROM records WHERE scope=? AND work=? AND id LIKE 'image:%' ORDER BY observed_at DESC,id LIMIT 1`,
		source.Scope, workID).Scan(&latestID, &latestRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	latest, ok := decodeSavedDouyinImagePage(latestID, latestRaw, workID)
	if !ok {
		return nil
	}
	result.ImageKey = latest.ImageKey
	rows, err := db.QueryContext(ctx, `SELECT id,text FROM records WHERE scope=? AND work=? AND id LIKE ?`,
		source.Scope, workID, "image:"+latest.ImageKey+":%")
	if err != nil {
		return err
	}
	defer rows.Close()
	var pages []localDouyinStoredImagePage
	valid := true
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		page, ok := decodeSavedDouyinImagePage(id, raw, workID)
		if !ok || page.ImageKey != latest.ImageKey || page.Total != latest.Total {
			valid = false
			continue
		}
		pages = append(pages, page)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Start < pages[j].Start })
	var lines []string
	next, analyzed := 0, 0
	for _, page := range pages {
		if page.Start != next {
			valid = false
		}
		next = page.End
		analyzed += page.End - page.Start
		lines = append(lines, fmt.Sprintf("%s：%s", page.Page.Coverage, page.Page.Chunks[0].Text))
	}
	result.ImageSummary = strings.Join(lines, "\n\n")
	result.ImagePages = len(pages)
	result.ImageComplete = valid && next == latest.Total && len(pages) > 0 && pages[len(pages)-1].Page.Complete
	if len(pages) > 0 {
		result.ImageCoverage = fmt.Sprintf("已存公开图文图片分析 %d/%d 张，共 %d 页；只表示保存时的图片顺序，不代表当前平台图片仍相同", analyzed, latest.Total, len(pages))
	}
	return nil
}

func decodeSavedDouyinImagePage(id, raw, workID string) (localDouyinStoredImagePage, bool) {
	var page localDouyinStoredImagePage
	if len(raw) > 128<<10 || strictLocalMemoryDecode([]byte(raw), &page) != nil || page.Version != 1 ||
		page.WorkID != workID || !localDouyinPublicationHex(page.ImageKey) ||
		page.Total < 1 || page.Start < 0 || page.End <= page.Start || page.End > page.Total ||
		page.Page.Complete != (page.End == page.Total) || len(page.Page.Chunks) != 1 ||
		page.Page.Chunks[0].Position != page.Start || page.Page.Chunks[0].SourceID != workID ||
		page.Page.Chunks[0].SourceURL != stationbrowser.DouyinCreatorManageURL ||
		id != fmt.Sprintf("image:%s:%d", page.ImageKey, page.Start) {
		return localDouyinStoredImagePage{}, false
	}
	return page, true
}
