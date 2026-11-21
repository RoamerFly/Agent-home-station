package station

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/strictjson"
)

func (t *stationAttachmentTools) publicAudioCppTools(ctx context.Context) []mcptools.Tool {
	if t.projects == nil || t.projectOwnerAllowed == nil || !t.projectOwnerAllowed(t.trace) || !t.allowed() {
		return nil
	}
	owner, err := t.projects.home.identity.Read()
	if err != nil || owner.LocalOwner == nil {
		return nil
	}
	if _, err := t.projects.publicAudioCppInfo(ctx, owner.FishID); err != nil {
		return nil
	}
	tools := []mcptools.Tool{mcptools.NewTool(localAudioCppModelsTool, "列出工作站已登记的 audio.cpp 语音模型及状态。", `{"type":"object","properties":{},"additionalProperties":false}`)}
	if t.replyAllowed(t.trace) && t.externalMu != nil && t.store != nil {
		tools = append(tools, mcptools.NewTool(localAudioCppSynthesizeTool, "用工作站 audio.cpp 的 Kokoro 模型生成 WAV 文件。应用按原微信消息保存请求及结果；同一请求不重复合成。", `{"type":"object","properties":{"text":{"type":"string","maxLength":12000},"text_language":{"type":"string"}},"required":["text"],"additionalProperties":false}`))
	}
	if t.externalMu != nil && t.store != nil && (stationVoiceRef(t.trace.Source) || stationVoiceRef(t.trace.QuotedSource)) {
		tools = append(tools, mcptools.NewTool(localAudioCppTranscribeTool, "用工作站 audio.cpp 转写当前或引用的微信语音、MP3/WAV/FLAC 音频文件。按起点和时长读取，返回 next_start_seconds 供续读。", `{"type":"object","properties":{"message_id":{"type":"string"},"start_seconds":{"type":"number","minimum":0},"duration_seconds":{"type":"number","exclusiveMinimum":0},"timeout_seconds":{"type":"integer","minimum":0},"text_offset":{"type":"integer","minimum":0},"text_limit":{"type":"integer","minimum":1}},"required":["message_id"],"additionalProperties":false}`))
	}
	return tools
}

func (t *stationAttachmentTools) callPublicAudioCpp(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	available := false
	for _, tool := range t.publicAudioCppTools(ctx) {
		available = available || tool.Name == name
	}
	if !available {
		return "", wechatapp.ErrAttachmentDenied
	}
	owner, err := t.projects.home.identity.Read()
	if err != nil || owner.LocalOwner == nil {
		return "", errors.New("工作站身份不可用")
	}
	info, err := t.projects.publicAudioCppInfo(ctx, owner.FishID)
	if err != nil {
		return "", err
	}
	if name == localAudioCppModelsTool {
		if strictLocalMemoryDecode(raw, &struct{}{}) != nil {
			return "", mcptools.ArgumentError("$", "模型列表无需参数")
		}
		return localProcessJSON(map[string]any{"models": info.Models, "backend": info.Backend, "version": info.Version})
	}
	if name == localAudioCppTranscribeTool {
		return t.transcribePublicAudioCpp(ctx, raw, owner.FishID, info.Binding)
	}
	if t.externalMu == nil {
		return "", wechatapp.ErrAttachmentDenied
	}
	t.externalMu.Lock()
	defer t.externalMu.Unlock()
	var input struct {
		Text     string `json:"text"`
		Language string `json:"text_language"`
	}
	if len(raw) > 80<<10 || strictjson.DecodeKnownFields(raw, &input) != nil || !utf8.ValidString(input.Text) ||
		strings.TrimSpace(input.Text) == "" || strings.ContainsRune(input.Text, 0) || len(input.Text) > localTTSTextLimit {
		return "", mcptools.ArgumentError("$.text", "请提供有效且不超过 12000 字节的正文")
	}
	source, ok := t.store.SourceForInput(t.trace)
	if !ok {
		return "", errors.New("微信原消息来源已变化")
	}
	request, _ := json.Marshal(input)
	j := stationWechatExternalHTTPJob{Account: t.trace.Account, Thread: t.trace.Thread, Sender: t.trace.Sender, Trace: t.trace.TraceID,
		Fish: owner.FishID, Source: *source, Grant: t.grants[t.trace.Thread], MediaGrant: t.replies[t.trace.Thread],
		Registration: info.Binding, Tool: name, RequestHash: localCheckpointKey(name, request), State: "submitting"}
	j.ID = j.expectedID()
	previous, err := t.readExternalHTTPJob(j.ID)
	if err == nil {
		if !t.externalHTTPAllowed(t.trace, previous) || !t.replyAllowed(t.trace) || previous.Fish != j.Fish || !previous.MediaGrant.Equal(j.MediaGrant) {
			return "", wechatapp.ErrAttachmentDenied
		}
		if previous.Registration != j.Registration {
			return localProcessJSON(map[string]any{"task_id": previous.ID, "state": "needs_review", "reused": true, "instruction": "audio.cpp 登记已变化，请核对原请求。"})
		}
		if previous.State == "submitting" {
			previous.State = "needs_review"
		}
		return t.externalHTTPView(previous, 0, 1200, true)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := t.writeExternalHTTPJob(j, nil); err != nil {
		return "", err
	}
	bound, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-bound.Done():
				return
			case <-ticker.C:
				if !t.externalHTTPAllowed(t.trace, j) || !t.replyAllowed(t.trace) {
					cancel()
					return
				}
			}
		}
	}()
	result, callErr := t.projects.publicAudioCppSynthesize(bound, owner.FishID, info.Binding, localTTSRequest{Text: input.Text, Language: input.Language})
	cancel()
	<-done
	if callErr != nil {
		j.State = "needs_review"
	} else {
		j.State, j.Bytes, j.SHA256, j.MIME, j.DeclaredKind = "completed", int64(len(result.WAV)), externalDigest(result.WAV), "audio/wav", "voice"
	}
	var wav []byte
	if callErr == nil {
		wav = result.WAV
	}
	if err := t.writeExternalHTTPJob(j, wav); err != nil {
		return "", err
	}
	if !t.externalHTTPAllowed(t.trace, j) || !t.replyAllowed(t.trace) {
		return "", wechatapp.ErrAttachmentDenied
	}
	return t.externalHTTPView(j, 0, 1200, false)
}
