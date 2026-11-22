package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/workspacefs"
)

// The application owns this receipt and its delivery. The workstation speech
// operation only receives localTTSRequest and returns localTTSResult.
type stationWechatSpeechProduct struct {
	ID, Account, Thread, Sender, Trace, VoiceID, VoiceVersion string
	RequestHash, WAVHash                                      string
	Bytes                                                     int64
	Source                                                    companion.SourceRef
	MediaGrant                                                time.Time
}

func (p stationWechatSpeechProduct) id() string {
	return "speech-" + localCheckpointKey(p.Account, p.Thread, p.Sender, p.Trace, p.Source,
		p.MediaGrant, p.RequestHash)
}

func (p stationWechatSpeechProduct) media() companion.GeneratedMedia {
	return companion.GeneratedMedia{ID: "reply-" + p.ID, AssetID: p.ID, Kind: "file",
		Title: "大肥鱼语音.wav", CachedMIME: "audio/wav"}
}

func (t *stationAttachmentTools) publicSpeechTool(ctx context.Context) (mcptools.Tool, bool) {
	if t.projects == nil || t.projectOwnerAllowed == nil || !t.projectOwnerAllowed(t.trace) || !t.replyAllowed(t.trace) {
		return mcptools.Tool{}, false
	}
	record, err := t.projects.home.identity.Read()
	if err != nil || record.LocalOwner == nil || t.projects.tts == nil {
		return mcptools.Tool{}, false
	}
	host, err := t.projects.forFish(record.FishID)
	if err != nil {
		return mcptools.Tool{}, false
	}
	if _, err = installedLocalProject(ctx, host, localProjectOperation{pkg: localTTSPackage, capability: localTTSCapability}); err != nil {
		return mcptools.Tool{}, false
	}
	info, err := t.projects.tts.Info(ctx, record.FishID)
	if err != nil || info.Binding == "" || len(info.Voices) == 0 {
		return mcptools.Tool{}, false
	}
	caps, err := host.store.Capabilities(ctx, localTTSPackage)
	if err != nil {
		return mcptools.Tool{}, false
	}
	for _, cap := range caps {
		if cap.ID == localTTSCapability && cap.Discoverable {
			tool := mcptools.Tool{Name: localTTSStartTool, Description: cap.Description, InputSchema: cap.InputSchema}
			if info.DefaultVoiceID != "" {
				tool = localTTSWithDefault(tool)
			}
			return tool, true
		}
	}
	return mcptools.Tool{}, false
}

func (t *stationAttachmentTools) publicSpeechVoicesTool(ctx context.Context) (mcptools.Tool, bool) {
	if _, ok := t.publicSpeechTool(ctx); !ok {
		return mcptools.Tool{}, false
	}
	return mcptools.Tool{Name: localTTSVoicesTool,
		Description: "列出工作站已登记声线、当前默认声线及模型版本；训练时间以模型记录为准。",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}, true
}

func (t *stationAttachmentTools) callPublicSpeechVoices(ctx context.Context, raw json.RawMessage) (string, error) {
	if strictLocalMemoryDecode(raw, &struct{}{}) != nil {
		return "", mcptools.ArgumentError("$", "声线列表不需要参数")
	}
	if _, ok := t.publicSpeechVoicesTool(ctx); !ok {
		return "", wechatapp.ErrAttachmentDenied
	}
	record, err := t.projects.home.identity.Read()
	if err != nil || record.LocalOwner == nil {
		return "", wechatapp.ErrAttachmentDenied
	}
	info, err := t.projects.tts.Info(ctx, record.FishID)
	if err != nil {
		return "", err
	}
	return localProcessJSON(map[string]any{"voices": info.Voices, "default_voice_id": info.DefaultVoiceID})
}

func (t *stationAttachmentTools) speechProductAllowed(in companion.Input, p stationWechatSpeechProduct) bool {
	if t.projectOwnerAllowed == nil || !t.projectOwnerAllowed(in) || !t.replyAllowed(in) || t.store == nil ||
		p.ID != p.id() || p.Account != in.Account || p.Thread != in.Thread || p.Sender != in.Sender ||
		p.Trace != in.TraceID || !p.MediaGrant.Equal(t.replies[in.Thread]) {
		return false
	}
	current, ok := t.store.SourceForInput(in)
	return ok && *current == p.Source
}

func (t *stationAttachmentTools) speechProduct(id string) (stationWechatSpeechProduct, *os.File, error) {
	var p stationWechatSpeechProduct
	if !artifactID(id, "speech-") {
		return p, nil, errors.New("语音成品编号无效")
	}
	root, err := workspacefs.OpenPath(filepath.Join(t.dir, "speech-products", id))
	if err != nil {
		return p, nil, err
	}
	defer root.Close()
	meta, err := workspacefs.OpenRegular(root, "record.json")
	if err != nil {
		return p, nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(meta, 16<<10))
	meta.Close()
	if err != nil || len(raw) >= 16<<10 || json.Unmarshal(raw, &p) != nil || p.ID != id || p.id() != id ||
		p.Bytes < 1 || p.Bytes > localTTSWAVLimit || !artifactID("sha-"+p.WAVHash, "sha-") {
		return stationWechatSpeechProduct{}, nil, errors.New("语音成品记录无效")
	}
	file, err := workspacefs.OpenRegular(root, "output.wav")
	if err != nil {
		return p, nil, err
	}
	return p, file, nil
}

func (t *stationAttachmentTools) saveSpeechProduct(p stationWechatSpeechProduct, wav []byte) error {
	root, err := workspacefs.EnsurePath(filepath.Join(t.dir, "speech-products"))
	if err != nil {
		return err
	}
	defer root.Close()
	stage := ".speech-" + identity.RandomToken()
	if err = root.Mkdir(stage, 0700); err != nil {
		return err
	}
	defer func() {
		_ = root.Remove(filepath.Join(stage, "output.wav"))
		_ = root.Remove(filepath.Join(stage, "record.json"))
		_ = root.Remove(stage)
	}()
	output, err := root.OpenFile(filepath.Join(stage, "output.wav"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = output.Write(wav)
	if err == nil {
		err = output.Sync()
	}
	err = errors.Join(err, output.Close())
	if err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	meta, err := root.OpenFile(filepath.Join(stage, "record.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = meta.Write(raw)
	if err == nil {
		err = meta.Sync()
	}
	err = errors.Join(err, meta.Close())
	if err != nil {
		return err
	}
	return root.Rename(stage, p.ID)
}

func (t *stationAttachmentTools) callPublicSpeech(ctx context.Context, raw json.RawMessage) (string, error) {
	if t.speechMu == nil {
		return "", errors.New("微信语音处理暂不可用")
	}
	t.speechMu.Lock()
	defer t.speechMu.Unlock()
	if _, ok := t.publicSpeechTool(ctx); !ok || t.store == nil || t.trace.TraceID == "" {
		return "", wechatapp.ErrAttachmentDenied
	}
	if len(raw) > 80<<10 {
		return "", mcptools.ArgumentError("$", "语音请求过长")
	}
	var request localTTSRequest
	if strictLocalMemoryDecode(raw, &request) != nil {
		return "", mcptools.ArgumentError("$", "请提供有效的语音合成参数")
	}
	source, ok := t.store.SourceForInput(t.trace)
	if !ok {
		return "", errors.New("微信原消息来源已变化")
	}
	requestHash := sha256.Sum256(raw)
	p := stationWechatSpeechProduct{Account: t.trace.Account, Thread: t.trace.Thread, Sender: t.trace.Sender,
		Trace: t.trace.TraceID, Source: *source, MediaGrant: t.replies[t.trace.Thread], RequestHash: hex.EncodeToString(requestHash[:])}
	p.ID = p.id()
	if previous, file, err := t.speechProduct(p.ID); err == nil {
		file.Close()
		if previous.Source != p.Source || previous.RequestHash != p.RequestHash || !t.speechProductAllowed(t.trace, previous) {
			return "", errors.New("微信语音原请求已变化")
		}
		media := previous.media()
		t.preparedSpeech(media)
		return localProcessJSON(map[string]any{"status": "prepared", "media": []companion.GeneratedMedia{media}, "reused": true})
	}
	record, err := t.projects.home.identity.Read()
	if err != nil || record.LocalOwner == nil {
		return "", errors.New("工作站身份不可用")
	}
	result, err := t.projects.synthesizePublicSpeech(ctx, record.FishID, request)
	if err != nil {
		return "", err
	}
	if !t.speechProductAllowed(t.trace, p) {
		return "", wechatapp.ErrAttachmentDenied
	}
	wavHash := sha256.Sum256(result.WAV)
	p.VoiceID, p.VoiceVersion, p.WAVHash, p.Bytes = result.VoiceID, result.Version, hex.EncodeToString(wavHash[:]), int64(len(result.WAV))
	if err = t.saveSpeechProduct(p, result.WAV); err != nil {
		return "", err
	}
	media := p.media()
	t.preparedSpeech(media)
	return localProcessJSON(map[string]any{"status": "prepared", "media": []companion.GeneratedMedia{media}, "voice_id": result.VoiceID})
}

func (t *stationAttachmentTools) preparedSpeech(media companion.GeneratedMedia) {
	if t.prepared == nil {
		t.prepared = map[string]companion.GeneratedMedia{}
	}
	t.prepared[media.ID] = media
}

func (t *stationAttachmentTools) stageSpeech(ctx context.Context, in companion.Input, id string) (string, error) {
	p, file, err := t.speechProduct(id)
	if err != nil {
		return "", wechatapp.ErrAttachmentDenied
	}
	defer file.Close()
	if !t.speechProductAllowed(in, p) {
		return "", wechatapp.ErrAttachmentDenied
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, p.Bytes+1))
	if err != nil || n != p.Bytes || hex.EncodeToString(hash.Sum(nil)) != p.WAVHash {
		return "", errors.New("语音成品内容已变化")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	upload, err := wechatapp.StageMediaUpload(ctx, t.native, wechatapp.MediaUpload{
		Account: in.Account, Filename: p.media().Title, MIME: "audio/wav", Bytes: p.Bytes, SHA256: p.WAVHash,
	}, file, func() bool { return t.speechProductAllowed(in, p) })
	if err != nil || !t.speechProductAllowed(in, p) {
		return "", errors.Join(err, wechatapp.ErrAttachmentDenied)
	}
	return upload, nil
}
