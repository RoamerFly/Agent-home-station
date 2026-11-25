package station

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/mediaspec"
	"wechat-bot/internal/strictjson"
)

func (t *stationAttachmentTools) publicAudioPrepareTool(ctx context.Context) (mcptools.Tool, bool) {
	if t.projects == nil || t.projectOwnerAllowed == nil || !t.projectOwnerAllowed(t.trace) || !t.replyAllowed(t.trace) ||
		t.externalMu == nil || t.store == nil || t.dir == "" ||
		(t.trace.Source == nil || t.trace.Source.MessageType != 49) && (t.trace.QuotedSource == nil || t.trace.QuotedSource.MessageType != 49) {
		return mcptools.Tool{}, false
	}
	owner, err := t.projects.home.identity.Read()
	if err != nil || owner.LocalOwner == nil {
		return mcptools.Tool{}, false
	}
	if _, err := t.projects.publicAudioPrepareBinding(ctx, owner.FishID); err != nil {
		return mcptools.Tool{}, false
	}
	return mcptools.NewTool(localAudioToolName, "用工作站音频工坊截取当前或引用的整数 PCM WAV 文件，可选保留声道或转为单声道。原件由微信应用核对，输出作为 WAV 文件准备。", `{"type":"object","properties":{"message_id":{"type":"string"},"filename":{"type":"string","maxLength":120},"start_seconds":{"type":"number","minimum":0},"duration_seconds":{"type":"number","exclusiveMinimum":0},"channels":{"type":"string","enum":["preserve","mono"]}},"required":["message_id"],"additionalProperties":false}`), true
}

func (t *stationAttachmentTools) callPublicAudioPrepare(ctx context.Context, raw json.RawMessage) (string, error) {
	if _, ok := t.publicAudioPrepareTool(ctx); !ok {
		return "", wechatapp.ErrAttachmentDenied
	}
	t.externalMu.Lock()
	defer t.externalMu.Unlock()
	var input struct {
		Message  string   `json:"message_id"`
		Filename string   `json:"filename"`
		Start    float64  `json:"start_seconds"`
		Duration *float64 `json:"duration_seconds"`
		Channels string   `json:"channels"`
	}
	if len(raw) > 16<<10 || strictjson.DecodeKnownFields(raw, &input) != nil || input.Message == "" || len(input.Message) > 8192 ||
		input.Start < 0 || input.Duration != nil && *input.Duration <= 0 {
		return "", mcptools.ArgumentError("$", "请提供已授权 WAV 消息及有效的截取范围")
	}
	if input.Channels == "" {
		input.Channels = "preserve"
	}
	if input.Channels != "preserve" && input.Channels != "mono" {
		return "", mcptools.ArgumentError("$.channels", "声道只支持 preserve 或 mono")
	}
	if input.Filename != "" && (!filepath.IsLocal(input.Filename) || !mediaspec.SafeFilename(input.Filename) ||
		len([]rune(input.Filename)) > 120 || !strings.EqualFold(filepath.Ext(input.Filename), ".wav")) {
		return "", mcptools.ArgumentError("$.filename", "请使用以 .wav 结尾的单个文件名")
	}
	access, err := t.attachmentAccess(t.trace, input.Message, t.allowed)
	if err != nil || access.ref == nil || access.ref.MessageType != 49 {
		return "", wechatapp.ErrAttachmentDenied
	}
	source, err := access.source(ctx, access.thread, access.message)
	if err != nil || source.Kind != "file" || !strings.EqualFold(filepath.Ext(source.Filename), ".wav") {
		return "", mcptools.ArgumentError("$.message_id", "需要当前或引用的 WAV 文件原消息")
	}
	epoch, err := t.store.RecallEpoch(ctx)
	if err != nil {
		return "", err
	}
	origin := func(ctx context.Context) error { return access.confirmObservationSource(ctx, source, epoch) }
	if err := origin(ctx); err != nil {
		return "", err
	}
	current, ok := t.store.SourceForInput(t.trace)
	if !ok {
		return "", errors.New("微信原消息来源已变化")
	}
	owner, err := t.projects.home.identity.Read()
	if err != nil || owner.LocalOwner == nil {
		return "", errors.New("工作站身份不可用")
	}
	binding, err := t.projects.publicAudioPrepareBinding(ctx, owner.FishID)
	if err != nil {
		return "", err
	}
	request := publicAudioPrepareRequest{Filename: input.Filename, Start: input.Start, Duration: input.Duration, Channels: input.Channels}
	j := stationWechatExternalHTTPJob{Account: t.trace.Account, Thread: t.trace.Thread, Sender: t.trace.Sender, Trace: t.trace.TraceID,
		Fish: owner.FishID, Source: *current, Grant: t.grants[t.trace.Thread], MediaGrant: t.replies[t.trace.Thread],
		Registration: binding, Tool: localAudioToolName, Fingerprint: localCheckpointKey(source),
		RequestHash: localCheckpointKey(localAudioToolName, source.AssetID(), request), State: "submitting"}
	j.ID = j.expectedID()
	previous, err := t.readExternalHTTPJob(j.ID)
	if err == nil {
		if !t.externalHTTPAllowed(t.trace, previous) || !t.replyAllowed(t.trace) || previous.Fish != j.Fish || previous.Fingerprint != j.Fingerprint ||
			!previous.MediaGrant.Equal(j.MediaGrant) {
			return "", wechatapp.ErrAttachmentDenied
		}
		if previous.Registration != binding {
			return localProcessJSON(map[string]any{"task_id": previous.ID, "state": "needs_review", "reused": true,
				"instruction": "音频工坊安装或配置已变化，请核对原任务。"})
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
				if !t.externalHTTPAllowed(t.trace, j) || !t.replyAllowed(t.trace) || !access.Allowed(bound, source.Thread) {
					cancel()
					return
				}
			}
		}
	}()
	workDir, err := os.MkdirTemp(t.dir, "audio-prepare-input-")
	var result publicAudioPrepareResult
	if err == nil {
		defer os.RemoveAll(workDir)
		path := filepath.Join(workDir, "original.wav")
		file, openErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if openErr != nil {
			err = openErr
		} else {
			rendered, transferErr := wechatapp.StreamAttachment(bound, t.native, source, false, stationAttachmentLimit, file,
				func() bool { return access.Allowed(bound, source.Thread) })
			closeErr := file.Close()
			if transferErr != nil || closeErr != nil {
				err = errors.New("微信 WAV 原件未能取得")
			} else if origin(bound) != nil {
				err = wechatapp.ErrAttachmentDenied
			} else {
				j.InputSHA256 = rendered.SHA256
				if err = t.writeExternalHTTPJob(j, nil); err == nil {
					reader, readErr := os.Open(path)
					if readErr != nil {
						err = readErr
					} else {
						result, err = t.projects.publicAudioPrepare(bound, owner.FishID, binding, j.ID, rendered.SHA256, rendered.Bytes, reader, request)
						_ = reader.Close()
					}
				}
			}
		}
	}
	if err == nil {
		err = origin(bound)
	}
	cancel()
	<-done
	if err != nil {
		j.State = "needs_review"
	} else {
		j.State, j.Bytes, j.SHA256, j.MIME, j.DeclaredKind, j.Filename = "completed", int64(len(result.WAV)), externalDigest(result.WAV), "audio/wav", "file", result.Name
		encoded, encodeErr := json.Marshal(map[string]any{"coverage": result.Coverage, "verification": result.Verification,
			"input_sha256": j.InputSHA256, "output_sha256": j.SHA256})
		if encodeErr != nil {
			return "", encodeErr
		}
		j.Text = string(encoded)
	}
	var wav []byte
	if err == nil {
		wav = result.WAV
	}
	if writeErr := t.writeExternalHTTPJob(j, wav); writeErr != nil {
		return "", writeErr
	}
	if !t.externalHTTPAllowed(t.trace, j) || !t.replyAllowed(t.trace) {
		return "", wechatapp.ErrAttachmentDenied
	}
	return t.externalHTTPView(j, 0, 1200, false)
}
