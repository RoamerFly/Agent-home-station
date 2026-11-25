package station

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/mediadecode"
	"wechat-bot/internal/strictjson"
)

func (t *stationAttachmentTools) audioCppTranscriptView(j stationWechatExternalHTTPJob, offset, limit int, reused bool) (string, error) {
	view := map[string]any{"task_id": j.ID, "state": j.State, "reused": reused, "model": "sensevoice"}
	if j.State != "completed" {
		view["instruction"] = "转写结果尚未确认；请核对原音频及工作站任务，不要将旧结果当成已完成。"
		return localProcessJSON(view)
	}
	var observation map[string]any
	if json.Unmarshal([]byte(j.Text), &observation) != nil || observation == nil {
		return "", errors.New("audio.cpp 转写记录无效")
	}
	if text, ok := observation["text"].(string); ok {
		delete(observation, "text")
		fields, _ := json.Marshal(map[string]string{"text": text})
		if limit == 0 {
			limit = 1200
		}
		page, err := localExternalTextPage(fields, offset, limit)
		if err != nil {
			return "", err
		}
		view["output"] = page
	} else if offset != 0 {
		return "", mcptools.ArgumentError("$.text_offset", "该音频页没有可续读正文")
	}
	view["observation"], view["untrusted_external_content"] = observation, true
	return localProcessJSON(view)
}

func (t *stationAttachmentTools) transcribePublicAudioCpp(ctx context.Context, raw json.RawMessage, fish, binding string) (string, error) {
	if t.externalMu == nil || t.store == nil || !t.allowed() {
		return "", wechatapp.ErrAttachmentDenied
	}
	t.externalMu.Lock()
	defer t.externalMu.Unlock()
	input := struct {
		Message  string  `json:"message_id"`
		Start    float64 `json:"start_seconds"`
		Duration float64 `json:"duration_seconds"`
		Timeout  int     `json:"timeout_seconds"`
		Offset   int     `json:"text_offset"`
		Limit    int     `json:"text_limit"`
	}{Duration: 90, Timeout: 180}
	if len(raw) > 16<<10 || strictjson.DecodeKnownFields(raw, &input) != nil || input.Message == "" || len(input.Message) > 8192 ||
		input.Start < 0 || input.Duration <= 0 || input.Timeout < 0 || int64(input.Timeout) > int64((1<<63-1)/int64(time.Second)) || input.Offset < 0 || input.Limit < 0 {
		return "", mcptools.ArgumentError("$", "请提供当前或引用的音频消息及有效的起点、时长")
	}
	access, err := t.attachmentAccess(t.trace, input.Message, t.allowed)
	if err != nil || !stationVoiceRef(access.ref) {
		return "", wechatapp.ErrAttachmentDenied
	}
	source, err := access.source(ctx, access.thread, access.message)
	if err != nil || source.Kind != "voice" && source.Kind != "file" {
		return "", wechatapp.ErrAttachmentDenied
	}
	kind, extension, probe := stationVoiceAudio, "voice", mediadecode.ProbeVoiceDuration
	if source.Kind == "file" {
		extension = mediadecode.AudioFileFormat(source.Filename)
		if extension == "" {
			return "", mcptools.ArgumentError("$.message_id", "请提供微信语音或 MP3、WAV、FLAC 音频文件")
		}
		kind, probe = stationFileAudio, mediadecode.ProbeAudioFileDuration
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
	j := stationWechatExternalHTTPJob{Account: t.trace.Account, Thread: t.trace.Thread, Sender: t.trace.Sender, Trace: t.trace.TraceID,
		Fish: fish, Source: *current, Grant: t.grants[t.trace.Thread], Registration: binding, Tool: localAudioCppTranscribeTool,
		Fingerprint: localCheckpointKey(source), RequestHash: localCheckpointKey(localAudioCppTranscribeTool, source.AssetID(), input.Start, input.Duration), State: "submitting"}
	j.ID = j.expectedID()
	previous, err := t.readExternalHTTPJob(j.ID)
	if err == nil {
		if !t.externalHTTPAllowed(t.trace, previous) || previous.Fish != fish || previous.Fingerprint != j.Fingerprint {
			return "", wechatapp.ErrAttachmentDenied
		}
		if previous.Registration != binding {
			return localProcessJSON(map[string]any{"task_id": previous.ID, "state": "needs_review", "reused": true,
				"instruction": "audio.cpp 登记已变化，请核对原转写。"})
		}
		if previous.State == "submitting" {
			previous.State = "needs_review"
		}
		return t.audioCppTranscriptView(previous, input.Offset, input.Limit, true)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if input.Offset != 0 {
		return "", mcptools.ArgumentError("$.text_offset", "首次转写请从正文起点读取")
	}
	if err := t.writeExternalHTTPJob(j, nil); err != nil {
		return "", err
	}
	bound := ctx
	var cancel context.CancelFunc
	if input.Timeout > 0 {
		bound, cancel = context.WithTimeout(ctx, time.Duration(input.Timeout)*time.Second)
	} else {
		bound, cancel = context.WithCancel(ctx)
	}
	defer cancel()
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
				if !t.externalHTTPAllowed(t.trace, j) || !access.Allowed(bound, source.Thread) {
					cancel()
					return
				}
			}
		}
	}()
	observation := map[string]any{"message_id": source.Message, "sender_id": source.Sender, "occurred_at": source.Occurred,
		"source_variant": "verified_attachment", "asr_backend": "audio.cpp"}
	workDir, err := os.MkdirTemp(t.dir, "audio-cpp-input-")
	if err == nil {
		defer os.RemoveAll(workDir)
		path := filepath.Join(workDir, "original."+extension)
		file, openErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if openErr != nil {
			err = openErr
		} else {
			rendered, transferErr := wechatapp.StreamAttachment(bound, t.native, source, kind == stationVoiceAudio, stationAttachmentLimit, file,
				func() bool { return access.Allowed(bound, source.Thread) })
			closeErr := file.Close()
			if transferErr != nil || closeErr != nil {
				err = errors.New("微信音频原件未能取得")
			} else {
				observation["sha256"] = rendered.SHA256
				var duration float64
				duration, err = probe(bound, path)
				if err == nil && origin(bound) == nil {
					err = stationObserveAudio(bound, path, workDir, input.Start, input.Duration, duration, kind,
						func(ctx context.Context, wav []byte) (string, error) {
							return t.projects.publicAudioCppTranscribe(ctx, fish, binding, wav)
						}, observation)
				} else if err == nil {
					err = wechatapp.ErrAttachmentDenied
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
		j.State = "completed"
		encoded, encodeErr := json.Marshal(observation)
		if encodeErr != nil {
			return "", encodeErr
		}
		j.Text = string(encoded)
	}
	if writeErr := t.writeExternalHTTPJob(j, nil); writeErr != nil {
		return "", writeErr
	}
	if !t.externalHTTPAllowed(t.trace, j) {
		return "", wechatapp.ErrAttachmentDenied
	}
	return t.audioCppTranscriptView(j, 0, input.Limit, false)
}
