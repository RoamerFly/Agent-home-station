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
	settings := p.manager.agent.config()
	action := linuxnative.AdminAction{Kind: "recall", ThreadID: in.Thread, MessageID: in.Message}
	if !p.wechatActionAllowed(action, true, settings) {
		return "", toolInputError("recall_target_not_authorized")
	}
	m, err := p.manager.native.Message(ctx, in.Message)
	if err != nil {
		return "", err
	}
	if !matchesOwnWechatMessage(m, p.trace.Account, in.Thread, in.Message) {
		return "", toolInputError("recall_requires_exact_own_outgoing_message")
	}
	action.ServerID = m.ServerID
	return p.submitWechatAction(ctx, "wechat_recall", raw, action, agentID(p.trace.Account, in.Thread, in.Key, "recall"), settings, true)
}
