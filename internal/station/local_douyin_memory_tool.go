package station

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/workspacefs"
)

type localDouyinMemoryProof struct {
	Source   localDouyinMemorySource
	Revision uint64
	Epoch    string
}

type localDouyinMemoryReference struct {
	Proof localDouyinMemoryProof
	ID    string
}

type localDouyinMemoryPage struct {
	Proof               localDouyinMemoryProof
	Terms               []string
	Work, Since, Before string
	Offset              int
}

func (t *localToolSet) douyinMemoryState(ctx context.Context) (localDouyinWebRecord, error) {
	if !t.douyinMemoryRequested || t.publications == nil || t.checkAppObservationIdentity() != nil {
		return localDouyinWebRecord{}, apphost.ErrObservationForbidden
	}
	if t.douyinMemoryOrigin != nil {
		if err := t.douyinMemoryOrigin(ctx); err != nil {
			return localDouyinWebRecord{}, err
		}
	} else if t.run == nil || !t.run.Tracked || t.run.CheckSource(ctx) != nil {
		return localDouyinWebRecord{}, apphost.ErrObservationForbidden
	}
	if _, ok := t.publications.applications.Lookup(douyinapp.ID); !ok {
		return localDouyinWebRecord{}, apphost.ErrObservationNotFound
	}
	m := t.publications.home.douyinWeb
	if m == nil {
		return localDouyinWebRecord{}, apphost.ErrObservationNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx.Err() != nil {
		return localDouyinWebRecord{}, apphost.ErrObservationForbidden
	}
	dir := filepath.Join(m.home.dataDir, "apps", "douyin", "web-login", t.access.fishID)
	root, err := workspacefs.OpenPath(dir)
	if errors.Is(err, os.ErrNotExist) {
		return localDouyinWebRecord{}, nil
	}
	if err != nil {
		return localDouyinWebRecord{}, err
	}
	defer root.Close()
	record, err := readLocalDouyinWebRecord(root, t.access.fishID)
	if err != nil || record.Memory == (localDouyinMemoryGrants{}) {
		return record, err
	}
	if record.Profile == "" || (m.attempt != "" && m.fish == t.access.fishID) {
		return localDouyinWebRecord{}, nil
	}
	profile, err := workspacefs.OpenPath(filepath.Join(dir, record.Profile))
	if err != nil {
		return localDouyinWebRecord{}, nil
	}
	profile.Close()
	return record, nil
}

func douyinMemoryEpoch(ctx context.Context, db *sql.DB, source localDouyinMemorySource) (string, error) {
	if db == nil {
		return "", nil
	}
	var account, epoch string
	err := db.QueryRowContext(ctx, `SELECT b.account,COALESCE(g.epoch,'') FROM bindings b
LEFT JOIN generations g ON g.scope=? WHERE b.id=?`, source.Scope, source.binding).Scan(&account, &epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err == nil && account == "" {
		return "", apphost.ErrObservationForbidden
	}
	return epoch, err
}

func (t *localToolSet) checkDouyinMemoryProof(ctx context.Context, db *sql.DB, record localDouyinWebRecord, proof localDouyinMemoryProof) error {
	source, ok := douyinMemorySource(record, t.access.fishID, t.access.ownerID, proof.Source.Kind)
	if !ok || source != proof.Source || record.Revision != proof.Revision {
		return apphost.ErrObservationForbidden
	}
	epoch, err := douyinMemoryEpoch(ctx, db, source)
	if err != nil {
		return err
	}
	if epoch != proof.Epoch {
		return companion.ErrMemoryConflict
	}
	return nil
}

func (t *localToolSet) checkDouyinMemoryResults(ctx context.Context) error {
	if len(t.douyinMemoryProofs) == 0 {
		return nil
	}
	record, err := t.douyinMemoryState(ctx)
	if err != nil {
		return err
	}
	db, err := openLocalDouyinMemory(ctx, t.publications.home, false)
	if err != nil {
		return err
	}
	defer db.Close()
	for proof := range t.douyinMemoryProofs {
		if err := t.checkDouyinMemoryProof(ctx, db, record, proof); err != nil {
			return err
		}
	}
	return nil
}

func (t *localToolSet) callDouyinMemory(ctx context.Context, in localWechatMemoryInput) (string, error) {
	if in.localWechatMemorySource != (localWechatMemorySource{}) {
		return "", apphost.ErrObservationInvalid
	}
	record, err := t.douyinMemoryState(ctx)
	if err != nil {
		return "", err
	}
	var db *sql.DB
	if record.Memory != (localDouyinMemoryGrants{}) {
		db, err = openLocalDouyinMemory(ctx, t.publications.home, false)
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		if err != nil {
			return "", err
		}
		if db != nil {
			defer db.Close()
		}
	}
	sources := []localDouyinMemorySource{}
	for _, kind := range localDouyinMemoryKinds {
		if source, ok := douyinMemorySource(record, t.access.fishID, t.access.ownerID, kind); ok {
			_, err := douyinMemoryEpoch(ctx, db, source)
			if errors.Is(err, apphost.ErrObservationForbidden) {
				continue
			}
			if err != nil {
				return "", err
			}
			sources = append(sources, source)
		}
	}
	var value map[string]any
	var proof localDouyinMemoryProof
	var count int
	switch in.Action {
	case "sources":
		if in.SourceID != "" || in.WorkID != "" || in.Query != "" || in.Since != "" || in.Before != "" ||
			in.Cursor != "" || in.Limit != nil || in.Reference != "" || in.Offset != nil || in.Length != nil {
			return "", apphost.ErrObservationInvalid
		}
		value = map[string]any{"sources": sources}
	case "search":
		value, proof, count, err = t.searchDouyinMemory(ctx, db, record, sources, in)
	case "read":
		value, proof, err = t.readDouyinMemory(ctx, db, record, in)
		count = 1
	default:
		return "", apphost.ErrObservationInvalid
	}
	if err != nil {
		return "", err
	}
	current, err := t.douyinMemoryState(ctx)
	if err != nil || current != record {
		return "", apphost.ErrObservationForbidden
	}
	value["app_id"], value["data_role"] = "douyin", "external_memory_evidence"
	result, err := localMemoryJSON(value)
	if err != nil || proof.Epoch == "" {
		return result, err
	}
	if err := t.checkDouyinMemoryProof(ctx, db, current, proof); err != nil {
		return "", err
	}
	if err := t.auditDouyinMemory(ctx, db, proof, in.Action, count, result); err != nil {
		return "", err
	}
	if t.douyinMemoryProofs == nil {
		t.douyinMemoryProofs = map[localDouyinMemoryProof]struct{}{}
	}
	t.douyinMemoryProofs[proof] = struct{}{}
	if err := t.checkDouyinMemoryResults(ctx); err != nil {
		return "", err
	}
	return result, nil
}

func douyinMemoryDate(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", apphost.ErrObservationInvalid
	}
	return at.UTC().Format(localDouyinMemoryTime), nil
}

func (t *localToolSet) searchDouyinMemory(ctx context.Context, db *sql.DB, record localDouyinWebRecord, sources []localDouyinMemorySource, in localWechatMemoryInput) (map[string]any, localDouyinMemoryProof, int, error) {
	invalid := func(err error) (map[string]any, localDouyinMemoryProof, int, error) {
		return nil, localDouyinMemoryProof{}, 0, err
	}
	requested := 5
	if in.Limit != nil {
		requested = *in.Limit
	}
	if requested < 1 || in.Reference != "" || in.Offset != nil || in.Length != nil {
		return invalid(apphost.ErrObservationInvalid)
	}
	const transportLimit = 50 // Source metadata and previews share the tool response.
	limit := min(requested, transportLimit)
	var page localDouyinMemoryPage
	if in.Cursor != "" {
		var ok bool
		page, ok = t.douyinMemoryPages[in.Cursor]
		if !ok || in.SourceID != "" || in.WorkID != "" || in.Query != "" || in.Since != "" || in.Before != "" {
			return invalid(companion.ErrMemoryConflict)
		}
	} else {
		for _, source := range sources {
			if source.Scope == in.SourceID {
				page.Proof.Source = source
				break
			}
		}
		if page.Proof.Source.Scope == "" {
			return invalid(apphost.ErrObservationForbidden)
		}
		page.Terms = strings.Fields(strings.ToLower(in.Query))
		if len(page.Terms) == 0 || len(in.Query) > 1000 || len(page.Terms) > 50 ||
			in.WorkID != "" && !validLocalDouyinCommentID(in.WorkID) {
			return invalid(apphost.ErrObservationInvalid)
		}
		var err error
		page.Since, err = douyinMemoryDate(in.Since)
		if err != nil {
			return invalid(err)
		}
		page.Before, err = douyinMemoryDate(in.Before)
		if err != nil || page.Since != "" && page.Before != "" && page.Since >= page.Before {
			return invalid(apphost.ErrObservationInvalid)
		}
		page.Work = in.WorkID
		page.Proof.Revision = record.Revision
		page.Proof.Epoch, err = douyinMemoryEpoch(ctx, db, page.Proof.Source)
		if err != nil {
			return invalid(err)
		}
	}
	if err := t.checkDouyinMemoryProof(ctx, db, record, page.Proof); err != nil {
		return invalid(err)
	}
	items := []map[string]any{}
	value := map[string]any{"items": items, "next_cursor": "", "requested_limit": requested, "effective_limit": limit,
		"transport_page_limit": transportLimit, "source": page.Proof.Source, "coverage": "本机已留存的抖音读取记录"}
	if page.Proof.Epoch == "" {
		return value, page.Proof, 0, nil
	}
	query := `SELECT id,work,substr(label,1,160),substr(text,MAX(1,instr(lower(text),?)-32),200),url,coverage,observed_at
FROM records WHERE scope=? AND (?='' OR work=?) AND observed_at>=? AND (?='' OR observed_at<?)`
	args := []any{page.Terms[0], page.Proof.Source.Scope, page.Work, page.Work, page.Since, page.Before, page.Before}
	for _, term := range page.Terms {
		query += " AND instr(lower(work||' '||label||' '||text),?)>0"
		args = append(args, term)
	}
	rows, err := db.QueryContext(ctx, query+" ORDER BY observed_at DESC,id LIMIT ? OFFSET ?", append(args, limit+1, page.Offset)...)
	if err != nil {
		return invalid(err)
	}
	defer rows.Close()
	if t.douyinMemoryRefs == nil {
		t.douyinMemoryRefs = map[string]localDouyinMemoryReference{}
		t.douyinMemoryPages = map[string]localDouyinMemoryPage{}
	}
	for rows.Next() {
		if len(items) == limit {
			if page.Offset > math.MaxInt-limit {
				return invalid(apphost.ErrObservationInvalid)
			}
			page.Offset += limit
			cursor := "douyin-cursor-" + identity.RandomToken()
			t.douyinMemoryPages[cursor] = page
			value["next_cursor"] = cursor
			break
		}
		var row localDouyinMemoryRecord
		if err := rows.Scan(&row.ID, &row.Work, &row.Label, &row.Text, &row.URL, &row.Coverage, &row.At); err != nil {
			return invalid(err)
		}
		ref := "douyin-reference-" + identity.RandomToken()
		t.douyinMemoryRefs[ref] = localDouyinMemoryReference{Proof: page.Proof, ID: row.ID}
		items = append(items, map[string]any{"reference": ref, "summary": row.Text, "title": row.Label, "work_id": row.Work,
			"source_url": row.URL, "coverage": row.Coverage, "observed_at": row.At})
	}
	if err := rows.Err(); err != nil {
		return invalid(err)
	}
	value["items"] = items
	return value, page.Proof, len(items), nil
}

func (t *localToolSet) readDouyinMemory(ctx context.Context, db *sql.DB, record localDouyinWebRecord, in localWechatMemoryInput) (map[string]any, localDouyinMemoryProof, error) {
	ref, ok := t.douyinMemoryRefs[in.Reference]
	if !ok || in.SourceID != "" || in.WorkID != "" || in.Query != "" || in.Cursor != "" || in.Limit != nil || in.Since != "" || in.Before != "" {
		return nil, ref.Proof, apphost.ErrObservationForbidden
	}
	if err := t.checkDouyinMemoryProof(ctx, db, record, ref.Proof); err != nil {
		return nil, ref.Proof, err
	}
	offset, length, _, _, _, err := (localMemoryReadInput{ID: ref.ID, Offset: in.Offset, Length: in.Length}).page()
	if err != nil {
		return nil, ref.Proof, err
	}
	var row localDouyinMemoryRecord
	err = db.QueryRowContext(ctx, "SELECT work,label,text,url,coverage,observed_at FROM records WHERE scope=? AND id=?",
		ref.Proof.Source.Scope, ref.ID).Scan(&row.Work, &row.Label, &row.Text, &row.URL, &row.Coverage, &row.At)
	if err != nil {
		return nil, ref.Proof, err
	}
	text, end, err := localMemoryPage(row.Text, offset, length)
	return map[string]any{"source": ref.Proof.Source, "work_id": row.Work, "title": row.Label, "text": text, "source_url": row.URL,
		"coverage": row.Coverage, "observed_at": row.At, "next_offset": end, "has_more": end < len(row.Text), "total_bytes": len(row.Text),
		"requested_length": length, "effective_length": end - offset}, ref.Proof, err
}

func (t *localToolSet) auditDouyinMemory(ctx context.Context, db *sql.DB, proof localDouyinMemoryProof, action string, count int, result string) error {
	if t.douyinMemoryOrigin != nil {
		// The native origin records its own direction and request proof.
		return nil
	}
	searches, reads := 0, 0
	if action == "search" {
		searches++
	} else {
		reads++
	}
	request := localCheckpointKey("station_to_douyin", t.access.fishID, t.access.ownerID, t.traceID)
	_, err := db.ExecContext(ctx, `INSERT INTO audit(request,scope,kind,searches,reads,items,estimated_tokens,updated_at) VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(request,scope) DO UPDATE SET searches=searches+excluded.searches,reads=reads+excluded.reads,items=items+excluded.items,
estimated_tokens=estimated_tokens+excluded.estimated_tokens,updated_at=excluded.updated_at`, request, proof.Source.Scope, proof.Source.Kind,
		searches, reads, count, companion.EstimateTokens(result), time.Now().UTC().Format(localDouyinMemoryTime))
	return err
}
