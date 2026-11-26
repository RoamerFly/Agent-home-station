package station

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/hostfile"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/stationbrowser"
	"wechat-bot/internal/workspacefs"
)

const localDouyinRoundsToolName = "douyin_rounds"

// The tool never changes notice read state or submits a comment. Only locate
// may read the current creator works list; the other actions use saved data.
func localDouyinRoundsTool() mcptools.Tool {
	return mcptools.NewTool(localDouyinRoundsToolName,
		"只读查询抖音执行回合。works 按 date 列作品组，以 next_cursor 续页；notices 必须指定 work_id，以 next_before 在该作品内续页；context 按 next_offset 读已保存的简介、音轨与采样画面。locate 仅为已有通知的 work_id 从当前创作者作品列表逐页寻找本人作品，找到后返回临时 content_id 和可观察字段；不读评论、音画或调用视觉模型。locate 首次省略 resume，后续传 resume=true 和相同 page_size；每次 pages 控制最多处理的列表页数，未完成继续调用。所有动作均不回复或投稿，reply_eligible=false。",
		`{"type":"object","properties":{"action":{"type":"string","enum":["works","notices","context","locate"]},"date":{"type":"string","pattern":"^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},"work_id":{"type":"string","pattern":"^([1-9][0-9]{0,19})?$"},"limit":{"type":"integer","minimum":1,"maximum":1000},"cursor":{"type":"string","minLength":1,"maxLength":256},"before":{"type":"string","pattern":"^[1-9][0-9]*$"},"offset":{"type":"integer","minimum":0},"pages":{"type":"integer","minimum":1,"maximum":1000},"page_size":{"type":"integer","minimum":1,"maximum":1000},"resume":{"type":"boolean"}},"required":["action","date"],"additionalProperties":false}`)
}

type localDouyinRoundsInput struct {
	Action   string `json:"action"`
	Date     string `json:"date"`
	WorkID   string `json:"work_id"`
	Limit    int    `json:"limit"`
	Cursor   string `json:"cursor"`
	Before   string `json:"before"`
	Offset   int    `json:"offset"`
	Pages    int    `json:"pages"`
	PageSize int    `json:"page_size"`
	Resume   bool   `json:"resume"`
}

// The source proof tracks authorization and ownership, not a memory-generation
// epoch. A later observation may legitimately add pages for the same work in
// this turn; the Agent can then read context again without losing its tools.
type localDouyinRoundContextProof struct {
	Source   localDouyinMemorySource
	Revision uint64
	Date     string
	WorkID   string
}

func parseLocalDouyinRoundsInput(raw json.RawMessage) (localDouyinRoundsInput, int64, error) {
	var in localDouyinRoundsInput
	if strictLocalMemoryDecode(raw, &in) != nil {
		return in, 0, mcptools.ArgumentError("$", "执行回合参数无效")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return in, 0, mcptools.ArgumentError("$", "执行回合参数无效")
	}
	day, err := time.Parse("2006-01-02", in.Date)
	if err != nil || day.Format("2006-01-02") != in.Date {
		return in, 0, mcptools.ArgumentError("$.date", "请提供有效的 YYYY-MM-DD 日期")
	}
	if _, ok := fields["limit"]; ok && (in.Limit < 1 || in.Limit > localDouyinExecutionRoundBatch) {
		return in, 0, mcptools.ArgumentError("$.limit", "单页数量须为 1–1000；更多内容按返回的续页位置读取")
	}
	if in.Limit == 0 {
		in.Limit = localDouyinExecutionRoundLimit
	}
	allowed := map[string]bool{"action": true, "date": true, "limit": true}
	var before int64
	switch in.Action {
	case "works":
		allowed["cursor"] = true
		if _, ok := fields["cursor"]; ok && (in.Cursor == "" || len(in.Cursor) > 256) {
			return in, 0, mcptools.ArgumentError("$.cursor", "作品组续页位置无效")
		}
	case "notices":
		allowed["work_id"], allowed["before"] = true, true
		if _, ok := fields["work_id"]; !ok || !validLocalDouyinRoundWorkID(in.WorkID) {
			return in, 0, mcptools.ArgumentError("$.work_id", "请指定作品编号；空字符串只表示通知缺少作品编号的分组")
		}
		if _, ok := fields["before"]; ok {
			before, err = strconv.ParseInt(in.Before, 10, 64)
			if err != nil || before < 1 {
				return in, 0, mcptools.ArgumentError("$.before", "通知续页位置无效")
			}
		}
	case "context":
		allowed["work_id"], allowed["offset"] = true, true
		if !validLocalDouyinCommentID(in.WorkID) {
			return in, 0, mcptools.ArgumentError("$.work_id", "请指定有效的作品编号")
		}
		if in.Offset < 0 {
			return in, 0, mcptools.ArgumentError("$.offset", "作品证据续页位置须为非负整数")
		}
	case "locate":
		delete(allowed, "limit")
		allowed["work_id"], allowed["pages"], allowed["page_size"], allowed["resume"] = true, true, true, true
		if !validLocalDouyinCommentID(in.WorkID) {
			return in, 0, mcptools.ArgumentError("$.work_id", "请指定通知中已有的有效作品编号")
		}
		if _, ok := fields["pages"]; ok && (in.Pages < 1 || in.Pages > 1000) {
			return in, 0, mcptools.ArgumentError("$.pages", "每次可读取 1–1000 个列表页，超出本轮时间时返回续办位置")
		}
		if _, ok := fields["page_size"]; ok && (in.PageSize < 1 || in.PageSize > 1000) {
			return in, 0, mcptools.ArgumentError("$.page_size", "每页可读取 1–1000 个作品，续办时沿用首次设置")
		}
		if in.Pages == 0 {
			in.Pages = 2
		}
		if in.PageSize == 0 {
			in.PageSize = 20
		}
	default:
		return in, 0, mcptools.ArgumentError("$.action", "请选择 works、notices、context 或 locate")
	}
	for field := range fields {
		if !allowed[field] {
			return in, 0, mcptools.ArgumentError("$."+field, "此动作不接受该参数")
		}
	}
	return in, before, nil
}

func validLocalDouyinRoundWorkID(workID string) bool {
	return workID == "" || validLocalDouyinCommentID(workID)
}

// A locate result exposes only the exact work named by a saved notice. Other
// creator-list rows are consumed by the host and never returned to the Agent.
type localDouyinRoundLocateResult struct {
	Date           string                     `json:"date"`
	WorkID         string                     `json:"work_id"`
	State          string                     `json:"state"`
	Ownership      string                     `json:"ownership"`
	RequestedPages int                        `json:"requested_pages"`
	PagesRead      int                        `json:"pages_read"`
	PageSize       int                        `json:"page_size"`
	WorksScanned   int                        `json:"works_scanned"`
	Resumed        bool                       `json:"resumed"`
	CanResume      bool                       `json:"can_resume"`
	StopReason     string                     `json:"stop_reason,omitempty"`
	Coverage       string                     `json:"coverage,omitempty"`
	ContentID      string                     `json:"content_id,omitempty"`
	Fields         []apphost.ObservationField `json:"fields,omitempty"`
	ReplyEligible  bool                       `json:"reply_eligible"`
}

func (t *localToolSet) locateDouyinRoundWork(ctx context.Context, m *localDouyinWebLogin, record localDouyinWebRecord,
	scope string, in localDouyinRoundsInput, notice localDouyinNoticeProof) (localDouyinRoundLocateResult, *localDouyinRoundContextProof, error) {
	result := localDouyinRoundLocateResult{Date: in.Date, WorkID: in.WorkID, State: "pending", Ownership: "unverified",
		RequestedPages: in.Pages, PageSize: in.PageSize, CanResume: true, ReplyEligible: false}
	if t.observations == nil {
		return result, nil, apphost.ErrObservationNotFound
	}
	if _, granted := douyinMemorySource(record, t.access.fishID, t.access.ownerID, douyinMemoryWorks); !granted {
		return result, nil, apphost.ErrObservationForbidden
	}
	// The work ID must already belong to this account-scoped, saved notice day.
	group, err := m.listExecutionRoundsForWork(ctx, scope, in.Date, 0, 1, &in.WorkID)
	if err != nil {
		return result, nil, err
	}
	if len(group.Rounds) == 0 {
		return result, nil, mcptools.ArgumentError("$.work_id", "所选日期没有该作品的已保存互动通知")
	}
	// Each target has an independent durable Observer cursor. A generic
	// app_observe(account_works) session cannot advance another work's scan.
	dir := filepath.Join(stationObservationsDir(m.home.dataDir), "douyin-round-locate",
		localCheckpointKey(scope, in.Date, in.WorkID))
	root, err := workspacefs.EnsurePath(dir)
	if err != nil {
		return result, nil, apphost.ErrObservationWebStorageUnavailable
	}
	if err := root.Close(); err != nil {
		return result, nil, apphost.ErrObservationWebStorageUnavailable
	}
	release, err := hostfile.TryLock(filepath.Join(dir, "scan.lock"))
	if err != nil {
		return result, nil, apphost.ErrObservationWebSessionBusy
	}
	defer release()
	observer := apphost.NewObserver(t.publications.applications, dir)
	request := apphost.ObservationInput{AppID: douyinapp.ID, ConnectionID: douyinapp.WebReadConnectionID,
		ContentID: douyinapp.WebWorksContentID, Field: apphost.ObservationAccountWorks, Limit: in.PageSize}
	for result.PagesRead < in.Pages {
		if deadline, exists := ctx.Deadline(); exists && time.Until(deadline) < 50*time.Second {
			result.StopReason = "turn_time_budget"
			return result, nil, nil
		}
		if err := t.checkNoticeProof(ctx, notice); err != nil {
			return result, nil, err
		}
		request.Refresh = result.PagesRead == 0 && !in.Resume
		page, readErr := observer.Read(ctx, request)
		if readErr != nil {
			if err := t.checkNoticeProof(ctx, notice); err != nil {
				return result, nil, err
			}
			if result.PagesRead == 0 {
				return result, nil, readErr
			}
			if errors.Is(readErr, apphost.ErrObservationWebSnapshotChanged) {
				result.State, result.CanResume, result.StopReason = "restart_required", false, "creator_works_changed"
			} else if errors.Is(readErr, apphost.ErrObservationAccountWorksPageSize) {
				result.StopReason = "page_size_mismatch"
			} else {
				result.StopReason = "creator_works_read_failed"
			}
			return result, nil, nil
		}
		if err := observer.Check(ctx, page); err != nil {
			return result, nil, err
		}
		if err := t.checkNoticeProof(ctx, notice); err != nil {
			return result, nil, err
		}
		t.appReads = append(t.appReads, page)
		if result.PagesRead == 0 {
			result.Resumed = page.Resumed
		}
		result.PagesRead++
		result.WorksScanned += len(page.Chunks)
		result.Coverage = page.Coverage
		for _, chunk := range page.Chunks {
			if chunk.SourceID != in.WorkID {
				continue
			}
			token, valid := strings.CutPrefix(chunk.FollowUpContentID, douyinapp.WebCreatorWorkPrefix)
			if !valid || !validLocalDouyinWebID(token) || chunk.SourceURL != stationbrowser.DouyinCreatorManageURL ||
				chunk.FollowUpField != apphost.ObservationPageText || !slices.Contains(chunk.FollowUpFields, apphost.ObservationPageText) {
				return result, nil, apphost.ErrObservationInvalid
			}
			owned, err := m.executionWorkOwnership(ctx, scope, record, t.access.fishID, t.access.ownerID, in.Date, in.WorkID)
			if err != nil {
				return result, nil, err
			}
			if owned.Ownership != "verified_own_work" {
				result.State, result.CanResume, result.StopReason = "ownership_unverified", false, "notice_creator_account_mismatch"
				return result, nil, nil
			}
			source, granted := douyinMemorySource(record, t.access.fishID, t.access.ownerID, douyinMemoryWorks)
			if !granted {
				return result, nil, apphost.ErrObservationForbidden
			}
			result.State, result.Ownership, result.CanResume = "found", owned.Ownership, false
			result.ContentID, result.Fields = chunk.FollowUpContentID, append([]apphost.ObservationField(nil), chunk.FollowUpFields...)
			proof := &localDouyinRoundContextProof{Source: source, Revision: record.Revision, Date: in.Date, WorkID: in.WorkID}
			return result, proof, nil
		}
		if page.Complete {
			result.State, result.CanResume = "not_in_returned_works", false
			if page.Cached {
				result.State, result.StopReason = "refresh_required", "completed_scan_has_no_cached_work_handles"
			}
			return result, nil, nil
		}
	}
	result.StopReason = "page_budget"
	return result, nil, nil
}

func (t *localToolSet) callDouyinRounds(ctx context.Context, raw json.RawMessage) (string, error) {
	if !t.douyinMemoryRequested || t.publications == nil || t.run == nil || !t.run.Tracked ||
		t.access == nil || t.access.home != t.publications.home || t.checkAppObservationIdentity() != nil || t.run.CheckSource(ctx) != nil {
		return "", errLocalDouyinNoticeGrant
	}
	if _, ok := t.publications.applications.Lookup(douyinapp.ID); !ok {
		return "", apphost.ErrObservationNotFound
	}
	in, before, err := parseLocalDouyinRoundsInput(raw)
	if err != nil {
		return "", err
	}
	m := t.publications.home.douyinWeb
	if m == nil {
		return "", errLocalDouyinNoticeGrant
	}
	record, profile, err := m.noticeState(t.access.fishID)
	if err != nil || !record.NoticeAgent || record.NoticeAccount == "" {
		return "", errLocalDouyinNoticeGrant
	}
	proof := localDouyinNoticeProof{record: record, profile: profile}
	if err := t.checkNoticeProof(ctx, proof); err != nil {
		return "", err
	}
	scope := localDouyinExecutionScope(t.access.fishID, t.access.ownerID, record)
	var out any
	var contextProof *localDouyinRoundContextProof
	switch in.Action {
	case "works":
		out, err = m.listExecutionWorkGroups(ctx, scope, in.Date, in.Limit, in.Cursor)
	case "notices":
		out, err = m.listExecutionRoundsForWork(ctx, scope, in.Date, before, in.Limit, &in.WorkID)
	case "context":
		var result localDouyinExecutionWorkContext
		result, err = m.executionWorkContext(ctx, scope, record, t.access.fishID, t.access.ownerID,
			in.Date, in.WorkID, in.Offset, in.Limit)
		if err == nil && result.Ownership == "verified_own_work" {
			var source localDouyinMemorySource
			var granted bool
			source, granted = douyinMemorySource(record, t.access.fishID, t.access.ownerID, douyinMemoryWorks)
			if !granted {
				return "", apphost.ErrObservationForbidden
			}
			contextProof = &localDouyinRoundContextProof{Source: source, Revision: record.Revision,
				Date: in.Date, WorkID: in.WorkID}
		}
		out = result
	case "locate":
		var result localDouyinRoundLocateResult
		result, contextProof, err = t.locateDouyinRoundWork(ctx, m, record, scope, in, proof)
		out = result
	}
	if err != nil {
		return "", err
	}
	if err := t.checkNoticeProof(ctx, proof); err != nil {
		return "", err
	}
	if contextProof != nil {
		if err := t.checkDouyinRoundContextProof(ctx, *contextProof); err != nil {
			return "", err
		}
	}
	value, err := localProcessJSON(out)
	if err != nil {
		return "", err
	}
	t.noticeReads = append(t.noticeReads, proof)
	if contextProof != nil {
		t.douyinRoundProofs = append(t.douyinRoundProofs, *contextProof)
	}
	return value, nil
}

func (t *localToolSet) checkDouyinRoundContextProof(ctx context.Context, proof localDouyinRoundContextProof) error {
	if err := t.checkAppObservationIdentity(); err != nil {
		return err
	}
	record, err := t.douyinMemoryState(ctx)
	if err != nil {
		return err
	}
	source, ok := douyinMemorySource(record, t.access.fishID, t.access.ownerID, douyinMemoryWorks)
	if !ok || source != proof.Source || record.Revision != proof.Revision {
		return apphost.ErrObservationForbidden
	}
	m := t.publications.home.douyinWeb
	if m == nil {
		return apphost.ErrObservationForbidden
	}
	result, err := m.executionWorkOwnership(ctx, localDouyinExecutionScope(t.access.fishID, t.access.ownerID, record),
		record, t.access.fishID, t.access.ownerID, proof.Date, proof.WorkID)
	if err != nil {
		return err
	}
	if result.Ownership != "verified_own_work" {
		return apphost.ErrObservationForbidden
	}
	return nil
}

func (t *localToolSet) checkDouyinRoundContextProofs(ctx context.Context) error {
	for _, proof := range t.douyinRoundProofs {
		if err := t.checkDouyinRoundContextProof(ctx, proof); err != nil {
			return err
		}
	}
	return nil
}
