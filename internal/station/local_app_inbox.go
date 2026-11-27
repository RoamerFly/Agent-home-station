package station

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/stationbrowser"
)

const localInboxToolName = "app_inbox"

func localInboxTool() mcptools.Tool {
	return mcptools.NewTool(localInboxToolName,
		"读取已授权应用的互动通知，目前支持抖音。status 查看授权，read 读取通知；网页可能标记已读，须传 allow_mark_read=true。pages 默认 1，可选 1–5；按 continuation 续读。返回去重通知、实际覆盖和续读位置。rendered_text 是列表可见文字，按通知编号与来源核对。",
		`{"type":"object","properties":{"action":{"type":"string","enum":["status","read"]},"app_id":{"type":"string","enum":["douyin"]},"allow_mark_read":{"type":"boolean"},"pages":{"type":"integer","minimum":1,"maximum":5},"continuation":{"type":"string"}},"required":["action","app_id"],"additionalProperties":false}`)
}

type localInboxInput struct {
	Action        string `json:"action"`
	AppID         string `json:"app_id"`
	AllowMarkRead bool   `json:"allow_mark_read"`
	Pages         int    `json:"pages"`
	Continuation  string `json:"continuation"`
}

type localInboxCheckpoint struct {
	Version      int                      `json:"version"`
	State        string                   `json:"state"`
	BodyRetained bool                     `json:"body_retained"`
	Notices      []localPublicationNotice `json:"notices"`
}

type localInboxToolError struct {
	status  int
	message string
}

func (e *localInboxToolError) Error() string { return e.message }
func (e *localInboxToolError) SafeToolFailure() (string, string) {
	return "app_inbox_" + strconv.Itoa(e.status), e.message
}

// Proofs retain only authorization metadata for the final chat acceptance.
// Notification text and browser state are never copied into these records.
type localDouyinNoticeProof struct {
	record  localDouyinWebRecord
	profile string
}

func (t *localToolSet) checkNoticeProof(ctx context.Context, proof localDouyinNoticeProof) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.run == nil || t.publications == nil || t.access == nil || t.access.home != t.publications.home || t.run.CheckSource(ctx) != nil || t.checkAppObservationIdentity() != nil {
		return errLocalDouyinNoticeGrant
	}
	m := t.publications.home.douyinWeb
	if m == nil {
		return errLocalDouyinNoticeGrant
	}
	current, path, err := m.noticeProfile(t.access.fishID, proof.record.Revision)
	if err != nil || current != proof.record || path != proof.profile || !current.NoticeAgent || current.NoticeAccount == "" {
		return errLocalDouyinNoticeGrant
	}
	return nil
}

func (t *localToolSet) callInbox(ctx context.Context, raw json.RawMessage) (value string, failure error) {
	defer func() {
		if failure == nil {
			return
		}
		var safe interface{ SafeToolFailure() (string, string) }
		if errors.As(failure, &safe) {
			return
		}
		status, message := localDouyinNoticeError(failure)
		failure = &localInboxToolError{status: status, message: message}
	}()
	if t.publications == nil || t.run == nil || t.store == nil || t.access == nil || t.access.home != t.publications.home || t.checkAppObservationIdentity() != nil || t.run.CheckSource(ctx) != nil {
		return "", errLocalDouyinNoticeGrant
	}
	var in localInboxInput
	if strictLocalMemoryDecode(raw, &in) != nil || in.AppID != "douyin" || (in.Action != "status" && in.Action != "read") {
		return "", mcptools.ArgumentError("$", "请选择 app_id=douyin 的 status 或 read")
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if in.Action == "status" {
		for name := range fields {
			if name != "action" && name != "app_id" {
				return "", mcptools.ArgumentError("$", "status只查询本机授权，请只提供action和app_id")
			}
		}
	}
	if _, ok := t.publications.applications.Lookup(in.AppID); !ok {
		return "", errLocalDouyinNoticeGrant
	}
	m := t.publications.home.douyinWeb
	if m == nil {
		return "", errLocalDouyinNoticeGrant
	}
	record, profile, err := m.noticeState(t.access.fishID)
	if in.Action == "status" {
		return localProcessJSON(map[string]any{"app_id": "douyin", "available": err == nil && record.NoticeAgent && record.NoticeAccount != "", "notice_bound": record.NoticeAccount != "", "notice_agent_enabled": record.NoticeAgent, "mark_read_possible": true})
	}
	if err != nil {
		return "", err
	}
	proof := localDouyinNoticeProof{record: record, profile: profile}
	authorize := func() error { return t.checkNoticeProof(ctx, proof) }
	if err := authorize(); err != nil {
		return "", err
	}
	if !in.AllowMarkRead {
		return "", mcptools.ArgumentError("$.allow_mark_read", "读取可能标记已读，须明确allow_mark_read=true，且主人已独立授权")
	}
	if in.Pages == 0 {
		in.Pages = 1
	}
	if in.Pages < 1 || in.Pages > stationbrowser.MaxDouyinNoticePages || len(in.Continuation) > 128 {
		return "", mcptools.ArgumentError("$.pages", "每次可读取1–5批；更多内容按返回continuation继续，不会自动截断显式批数")
	}
	source, ok := t.artifactSource(localWorkspaceGrant{FishID: t.access.fishID, OwnerID: t.access.ownerID})
	if !ok || source == nil || source.EventID == "" || source.Revision < 1 {
		return "", errLocalDouyinNoticeGrant
	}
	// Separate tool continuations from HTTP-session references and bind them to
	// the actual original request revision, including verified continuations.
	scope := localCheckpointKey("app-inbox-v1", t.access.fishID, t.access.ownerID, source.EventID, source.Revision, record)
	op := companion.LocalToolCheckpoint{Key: localCheckpointKey(localInboxToolName, scope, in.Pages, in.Continuation), Tool: localInboxToolName, Grant: scope, Target: "douyin:notifications"}
	for _, previous := range t.run.Operations {
		if previous.Key == op.Key {
			return localProcessJSON(t.checkpointView(previous, localWorkspaceGrant{}))
		}
	}
	if _, err := m.noticeCursor(scope, in.Continuation, false); err != nil {
		return "", err
	}
	return t.dispatchCheckpointed(ctx, op, localWorkspaceGrant{}, func() (string, string, error) {
		if err := authorize(); err != nil {
			return "", "", err
		}
		nextToken, err := localDouyinWebID()
		if err != nil {
			return "", "", err
		}
		readCtx, cancel := context.WithTimeout(ctx, 50*time.Second)
		defer cancel()
		stop := context.AfterFunc(m.ctx, cancel)
		defer stop()
		check := func() error {
			if err := readCtx.Err(); err != nil {
				return err
			}
			return authorize()
		}
		after, err := m.noticeCursor(scope, in.Continuation, true)
		if err != nil {
			return "", "", err
		}
		page, err := m.readNotice(readCtx, profile, stationbrowser.DouyinNoticeRequest{Account: record.NoticeAccount, AllowMarkRead: true, Pages: in.Pages, After: after}, check)
		if err != nil {
			if errors.Is(err, stationbrowser.ErrDouyinNoticeAccountChanged) && check() == nil {
				err = errors.Join(err, m.revokeChangedNoticeAccount(readCtx, record))
			}
			return "", "", err
		}
		if err := check(); err != nil {
			return "", "", err
		}
		if page.Account != record.NoticeAccount {
			err = m.revokeChangedNoticeAccount(readCtx, record)
			return "", "", errors.Join(stationbrowser.ErrDouyinNoticeAccountChanged, err)
		}
		if err := check(); err != nil {
			return "", "", err
		}
		roundsRecorded := m.captureExecutionRounds(readCtx, t.access.fishID, t.access.ownerID, record, page) == nil
		memoryRecorded := m.captureNoticeMemory(readCtx, record, page) == nil
		if err := check(); err != nil {
			return "", "", err
		}
		next := m.finishNoticeCursor(scope, after, page, nextToken)
		result := localDouyinNoticeResult(page, next, in.Pages)
		result["app_id"], result["mark_read_possible"], result["text_scope"] = "douyin", true, "rendered_list_may_include_previous_pages"
		result["rounds_recorded"] = roundsRecorded
		if !roundsRecorded {
			result["rounds_warning"] = "通知已读取，可能已标记已读；执行回合记录未确认保存，请保存本次读取结果，不会自动重读"
		}
		if _, granted := douyinMemorySource(record, t.access.fishID, t.access.ownerID, douyinMemoryNotices); granted {
			result["memory_recorded"] = memoryRecorded
			if !memoryRecorded {
				result["memory_warning"] = "通知已读取，可能已标记已读；已授权的本机通知记忆未确认保存，请保存本次读取结果，不会自动重读"
			}
		}
		t.noticeReads = append(t.noticeReads, proof)
		value, err := localProcessJSON(result)
		if err != nil {
			return "", "", err
		}
		metadata, err := localProcessJSON(localInboxCheckpoint{Version: 1, State: "read", Notices: []localPublicationNotice{{AppID: "douyin", GrantVersion: localNoticeGrantVersion(t.access.fishID, t.access.ownerID, proof.record)}}})
		return value, metadata, err
	})
}
