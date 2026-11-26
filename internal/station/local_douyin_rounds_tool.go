package station

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/mcptools"
)

const localDouyinRoundsToolName = "douyin_rounds"

// The tool reads only the existing local ledger. It never starts a browser
// observation, changes notice read state, or submits a comment.
func localDouyinRoundsTool() mcptools.Tool {
	return mcptools.NewTool(localDouyinRoundsToolName,
		"只读查询本机已保存的抖音执行回合。works 按日期列作品组，按 next_cursor 续读；notices 必须指定 work_id（空字符串表示缺少作品编号的组），按 next_before 续读；context 必须指定作品编号，按 next_offset 续读已保存的简介、音轨转写和画面采样页。三个动作均不访问抖音、不标记已读、不发送回复。只有已绑定且授权 Agent 读取互动通知的账号可用；reply_eligible=false 不能据此回复。",
		`{"type":"object","properties":{"action":{"type":"string","enum":["works","notices","context"]},"date":{"type":"string","pattern":"^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},"work_id":{"type":"string","pattern":"^([1-9][0-9]{0,19})?$"},"limit":{"type":"integer","minimum":1,"maximum":1000},"cursor":{"type":"string","minLength":1,"maxLength":256},"before":{"type":"string","pattern":"^[1-9][0-9]*$"},"offset":{"type":"integer","minimum":0}},"required":["action","date"],"additionalProperties":false}`)
}

type localDouyinRoundsInput struct {
	Action string `json:"action"`
	Date   string `json:"date"`
	WorkID string `json:"work_id"`
	Limit  int    `json:"limit"`
	Cursor string `json:"cursor"`
	Before string `json:"before"`
	Offset int    `json:"offset"`
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
	default:
		return in, 0, mcptools.ArgumentError("$.action", "请选择 works、notices 或 context")
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
