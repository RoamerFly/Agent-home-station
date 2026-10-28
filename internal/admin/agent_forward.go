package admin

import (
	"context"
	"encoding/json"
	"errors"
)

type forwardFileInput struct {
	Source  string `json:"source_thread_id"`
	Message string `json:"message_id"`
	Target  string `json:"target_thread_id"`
	Key     string `json:"request_key"`
}

func forwardReceiptDescription(op agentOperation, current string) (string, string) {
	var in forwardFileInput
	_ = json.Unmarshal(op.Arguments, &in)
	if in.Target == "" {
		in.Target = current
	}
	var result struct {
		Filename string `json:"filename"`
	}
	_ = json.Unmarshal([]byte(op.Result), &result)
	if result.Filename == "" {
		result.Filename = "转发附件"
	}
	return in.Target, "[文件] " + result.Filename + "（转自 " + in.Source + "）"
}

func (p *permittedTools) forwardFile(ctx context.Context, args json.RawMessage) (string, error) {
	var in forwardFileInput
	if json.Unmarshal(args, &in) != nil || in.Key == "" || in.Message == "" {
		return "", errors.New("转发需要真实消息编号和稳定请求编号")
	}
	if in.Target == "" {
		in.Target = p.trace.Thread
	}
	if !p.crossAllowed(in.Source, in.Target) {
		return "", errors.New("cross_forward_denied")
	}
	// Preserve pre-upgrade operation identities; never replay a historical send.
	id := p.operationID("wechat_forward_file", args)
	p.manager.agent.mu.Lock()
	old, ok := p.manager.agent.data.Operations[id]
	p.manager.agent.mu.Unlock()
	if ok {
		return old.Result, nil
	}
	next, _ := json.Marshal(crossMediaInput{Source: in.Source, Target: in.Target, Message: in.Message, Kind: "file", Key: in.Key})
	return p.sendMedia(ctx, next)
}
