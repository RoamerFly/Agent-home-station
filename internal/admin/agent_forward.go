package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"strings"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/linuxnative"
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
	if json.Unmarshal(args, &in) != nil || in.Key == "" || len(in.Key) > 128 || in.Message == "" || len(in.Message) > 8192 {
		return "", errors.New("转发需要真实消息编号和稳定请求编号")
	}
	if in.Target == "" {
		in.Target = p.trace.Thread
	}
	m := p.manager
	cfg := m.Config()
	if !p.privileged() || !m.agent.config().CrossConversation || !permittedRecipient(cfg.Config, in.Source) || !permittedRecipient(cfg.Config, in.Target) || !cfg.Companion.Permissions.SendFile {
		return "", errors.New("仅可信主人可在已允许会话间转发文件，且需允许文件发送")
	}
	native, ok := m.native.(nativeTools)
	reader, readable := m.native.(incomingReader)
	if !ok || !readable {
		return "", errors.New("后台不支持文件读取和发送")
	}
	message, err := m.native.Message(ctx, in.Message)
	if err != nil || message.ID != in.Message || message.AccountID != p.trace.Account || message.ThreadID != in.Source {
		return "", errors.New("文件消息不属于指定来源会话或当前微信账号")
	}
	filename := message.FileAttachmentName()
	if !safeMediaFilename(filename) || strings.ContainsAny(filename, ":") || filename == "." || filename == ".." {
		return "", errors.New("该消息不是可读取的文件附件，或原文件名无效")
	}
	capabilities, err := native.Capabilities(ctx)
	if err != nil || !contains(capabilities.MediaKinds, "file") {
		return "", errors.New("原生后台文件发送能力不可用")
	}
	limit := toolsLimit("file")
	if n := capabilities.MediaSendMaxBytes["file"]; n > 0 && n < limit {
		limit = n
	}
	return p.privilegedCall(ctx, "wechat_forward_file", args, func() (any, error) {
		bounded, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		data, mime, err := reader.IncomingMedia(bounded, in.Message, limit)
		if err != nil {
			return nil, errors.New("来源文件读取失败，可能已过期、未同步或下载失败；未发送")
		}
		if len(data) == 0 || int64(len(data)) > limit {
			return nil, errors.New("来源文件为空或超过发送大小限制；未发送")
		}
		if mime == "" {
			mime = "application/octet-stream"
		}
		source, err := native.Upload(bounded, filename, mime, bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, errors.New("来源文件已读取，但重新暂存失败；未发送")
		}
		if !validID(source.ID) {
			return nil, errors.New("文件暂存未返回有效编号；未发送")
		}
		if err = m.tools.upload(stagedUpload{UploadSource: source, Account: p.trace.Account, Kind: "file"}); err != nil {
			return nil, errors.New("文件暂存记录未保存；未发送")
		}
		// Recheck revocation after downloading. Reading the other conversation's
		// messages never changes this call's authenticated sender or target scope.
		latest := m.Config()
		if !p.privileged() || !m.agent.config().CrossConversation || !permittedRecipient(latest.Config, in.Source) || !permittedRecipient(latest.Config, in.Target) || !latest.Companion.Permissions.SendFile {
			return nil, errors.New("转发授权已变更；未发送")
		}
		if strings.HasSuffix(in.Target, "@chatroom") && latest.Companion.Quiet(time.Now()) {
			return nil, errors.New("目标群处于安静时段；未发送")
		}
		if latest.Behavior.RandomDelay {
			delay := latest.Behavior.DelayMinSeconds
			if n := latest.Behavior.DelayMaxSeconds - delay; n > 0 {
				delay += rand.IntN(n + 1)
			}
			select {
			case <-bounded.Done():
				return nil, bounded.Err()
			case <-time.After(time.Duration(delay) * time.Second):
			}
		}
		if err = m.memory.ClaimReply(latest.Companion, time.Now()); err != nil {
			return nil, err
		}
		status, err := native.Submit(bounded, linuxnative.ToolSend{ThreadID: in.Target, Kind: "file", SourceID: source.ID, IdempotencyKey: p.operationID("wechat_forward_file", args)})
		if err != nil {
			return nil, errors.New("文件发送提交回执未知，请核查原操作；不要换请求编号重复发送")
		}
		body, _ := json.Marshal(status)
		var result map[string]any
		_ = json.Unmarshal(body, &result)
		result["filename"] = filename
		result["source_thread_id"] = in.Source
		result["source_message_id"] = in.Message
		text := "[文件] " + filename + "（转自 " + in.Source + "）"
		if status.State == "accepted" && status.ServerID != "" {
			p.rememberAgentSend(in.Target, text, p.operationID("wechat_forward_file", args))
		}
		m.record(bot.Record{Event: "agent_cross_file_send", Category: "sent", ThreadID: in.Target, Sender: p.trace.Sender, Content: text, Status: status.State, SendID: status.ID, MessageID: p.trace.TraceID, Origin: "agent"})
		return result, nil
	})
}
