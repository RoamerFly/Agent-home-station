package station

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/websession"
)

const localDouyinExecutionContextPath = localDouyinExecutionRoundsPath + "/context"

// Media observations are retained only under an explicit works-memory grant.
// A page records what was actually decoded and returned to the Agent. Complete
// means the source field's own pagination completed; visual analysis still
// describes sampled frames, not every frame of the original video.
type localDouyinWorkMediaPage struct {
	Version int                      `json:"version"`
	WorkID  string                   `json:"work_id"`
	Field   apphost.ObservationField `json:"field"`
	Page    apphost.ObservationPage  `json:"page"`
}

type localDouyinWorkContextPage struct {
	Field       apphost.ObservationField  `json:"field"`
	Text        string                    `json:"text,omitempty"`
	Coverage    string                    `json:"coverage"`
	ObservedAt  string                    `json:"observed_at"`
	Complete    bool                      `json:"complete"`
	AudioProof  *apphost.AudioProof       `json:"audio_proof,omitempty"`
	VisualProof *apphost.VisualFrameProof `json:"visual_proof,omitempty"`
}

type localDouyinExecutionWorkContext struct {
	WorkID            string                       `json:"work_id"`
	Ownership         string                       `json:"ownership"`
	CreatorText       string                       `json:"creator_text,omitempty"`
	CreatorCoverage   string                       `json:"creator_coverage,omitempty"`
	CreatorObservedAt string                       `json:"creator_observed_at,omitempty"`
	MediaSHA256       string                       `json:"media_sha256,omitempty"`
	AudioComplete     bool                         `json:"audio_complete"`
	VisualComplete    bool                         `json:"visual_complete"`
	MediaPages        []localDouyinWorkContextPage `json:"media_pages"`
	MediaPageCount    int                          `json:"media_page_count"`
	NextOffset        int                          `json:"next_offset,omitempty"`
	NextCursor        string                       `json:"next_cursor"`
	SnapshotState     string                       `json:"snapshot_state,omitempty"`
	RestartRequired   bool                         `json:"restart_required"`
	EffectiveLimit    int                          `json:"effective_limit"`
	Coverage          string                       `json:"coverage"`
	ReplyEligible     bool                         `json:"reply_eligible"`
}

func (p *localDouyinWebObservation) captureCreatorMediaContext(ctx context.Context, a localDouyinWebAccess,
	target localDouyinWebTarget, field apphost.ObservationField, page apphost.ObservationPage) error {
	if a.record.Memory.Works == "" || target.creatorAccount == "" {
		return nil
	}
	if !validLocalDouyinCommentID(target.videoID) || !creatorOwnerCurrent(a, target.creatorAccount) {
		return apphost.ErrObservationForbidden
	}
	var mediaHash string
	var position string
	switch field {
	case apphost.ObservationAudio:
		if page.AudioProof == nil {
			return apphost.ErrObservationInvalid
		}
		mediaHash = page.AudioProof.MediaSHA256
		position = strconv.FormatFloat(page.AudioProof.StartSeconds, 'f', -1, 64)
	case apphost.ObservationVisual:
		if page.VisualProof == nil {
			return apphost.ErrObservationInvalid
		}
		mediaHash = page.VisualProof.MediaSHA256
		position = strconv.Itoa(page.VisualProof.StartFrame)
	default:
		return apphost.ErrObservationInvalid
	}
	if !localDouyinPublicationHex(mediaHash) {
		return apphost.ErrObservationInvalid
	}
	raw, err := json.Marshal(localDouyinWorkMediaPage{Version: 1, WorkID: target.videoID, Field: field, Page: page})
	if err != nil || len(raw) > 128<<10 {
		return apphost.ErrObservationWebStorageUnavailable
	}
	return p.home.douyinWeb.captureMemory(ctx, a.record, douyinMemoryWorks, target.creatorAccount,
		[]localDouyinMemoryRecord{{ID: "media:" + string(field) + ":" + mediaHash + ":" + position,
			Work: target.videoID, Label: "本人作品视频" + string(field) + "证据", Text: string(raw),
			URL: target.url, Coverage: page.Coverage, At: time.Now().UTC().Format(localDouyinMemoryTime)}})
}

func (m *localDouyinWebLogin) executionRoundContextRoutes(owner *websession.Store, mux *http.ServeMux) {
	mux.HandleFunc("GET "+localDouyinExecutionContextPath, owner.Protect(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		scope, current, ok := m.executionRoundReadScope(owner, w, r)
		if !ok {
			return
		}
		query := r.URL.Query()
		if len(query) < 2 || len(query) > 5 {
			websession.Fail(w, http.StatusBadRequest, "作品上下文查询参数无效")
			return
		}
		for key, values := range query {
			if (key != "date" && key != "work_id" && key != "offset" && key != "limit" && key != "cursor") || len(values) != 1 {
				websession.Fail(w, http.StatusBadRequest, "作品上下文查询参数无效")
				return
			}
		}
		day, workID := query.Get("date"), query.Get("work_id")
		parsed, err := time.Parse("2006-01-02", day)
		if err != nil || parsed.Format("2006-01-02") != day || !validLocalDouyinCommentID(workID) {
			websession.Fail(w, http.StatusBadRequest, "请提供有效的日期和作品编号")
			return
		}
		offset, limit := 0, 20
		cursor := query.Get("cursor")
		if query.Has("cursor") && (cursor == "" || len(cursor) > localDouyinExecutionContextCursorMax || query.Has("offset")) {
			websession.Fail(w, http.StatusBadRequest, "作品上下文续页位置无效")
			return
		}
		if query.Has("offset") {
			offset, err = strconv.Atoi(query.Get("offset"))
			if err != nil || offset < 0 {
				websession.Fail(w, http.StatusBadRequest, "作品上下文续读位置无效")
				return
			}
		}
		if query.Has("limit") {
			limit, err = strconv.Atoi(query.Get("limit"))
			if err != nil || limit < 1 || limit > 1000 {
				websession.Fail(w, http.StatusBadRequest, "作品上下文单页数量须为 1–1000")
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if err := current(); err != nil {
			localDouyinNoticeFailure(w, err)
			return
		}
		identity, err := m.home.identity.Read()
		if err != nil || identity.LocalOwner == nil {
			websession.Fail(w, http.StatusForbidden, "本机主人身份已改变")
			return
		}
		record, _, err := m.noticeState(identity.FishID)
		if err != nil || scope != localDouyinExecutionScope(identity.FishID, identity.LocalOwner.ID, record) {
			websession.Fail(w, http.StatusForbidden, "抖音通知授权已改变")
			return
		}
		result, err := m.executionWorkContextPage(ctx, scope, record, identity.FishID, identity.LocalOwner.ID, day, workID, offset, limit, cursor)
		if err != nil {
			if errors.Is(err, errLocalDouyinExecutionContextCursor) {
				websession.Fail(w, http.StatusBadRequest, "作品上下文续页位置无效")
			} else {
				websession.Fail(w, http.StatusServiceUnavailable, "作品上下文读取失败")
			}
			return
		}
		if err := current(); err != nil {
			localDouyinNoticeFailure(w, err)
			return
		}
		websession.Respond(w, http.StatusOK, result)
	}))
}

func (m *localDouyinWebLogin) executionWorkContext(ctx context.Context, scope string, record localDouyinWebRecord,
	fish, owner, day, workID string, offset, limit int) (localDouyinExecutionWorkContext, error) {
	return m.executionWorkContextWithMedia(ctx, scope, record, fish, owner, day, workID, offset, limit, true)
}

func (m *localDouyinWebLogin) executionWorkOwnership(ctx context.Context, scope string, record localDouyinWebRecord,
	fish, owner, day, workID string) (localDouyinExecutionWorkContext, error) {
	return m.executionWorkContextWithMedia(ctx, scope, record, fish, owner, day, workID, 0, 1, false)
}

func (m *localDouyinWebLogin) executionWorkContextWithMedia(ctx context.Context, scope string, record localDouyinWebRecord,
	fish, owner, day, workID string, offset, limit int, includeMedia bool) (localDouyinExecutionWorkContext, error) {
	result := localDouyinExecutionWorkContext{WorkID: workID, Ownership: "unverified", MediaPages: []localDouyinWorkContextPage{},
		EffectiveLimit: limit, Coverage: "通知作品编号尚未核实归属；无作品内容", ReplyEligible: false}
	rounds, err := openLocalDouyinExecutionDB(ctx, m.home, false)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer rounds.Close()
	var total, accounted int
	var minimum, maximum sql.NullString
	// Older ledgers did not retain a browser-verified creator account.
	var tableCount int
	if err := rounds.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='round_creator_accounts'`).Scan(&tableCount); err != nil {
		return result, err
	}
	if tableCount == 0 {
		return result, nil
	}
	err = rounds.QueryRowContext(ctx, `SELECT count(*),count(a.creator_account),min(a.creator_account),max(a.creator_account)
FROM rounds r LEFT JOIN round_creator_accounts a ON a.scope=r.scope AND a.notice_id=r.notice_id
WHERE r.scope=? AND r.observed_date=? AND r.work_id=?`, scope, day, workID).Scan(&total, &accounted, &minimum, &maximum)
	if err != nil {
		return result, err
	}
	if total == 0 || accounted != total || !minimum.Valid || minimum.String != maximum.String || !validDouyinNoticeAccount(minimum.String) {
		return result, nil
	}
	source, granted := douyinMemorySource(record, fish, owner, douyinMemoryWorks)
	if !granted {
		result.Coverage = "通知账号可核对；尚未授权保留本人作品信息"
		return result, nil
	}
	access := (&localDouyinWebObservation{home: m.home}).access(fish, owner, record, m.view(record))
	if !creatorOwnerCurrent(access, minimum.String) {
		result.Coverage = "通知账号与当前创作者账号的归属尚未核对"
		return result, nil
	}
	memory, err := openLocalDouyinMemory(ctx, m.home, false)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer memory.Close()
	var pinned string
	err = memory.QueryRowContext(ctx, `SELECT account FROM bindings WHERE id=?`, source.binding).Scan(&pinned)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if pinned == "" || pinned != localCheckpointKey(source.binding, minimum.String) {
		return result, nil
	}
	var text, coverage, observedAt string
	err = memory.QueryRowContext(ctx, `SELECT text,coverage,observed_at FROM records WHERE scope=? AND id=? AND work=?`, source.Scope, workID, workID).
		Scan(&text, &coverage, &observedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if len(text) > 128<<10 || !strings.HasPrefix(text, "作品编号 "+workID+"\n") {
		return result, nil
	}
	result.Ownership = "verified_own_work"
	result.CreatorText, result.CreatorCoverage, result.CreatorObservedAt = text, coverage, observedAt
	result.Coverage = "通知账号与当前创作者管理页作品相符；视频内容仅覆盖已读取的证据页"
	if !includeMedia {
		return result, nil
	}
	return m.readExecutionWorkMedia(ctx, memory, source, workID, offset, limit, result)
}

type localDouyinStoredWorkMedia struct {
	page       localDouyinWorkContextPage
	hash       string
	startAudio float64
	endAudio   float64
	startFrame int
	nextFrame  int
}

func decodeLocalDouyinWorkMedia(id, text, observedAt, workID string) (localDouyinStoredWorkMedia, bool) {
	if len(text) > 128<<10 {
		return localDouyinStoredWorkMedia{}, false
	}
	var stored localDouyinWorkMediaPage
	if strictLocalMemoryDecode([]byte(text), &stored) != nil || stored.Version != 1 || stored.WorkID != workID ||
		len(stored.Page.Chunks) > 1 {
		return localDouyinStoredWorkMedia{}, false
	}
	item := localDouyinStoredWorkMedia{page: localDouyinWorkContextPage{Field: stored.Field,
		Coverage: stored.Page.Coverage, ObservedAt: observedAt, Complete: stored.Page.Complete,
		AudioProof: stored.Page.AudioProof, VisualProof: stored.Page.VisualProof}}
	if len(stored.Page.Chunks) == 1 {
		item.page.Text = stored.Page.Chunks[0].Text
	}
	var position string
	switch stored.Field {
	case apphost.ObservationAudio:
		if stored.Page.AudioProof == nil || stored.Page.VisualProof != nil || !localDouyinPublicationHex(stored.Page.AudioProof.MediaSHA256) {
			return localDouyinStoredWorkMedia{}, false
		}
		item.hash = stored.Page.AudioProof.MediaSHA256
		item.startAudio, item.endAudio = stored.Page.AudioProof.StartSeconds, stored.Page.AudioProof.WindowEndSeconds
		position = strconv.FormatFloat(item.startAudio, 'f', -1, 64)
	case apphost.ObservationVisual:
		if stored.Page.VisualProof == nil || stored.Page.AudioProof != nil || !localDouyinPublicationHex(stored.Page.VisualProof.MediaSHA256) {
			return localDouyinStoredWorkMedia{}, false
		}
		item.hash = stored.Page.VisualProof.MediaSHA256
		item.startFrame, item.nextFrame = stored.Page.VisualProof.StartFrame, stored.Page.VisualProof.NextFrame
		position = strconv.Itoa(item.startFrame)
	default:
		return localDouyinStoredWorkMedia{}, false
	}
	if id != "media:"+string(stored.Field)+":"+item.hash+":"+position {
		return localDouyinStoredWorkMedia{}, false
	}
	return item, true
}

func (m *localDouyinWebLogin) readExecutionWorkMedia(ctx context.Context, db *sql.DB, source localDouyinMemorySource,
	workID string, offset, limit int, result localDouyinExecutionWorkContext) (localDouyinExecutionWorkContext, error) {
	var newestID, newestText, newestAt string
	err := db.QueryRowContext(ctx, `SELECT id,text,observed_at FROM records WHERE scope=? AND work=? AND id LIKE 'media:%' ORDER BY observed_at DESC,id LIMIT 1`,
		source.Scope, workID).Scan(&newestID, &newestText, &newestAt)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	newest, valid := decodeLocalDouyinWorkMedia(newestID, newestText, newestAt, workID)
	if !valid {
		result.Coverage += "；最新视频证据页无法核验，覆盖待核对"
		return result, nil
	}
	// Use the newest observed original. A later partial reread must not inherit
	// "complete" from evidence for different media bytes.
	result.MediaSHA256 = newest.hash
	audioPattern := "media:audio:" + newest.hash + ":%"
	visualPattern := "media:visual:" + newest.hash + ":%"
	err = db.QueryRowContext(ctx, `SELECT count(*) FROM records WHERE scope=? AND work=? AND (id LIKE ? OR id LIKE ?)`,
		source.Scope, workID, audioPattern, visualPattern).Scan(&result.MediaPageCount)
	if err != nil {
		return result, err
	}
	if result.MediaPageCount <= 4096 {
		rows, err := db.QueryContext(ctx, `SELECT id,text,observed_at FROM records WHERE scope=? AND work=? AND (id LIKE ? OR id LIKE ?) ORDER BY observed_at DESC,id`,
			source.Scope, workID, audioPattern, visualPattern)
		if err != nil {
			return result, err
		}
		proofs := make([]localDouyinStoredWorkMedia, 0, result.MediaPageCount)
		validProofs := true
		for rows.Next() {
			var id, text, observedAt string
			if err := rows.Scan(&id, &text, &observedAt); err != nil {
				rows.Close()
				return result, err
			}
			item, ok := decodeLocalDouyinWorkMedia(id, text, observedAt, workID)
			if !ok || item.hash != newest.hash {
				validProofs = false
				continue
			}
			item.page.Text = "" // Retain only bounded proof metadata for completeness checks.
			proofs = append(proofs, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return result, err
		}
		if err := rows.Close(); err != nil {
			return result, err
		}
		if validProofs {
			result.AudioComplete, result.VisualComplete = completeLocalDouyinWorkMedia(proofs)
		} else {
			result.Coverage += "；部分视频证据页无法核验，完整覆盖待核对"
		}
	} else {
		result.Coverage += "；视频证据超过 4096 页，完整覆盖待核对；可按 offset 继续分页读取"
	}
	if offset >= result.MediaPageCount {
		return result, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT id,text,observed_at FROM records WHERE scope=? AND work=? AND (id LIKE ? OR id LIKE ?) ORDER BY observed_at DESC,id LIMIT ? OFFSET ?`,
		source.Scope, workID, audioPattern, visualPattern, limit, offset)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, text, observedAt string
		if err := rows.Scan(&id, &text, &observedAt); err != nil {
			return result, err
		}
		if item, ok := decodeLocalDouyinWorkMedia(id, text, observedAt, workID); ok && item.hash == newest.hash {
			result.MediaPages = append(result.MediaPages, item.page)
		}
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	end := result.MediaPageCount
	if limit < result.MediaPageCount-offset {
		end = offset + limit
	}
	if end < result.MediaPageCount {
		result.NextOffset = end
	}
	return result, nil
}

func completeLocalDouyinWorkMedia(rows []localDouyinStoredWorkMedia) (audio, visual bool) {
	// Pages arrive newest first; logical position establishes coverage.
	audios, visuals := map[float64]localDouyinStoredWorkMedia{}, map[int]localDouyinStoredWorkMedia{}
	var audioDuration float64
	var visualDuration float64
	var frameCount, frameStep int
	validAudio, validVisual := true, true
	for _, row := range rows {
		if row.page.AudioProof != nil {
			proof := row.page.AudioProof
			if math.IsNaN(proof.StartSeconds) || math.IsNaN(proof.WindowEndSeconds) || math.IsNaN(proof.MediaDurationSeconds) ||
				math.IsInf(proof.StartSeconds, 0) || math.IsInf(proof.WindowEndSeconds, 0) || math.IsInf(proof.MediaDurationSeconds, 0) ||
				proof.StartSeconds < 0 || proof.WindowEndSeconds <= proof.StartSeconds || proof.MediaDurationSeconds <= 0 ||
				proof.WindowEndSeconds > proof.MediaDurationSeconds+1e-6 ||
				(audioDuration > 0 && math.Abs(audioDuration-proof.MediaDurationSeconds) > 1e-6) {
				validAudio = false
				continue
			}
			if audioDuration == 0 {
				audioDuration = proof.MediaDurationSeconds
			}
			if _, found := audios[row.startAudio]; !found {
				audios[row.startAudio] = row
			}
		} else if row.page.VisualProof != nil {
			proof := row.page.VisualProof
			if math.IsNaN(proof.MediaDurationSeconds) || math.IsInf(proof.MediaDurationSeconds, 0) || proof.MediaDurationSeconds <= 0 ||
				proof.FrameCount < 1 || proof.FrameStep < 1 || proof.StartFrame < 0 || proof.NextFrame <= proof.StartFrame ||
				proof.NextFrame > proof.FrameCount || len(proof.FrameIndexes) == 0 || len(proof.FrameIndexes) != len(proof.TimestampSeconds) ||
				(frameCount > 0 && (frameCount != proof.FrameCount || frameStep != proof.FrameStep || math.Abs(visualDuration-proof.MediaDurationSeconds) > 1e-6)) {
				validVisual = false
				continue
			}
			for index, frame := range proof.FrameIndexes {
				if frame < proof.StartFrame || frame >= proof.NextFrame || index > 0 && frame <= proof.FrameIndexes[index-1] ||
					math.IsNaN(proof.TimestampSeconds[index]) || math.IsInf(proof.TimestampSeconds[index], 0) ||
					proof.TimestampSeconds[index] < 0 || proof.TimestampSeconds[index] > proof.MediaDurationSeconds+1e-6 ||
					(index == 0 && frame != proof.StartFrame) ||
					(index > 0 && frame-proof.FrameIndexes[index-1] != proof.FrameStep) ||
					(index > 0 && proof.TimestampSeconds[index] < proof.TimestampSeconds[index-1]) {
					validVisual = false
				}
			}
			last := proof.FrameIndexes[len(proof.FrameIndexes)-1]
			if proof.NextFrame != last+min(proof.FrameStep, proof.FrameCount-last) {
				validVisual = false
			}
			if frameCount == 0 {
				frameCount, frameStep, visualDuration = proof.FrameCount, proof.FrameStep, proof.MediaDurationSeconds
			}
			if _, found := visuals[row.startFrame]; !found {
				visuals[row.startFrame] = row
			}
		}
	}
	if !validAudio {
		audios = nil
	}
	if !validVisual {
		visuals = nil
	}
	for position := float64(0); ; {
		row, found := audios[position]
		if !found || row.endAudio <= position {
			break
		}
		if row.page.AudioProof.State == "no_audio_track" && row.page.Complete {
			audio = true
			break
		}
		if row.page.Complete && math.Abs(row.endAudio-audioDuration) <= 1e-6 {
			audio = true
			break
		}
		position = row.endAudio
	}
	for position := 0; ; {
		row, found := visuals[position]
		if !found || row.nextFrame <= position {
			break
		}
		if row.page.Complete && row.nextFrame == frameCount {
			visual = true
			break
		}
		position = row.nextFrame
	}
	if audio && visual && math.Abs(audioDuration-visualDuration) > 1e-3 {
		return false, false
	}
	return audio, visual
}
