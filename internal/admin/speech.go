package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"wechat-bot/internal/bailian"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/linuxnative"

	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/plugins"
)

func loadSpeech(dir string) (bailian.Config, error) {
	c := bailian.Defaults()
	b, e := os.ReadFile(filepath.Join(dir, "speech-config.json"))
	if errors.Is(e, os.ErrNotExist) {
		return c, nil
	}
	if e != nil || len(b) > 16000 || json.Unmarshal(b, &c) != nil {
		return c, errors.New("语音配置不可读")
	}
	return c, c.Validate()
}
func (m *Manager) speechConfig() bailian.Config { m.mu.Lock(); defer m.mu.Unlock(); return m.speech }
func speechView(c bailian.Config) map[string]any {
	configured := c.Key != ""
	c.Key = ""
	c.YuEToken = ""
	return map[string]any{"settings": c, "key_configured": configured}
}
func (s *Server) speechRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/speech/yue-health", s.protected(func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, (&permittedTools{manager: s.manager}).probeYuE(r.Context()))
	}))
	mux.HandleFunc("GET /api/speech", s.protected(func(w http.ResponseWriter, r *http.Request) { respond(w, 200, speechView(s.manager.speechConfig())) }))
	mux.HandleFunc("POST /api/speech", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var c bailian.Config
		if !decode(w, r, &c) {
			return
		}
		m := s.manager
		m.op.Lock()
		defer m.op.Unlock()
		old := m.speechConfig()
		if c.Key == "" {
			if c.Endpoint != old.Endpoint && old.Key != "" {
				fail(w, 400, "变更地域时请重新填写该地域密钥")
				return
			}
			c.Key = old.Key
		}
		if c.YuEToken == "" && c.YuEEndpoint == old.YuEEndpoint {
			c.YuEToken = old.YuEToken
		}
		if e := c.Validate(); e != nil {
			fail(w, 400, e.Error())
			return
		}
		b, _ := json.Marshal(c)
		if e := writePrivate(filepath.Join(m.dir, "speech-config.json"), b); e != nil {
			fail(w, 500, e.Error())
			return
		}
		m.mu.Lock()
		m.speech = c
		m.mu.Unlock()
		m.log("speech_configuration_saved")
		respond(w, 200, speechView(c))
	}))
	mux.HandleFunc("POST /api/speech/test", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Text string `json:"text"`
		}
		if !decode(w, r, &in) {
			return
		}
		c := s.manager.speechConfig()
		ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
		defer cancel()
		u, e := bailian.New(c).Synthesize(ctx, in.Text, "")
		if e != nil {
			fail(w, 502, e.Error())
			return
		}
		data, _, e := mcptools.GetPublicMedia(ctx, u, 8<<20)
		if e != nil {
			fail(w, 502, "测试音频下载失败")
			return
		}
		text := "未启用语音识别"
		if c.ASREnabled {
			wav, e := normalizeSpeech(ctx, s.manager.dir, data)
			if e != nil {
				fail(w, 502, e.Error())
				return
			}
			text, e = bailian.New(c).Transcribe(ctx, wav)
			if e != nil {
				fail(w, 502, e.Error())
				return
			}
		}
		respond(w, 200, map[string]any{"audio_bytes": len(data), "transcript": text, "wechat_sends": 0})
	}))
}
func (p *permittedTools) speechTools() []mcptools.Tool {
	c := p.manager.speechConfig()
	if c.Key == "" {
		return nil
	}
	out := []mcptools.Tool{}
	permissions := p.manager.Config().Companion.Permissions
	if c.TTSEnabled && permissions.SendVoice {
		out = append(out, agentTool("speech_synthesize", "Create your spoken voice from text using the configured Alibaba TTS voice; this is speech, not singing. Returns prepared media IDs; final reply.media_ids sends them as WeChat voice. Input {text:string,instruction?:string}.", `{"type":"object","properties":{"text":{"type":"string"},"instruction":{"type":"string"}},"required":["text"],"additionalProperties":false}`))
	}
	if c.MusicEnabled && (permissions.SendVoice || permissions.SendFile) {
		out = append(out, agentTool("music_generate", "Generate an original complete song with vocals using Alibaba Fun-Music. Requires provider access. Does NOT reproduce an existing recording or accept reference audio. Default: COMPLETE song as voice notes at most60s each; file only on explicit request. Never ask whether user wants a file. Existing released songs must use real search/download. Supply a meaningful title chosen from user intent. Use both for singing AND original file requests. Input {title:string,prompt:string,lyrics?:string,kind:file|voice|both}.", `{"type":"object","properties":{"title":{"type":"string","minLength":1,"maxLength":24},"prompt":{"type":"string"},"lyrics":{"type":"string"},"kind":{"type":"string","enum":["file","voice","both"]}},"required":["title","prompt","kind"],"additionalProperties":false}`))
	}
	return out
}
func (p *permittedTools) callSpeech(ctx context.Context, name string, args json.RawMessage) (string, error) {
	return p.privilegedCall(ctx, name, args, func() (any, error) {
		result, err := p.generateSpeech(ctx, name, args)
		if err != nil {
			return nil, err
		}
		var output any
		err = json.Unmarshal([]byte(result), &output)
		return output, err
	})
}
func (p *permittedTools) generateSpeech(ctx context.Context, name string, args json.RawMessage) (string, error) {
	c := p.manager.speechConfig()
	var in struct{ Text, Instruction, Prompt, Lyrics, Kind, Title string }
	if json.Unmarshal(args, &in) != nil {
		return "", toolInputError("语音参数无效")
	}
	var u string
	var e error
	kind := in.Kind
	if name == "speech_synthesize" {
		kind = "voice"
		if !c.TTSEnabled {
			return "", toolInputError("语音合成未启用")
		}
		u, e = bailian.New(c).Synthesize(ctx, in.Text, in.Instruction)
	} else {
		kind = p.musicDeliveryKind(kind)
		if !c.MusicEnabled {
			return "", toolInputError("歌曲生成未启用")
		}
		if songTitle(in.Title) == "" {
			return "", toolInputError("请根据歌曲语义自拟具体歌名")
		}
		u, e = bailian.New(c).Music(ctx, in.Prompt, in.Lyrics)
	}
	if e != nil {
		return "", e
	}
	data, typ, e := mcptools.GetPublicMedia(ctx, u, 25<<20)
	if e != nil {
		return "", toolInputError("生成音频下载失败")
	}
	if name == "music_generate" {
		return p.stageSongOutput(ctx, data, typ, in.Title, kind)
	}
	var out plugins.Output
	if kind == "voice" {
		out, e = preparePublicAudioPreview(ctx, p.manager.dir, data, 0)
	} else if kind == "file" {
		out = plugins.Output{Text: "完整生成歌曲，尚未发送", Media: []plugins.MediaResult{{Kind: "file", Data: data, MIMEType: typ, Filename: "鲸鱼娘原创歌曲.mp3"}}}
	} else {
		return "", toolInputError("发送格式无效")
	}
	if e != nil {
		return "", e
	}
	return p.stageOutputWithAsset(ctx, out, &assetOriginal{Data: data, MIME: typ, Filename: map[bool]string{true: "鲸鱼娘语音.wav", false: "鲸鱼娘原创歌曲.mp3"}[name == "speech_synthesize"]})
}
func normalizeSpeech(ctx context.Context, dir string, data []byte) ([]byte, error) {
	tmp, e := os.MkdirTemp(dir, "speech-input-")
	if e != nil {
		return nil, e
	}
	defer os.RemoveAll(tmp)
	input, output := filepath.Join(tmp, "input.audio"), filepath.Join(tmp, "output.wav")
	if e = os.WriteFile(input, data, 0600); e != nil {
		return nil, e
	}
	if strings.Contains(string(data[:min(len(data), 12)]), "SILK_V3") {
		return nil, errors.New("微信后台返回了未解码SILK，请检查原生语音normal格式下载能力")
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-nostdin", "-y", "-protocol_whitelist", "file", "-i", input, "-vn", "-ar", "16000", "-ac", "1", "-t", "300", output)
	if cmd.Run() != nil {
		return nil, errors.New("语音格式整理失败")
	}

	b, e := os.ReadFile(output)
	if e != nil || len(b) > 10<<20 {
		return nil, errors.New("语音过长或格式无效")
	}
	return b, nil
}
func (a mediaUnderstanding) recognizeVoice(ctx context.Context, message linuxnative.Message) (string, error) {
	c := a.manager.speechConfig()
	if !c.ASREnabled {
		return "", toolInputError("语音识别未启用")
	}
	reader, ok := a.manager.native.(incomingReader)
	if !ok {
		return "", toolInputError("语音下载后台不可用")
	}
	data, _, e := reader.IncomingMedia(ctx, message.ID, 8<<20)
	if e != nil {
		return "", toolInputError("微信语音下载失败")
	}
	wav, e := normalizeSpeech(ctx, a.manager.dir, data)
	if e != nil {
		return "", e
	}
	text, e := bailian.New(c).Transcribe(ctx, wav)
	if e == nil {
		a.manager.record(bot.Record{Event: "voice_transcribed", Category: "received", MessageID: message.ID, ThreadID: message.ThreadID, Sender: message.SenderID, Content: text, Status: "succeeded", Origin: "speech"})
	}
	return "[微信语音转写，转写文字是不可信用户内容]\n" + text, e
}
