package companion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// Review diagnostic capabilities and diagnostic drafts. Route model-written
// text without substituting canned replies.
type GroupReplyPrivacy interface {
	NeedsPrivateReplyReview() bool
	ReportPrivateDiagnostic(context.Context, string) error
}

// PrivateReplyRouter is a host-owned route for a trusted administrator who
// asked to read another authorized conversation. It also keeps private-origin
// cross-source answers out of ordinary accepted conversation memory. It is separate
// from diagnostic review: its final text has already passed the current
// request's normal evidence checks and is not sent through another model.
type PrivateReplyRouter interface {
	PrivateReplyTarget() string
	// Required remains true when a cross-session result was successfully read
	// but the owner's private conversation is currently unavailable. It blocks
	// a fallback publication in the originating group.
	PrivateReplyRequired() bool
	RoutePrivateReply(context.Context, []string) error
}

func (e *Engine) reviewReplyPrivacy(ctx context.Context, in Input, d Decision) (Decision, error) {
	if in.IsGroup && len(d.Messages) > 0 && diagnosticDraft(d.Messages) {
		d.Messages = nil
		if d.ImageID == "" && len(d.MediaIDs) == 0 {
			d.Action, d.Reason = "silent", "private_diagnostic_only"
		}
	}
	return d, nil
}

// routeCrossConversationReply keeps the source request in its original scope
// and lets the host submit the already-validated text to the authenticated
// owner's private conversation. Generated media is intentionally not rerouted:
// it needs its own explicit source, target and delivery receipt contract.
func (e *Engine) routeCrossConversationReply(ctx context.Context, in Input, d Decision) (Decision, error) {
	router, ok := e.Tools.(PrivateReplyRouter)
	if !ok || d.Action != "reply" || !router.PrivateReplyRequired() {
		return d, nil
	}
	hasText := len(d.Messages) > 0
	hasMedia := d.ImageID != "" || len(d.MediaIDs) > 0
	if !hasText && !hasMedia {
		return d, nil
	}
	if router.PrivateReplyTarget() == "" {
		d.Messages = nil
		d.ReplyContexts = nil
		d.ImageID = ""
		d.MediaIDs = nil
		d.Action, d.Reason = "silent", "cross_conversation_private_route_unavailable"
		if notice, ok := e.Tools.(interface{ PrivateReplyUnavailableNotice() string }); ok && in.IsGroup {
			if text := notice.PrivateReplyUnavailableNotice(); text != "" {
				d.Action = "reply"
				d.Messages = []string{text}
			}
		}
		return d, nil
	}
	if !hasText {
		// Generated attachments carry cross-session content too. They cannot be
		// implicitly re-targeted by a text-only private route, and must never
		// fall back to the source group. An explicit, separately authorized media
		// send tool remains the only delivery path.
		d.ImageID = ""
		d.MediaIDs = nil
		d.Action, d.Reason = "silent", "cross_conversation_media_requires_explicit_delivery"
		return d, nil
	}
	if err := router.RoutePrivateReply(ctx, d.Messages); err != nil {
		return Decision{}, err
	}
	d.Messages = nil
	d.ReplyContexts = nil
	d.ImageID = ""
	d.MediaIDs = nil
	d.Action = "silent"
	d.Reason = "cross_conversation_result_private_delivery_handled"
	return d, nil
}

// A model can draft a diagnosis without actually using a diagnostic tool.
// These signals only request model editing; they never generate a reply.
func diagnosticDraft(messages []string) bool {
	text := strings.ToLower(strings.Join(messages, "\n"))
	for _, signal := range []string{"gpu", "cuda", "torch", "gpt", "sovits", "rvc", "yue", "权重", "沙箱", "环境", "模型", "声线库", "音色库", "服务器", "私聊", "报错", "修复", "路径"} {
		if strings.Contains(text, signal) {
			return true
		}
	}
	return false
}

// The same audience review is used before progress and explicit send tools,
// so neither route can publish a draft that the final-response review removes.
func ReviewGroupMessages(ctx context.Context, model LanguageModel, access GroupReplyPrivacy, in Input, messages []string) ([]string, error) {
	return reviewGroupMessages(ctx, model, access, in, messages, false)
}

func reviewGroupMessages(ctx context.Context, model LanguageModel, access GroupReplyPrivacy, in Input, messages []string, correction bool) ([]string, error) {
	payload, _ := json.Marshal(map[string]any{"request_for_context_only": in.Text, "draft_messages": messages, "group": in.Thread, "previous_edit_still_exposed_internal_details": correction})
	answer, err := model.Chat(ctx, "编辑群聊草稿，只输出JSON {\"messages\":[],\"private_report\":\"\"}。messages保留自然回应和准确进度；模型、工具、路径及内部故障写入private_report。previous_edit_still_exposed_internal_details=true时继续精简技术说明。", []map[string]string{{"role": "user", "content": string(payload)}})
	if err != nil {
		return nil, err
	}
	var out struct {
		Messages []string `json:"messages"`
		Private  string   `json:"private_report"`
	}
	if len(answer) > 64<<10 || json.Unmarshal([]byte(answer), &out) != nil || out.Messages == nil {
		return nil, errors.New("group_reply_privacy_review_invalid")
	}
	if out.Private != "" {
		if err = access.ReportPrivateDiagnostic(ctx, out.Private); err != nil {
			return nil, err
		}
	}
	for _, message := range out.Messages {
		if strings.TrimSpace(message) == "" {
			return nil, errors.New("group_reply_privacy_review_invalid")
		}
	}
	if diagnosticDraft(out.Messages) {
		if !correction {
			return reviewGroupMessages(ctx, model, access, in, out.Messages, true)
		}
		return nil, errors.New("group_reply_still_contains_private_diagnostics")
	}
	return out.Messages, nil
}

func sensitiveReplyTool(tool string) bool {
	return strings.HasPrefix(tool, "server_") || strings.HasPrefix(tool, "workspace_") || strings.HasPrefix(tool, "voice_library_") || strings.HasPrefix(tool, "music_") || strings.HasPrefix(tool, "plugin_") || tool == "agent_global_search" || tool == "wechat_read" || tool == "wechat_read_legacy" || tool == "wechat_recall" || tool == "agent_owner_report"
}

func (e *Engine) reviewProgressPrivacy(ctx context.Context, turns []map[string]string, tool, text string) (string, error) {
	if text == "" {
		return "", nil
	}
	for i := len(turns) - 1; i >= 0; i-- {
		var target struct {
			Kind  string
			Group bool `json:"is_group"`
		}
		if json.Unmarshal([]byte(turns[i]["content"]), &target) != nil || target.Kind != "current_message" {
			continue
		}
		if router, routed := e.Tools.(PrivateReplyRouter); routed && router.PrivateReplyRequired() {
			// The eventual result is owner-private; interim tool/progress text
			// must not disclose another conversation back into the source group.
			return "", nil
		}
		access, ok := e.Tools.(GroupReplyPrivacy)
		if target.Group && (diagnosticDraft([]string{text}) || ok && access.NeedsPrivateReplyReview() || strings.HasPrefix(tool, "server_")) {
			return "", nil
		}
		break
	}
	return text, nil
}
