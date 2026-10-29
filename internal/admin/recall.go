package admin

import (
	"context"
	"encoding/json"
	"wechat-bot/internal/linuxnative"
)

func (p *permittedTools) recallMessage(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Thread  string `json:"thread_id"`
		Message string `json:"message_id"`
		Key     string `json:"request_key"`
	}
	if json.Unmarshal(raw, &in) != nil || !p.privileged() || in.Message == "" || !validID(in.Key) {
		return "", toolInputError("recall_requires_verified_owner_and_message")
	}
	if in.Thread == "" {
		in.Thread = p.trace.Thread
	}
	if !p.conversationReadAllowed(in.Thread) {
		return "", toolInputError("recall_target_not_authorized")
	}
	m, err := p.manager.native.Message(ctx, in.Message)
	if err != nil {
		return "", err
	}
	if m.AccountID != p.trace.Account || m.ThreadID != in.Thread || m.Direction != "outgoing" || m.ServerID == "" {
		return "", toolInputError("recall_requires_exact_own_outgoing_message")
	}
	n, ok := p.manager.native.(nativeActions)
	if !ok {
		return "", toolInputError("native_recall_unavailable")
	}
	return p.privilegedCall(ctx, "wechat_recall", raw, func() (any, error) {
		return n.ExecuteAction(ctx, linuxnative.AdminAction{Kind: "recall", ThreadID: in.Thread, MessageID: in.Message, ServerID: m.ServerID}, agentID(p.trace.Account, in.Thread, in.Key, "recall"))
	})
}
