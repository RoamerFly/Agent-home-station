package station

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestWechatSpeechUsesWorkstationToolAndKeepsSourceInApplication(t *testing.T) {
	f, backend := newLocalTTSFixture(t)
	backend.defaultVoiceID = backend.voice.ID
	key := filepath.Join(t.TempDir(), "native-token")
	if err := os.WriteFile(key, []byte("synthetic-token"), 0600); err != nil {
		t.Fatal(err)
	}
	var uploads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/accounts/synthetic-account/uploads" || r.Method != http.MethodPost {
			t.Errorf("unexpected native path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer file.Close()
		wav, err := io.ReadAll(file)
		if err != nil || !bytes.Equal(wav, backend.wav) {
			t.Error("uploaded WAV differed from the workstation output", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		uploads.Add(1)
		sum := sha256.Sum256(wav)
		fmt.Fprintf(w, `{"id":"synthetic-upload","name":%q,"byteLength":%d,"sha256":"%x"}`, header.Filename, len(wav), sum)
	}))
	defer server.Close()
	native, err := linuxnative.New(linuxnative.Config{URL: server.URL, TokenFile: key, Account: "synthetic-account"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := companion.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	in := companion.Input{Channel: "wechat", Account: native.Account(), Thread: "test@chatroom", Sender: "owner",
		TraceID: "synthetic-message", Text: "请朗读一句话", MessageTime: now.Format(time.RFC3339Nano), Mode: "direct", Directed: true}
	in.Source = &companion.SourceRef{InstanceID: store.SourceInstanceID(), MessageID: in.TraceID, Account: in.Account,
		Thread: in.Thread, Sender: in.Sender, OccurredAt: in.MessageTime}
	config := companion.Defaults()
	config.Permissions.Remember = true
	if err = store.Observe(in, config, now); err != nil {
		t.Fatal(err)
	}
	in.Source, _ = store.SourceForInput(in)
	if in.Source == nil {
		t.Fatal("application source was not observed")
	}
	grant := now.Add(-time.Minute)
	adapter := &stationAttachmentTools{native: native, store: store, dir: t.TempDir(),
		replies: map[string]time.Time{in.Thread: grant},
		blocked: func() bool { return false }, projects: f.handler.chat.projectTools, speechMu: new(sync.Mutex),
		projectOwnerAllowed: func(current companion.Input) bool { return current.Sender == in.Sender }}
	adapter.SetTrace(in)
	items, err := adapter.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		found = found || item.Name == localTTSStartTool
	}
	if !found {
		t.Fatal("workstation speech was not offered to the application")
	}
	speed := 0.8
	args, _ := json.Marshal(localTTSRequest{Text: "A synthetic test.", Speed: &speed, Language: "en"})
	result, err := adapter.Call(t.Context(), localTTSStartTool, args)
	if err != nil || backend.calls.Load() != 1 {
		t.Fatalf("application speech call failed: %q, %v", result, err)
	}
	var prepared struct {
		Media []companion.GeneratedMedia `json:"media"`
	}
	if json.Unmarshal([]byte(result), &prepared) != nil || len(prepared.Media) != 1 || prepared.Media[0].Kind != "file" {
		t.Fatalf("speech product was not prepared: %s", result)
	}
	if _, ok := adapter.Media(prepared.Media[0].ID); !ok {
		t.Fatal("application could not read its prepared product")
	}
	if upload, err := adapter.stageReply(t.Context(), in, "file", prepared.Media[0].AssetID); err != nil || upload != "synthetic-upload" || uploads.Load() != 1 {
		t.Fatalf("prepared WAV was not staged once: %q, %v", upload, err)
	}
	if _, err = adapter.Call(context.Background(), localTTSStartTool, args); err != nil || backend.calls.Load() != 1 {
		t.Fatal("same message generated speech twice", err)
	}
	changed := in
	changed.Sender = "another-member"
	adapter.SetTrace(changed)
	if _, ok := adapter.Media(prepared.Media[0].ID); ok {
		t.Fatal("another sender inherited the generated product")
	}
	if _, err := adapter.stageReply(t.Context(), changed, "file", prepared.Media[0].AssetID); err == nil || uploads.Load() != 1 {
		t.Fatal("another sender staged the generated WAV", err)
	}
	adapter.SetTrace(in)
	installed := localProjectToolByID(t, f, localTTSPackage)
	if code, out := f.request("PUT", "/api/local/project-tools/station-speech", map[string]any{"action": "disable", "revision": localProjectToolRevision(t, installed)}, true); code != http.StatusOK {
		t.Fatalf("disable workstation speech: %d %#v", code, out)
	}
	if _, ok := adapter.publicSpeechTool(t.Context()); ok {
		t.Fatal("application kept the disabled workstation speech tool")
	}
	if _, err := adapter.Call(t.Context(), localTTSStartTool, args); err == nil || backend.calls.Load() != 1 {
		t.Fatal("disabled workstation speech was called through the application", err)
	}
}
