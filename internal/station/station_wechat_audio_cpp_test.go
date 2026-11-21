package station

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type syntheticAudioCppBackend struct {
	WAV             []byte
	Calls           atomic.Int32
	Transcription   string
	TranscribeCalls atomic.Int32
}

func (b *syntheticAudioCppBackend) Info(context.Context, string) (localAudioCppDeployment, error) {
	return localAudioCppDeployment{Binding: "synthetic-audio-binding", Backend: "cpu", Version: "synthetic",
		Models: []localAudioCppModelView{{ID: "kokoro", Task: "tts", Configured: true}}}, nil
}
func (b *syntheticAudioCppBackend) Start(context.Context, string) error { return nil }
func (b *syntheticAudioCppBackend) Stop(context.Context) error          { return nil }
func (b *syntheticAudioCppBackend) Close(context.Context) error         { return nil }
func (b *syntheticAudioCppBackend) Synthesize(_ context.Context, _, binding string, request localTTSRequest) (localTTSResult, error) {
	b.Calls.Add(1)
	return localTTSResult{VoiceID: request.VoiceID, Version: binding, WAV: b.WAV}, nil
}
func (b *syntheticAudioCppBackend) Transcribe(_ context.Context, _, _ string, wav []byte) (string, error) {
	if len(wav) < 4 || string(wav[:4]) != "RIFF" {
		return "", errors.New("synthetic WAV missing")
	}
	b.TranscribeCalls.Add(1)
	return b.Transcription, nil
}

func TestWechatAudioCppSynthesisOwnedByApplication(t *testing.T) {
	if os.Getenv("AHS_LOCAL_PROJECT_TOOL_RUNTIME_ROOT") == "" {
		t.Skip("Windows project-tool runtime required")
	}
	f := localChatFixture(t)
	f.handler.chat.projectTools.options = localProjectToolWindowsOptionsFor(t, localAudioCppPackage)
	backend := &syntheticAudioCppBackend{WAV: syntheticMonoWAV(t, 8000, 8000)}
	f.handler.chat.projectTools.audio = backend
	if code, out := f.request("GET", "/api/local/project-tools", nil, true); code != http.StatusOK {
		t.Fatal(code, out)
	}
	route := "/api/local/project-tools/" + localAudioCppPackage
	for _, action := range []string{"install", "enable"} {
		input := map[string]any{"action": action}
		if action == "install" {
			input["approve_artifacts"] = true
		} else {
			input["revision"] = localProjectToolRevision(t, localProjectToolByID(t, f, localAudioCppPackage))
		}
		if code, out := f.request("PUT", route, input, true); code != http.StatusOK {
			t.Fatal(code, out)
		}
	}
	key := filepath.Join(t.TempDir(), "native-token")
	if err := os.WriteFile(key, []byte("synthetic-token"), 0600); err != nil {
		t.Fatal(err)
	}
	native, err := linuxnative.New(linuxnative.Config{URL: "http://127.0.0.1:1", TokenFile: key, Account: "synthetic-account"})
	if err != nil {
		t.Fatal(err)
	}
	memory, err := companion.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	now := time.Now().UTC()
	in := companion.Input{Channel: "wechat", Account: native.Account(), Thread: "synthetic-thread", Sender: "owner", TraceID: "synthetic-audio-cpp",
		Text: "synthesize", MessageTime: now.Format(time.RFC3339Nano), Mode: "direct", Directed: true}
	in.Source = &companion.SourceRef{InstanceID: memory.SourceInstanceID(), MessageID: in.TraceID, Account: in.Account, Thread: in.Thread,
		Sender: in.Sender, OccurredAt: in.MessageTime}
	config := companion.Defaults()
	config.Permissions.Remember = true
	if err := memory.Observe(in, config, now); err != nil {
		t.Fatal(err)
	}
	in.Source, _ = memory.SourceForInput(in)
	grant := now.Add(-time.Minute)
	adapter := &stationAttachmentTools{native: native, store: memory, dir: t.TempDir(), projects: f.handler.chat.projectTools,
		grants: map[string]time.Time{in.Thread: grant}, replies: map[string]time.Time{in.Thread: grant}, blocked: func() bool { return false }, externalMu: new(sync.Mutex),
		projectOwnerAllowed: func(current companion.Input) bool { return current.Sender == in.Sender }}
	adapter.SetTrace(in)
	listed, err := adapter.List(t.Context())
	if err != nil || !hasTool(listed, localAudioCppModelsTool) || !hasTool(listed, localAudioCppSynthesizeTool) || hasTool(listed, localAudioCppTranscribeTool) {
		t.Fatal("audio.cpp application catalog incorrect", err)
	}
	models, err := adapter.Call(t.Context(), localAudioCppModelsTool, json.RawMessage(`{}`))
	if err != nil || !strings.Contains(models, "kokoro") {
		t.Fatal("audio.cpp models unavailable", models, err)
	}
	request := json.RawMessage(`{"text":"合成测试"}`)
	first, err := adapter.Call(t.Context(), localAudioCppSynthesizeTool, request)
	if err != nil || backend.Calls.Load() != 1 || !strings.Contains(first, `"state":"completed"`) || !strings.Contains(first, `"delivery_kind":"file"`) {
		t.Fatal("audio.cpp synthesis not prepared", first, err)
	}
	second, err := adapter.Call(t.Context(), localAudioCppSynthesizeTool, request)
	if err != nil || backend.Calls.Load() != 1 || !strings.Contains(second, `"reused":true`) {
		t.Fatal("audio.cpp synthesis repeated", second, err)
	}
	publicStore, localInput, err := f.handler.chat.conversation()
	if err != nil {
		t.Fatal(err)
	}
	public := f.handler.chat.tools(publicStore, localInput)
	public.publicUseOnly = true
	public.run = companion.NewGuardedToolRun(func(context.Context) error { return nil })
	publicList, err := public.List(t.Context())
	if err != nil || hasTool(publicList, localAudioCppSynthesizeTool) || hasTool(publicList, localAudioCppTranscribeTool) || hasTool(publicList, localAudioToolName) {
		t.Fatal("source-bound audio tools leaked into application", err)
	}
}
