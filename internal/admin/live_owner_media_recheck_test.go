package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

// Explicit read-only acceptance against the owner's original native request.
// Copies one authorized cached video into an isolated test workspace, uses the
// actual configured ASR, and never starts a bot or submits a WeChat send.
func TestLiveOwnerMediaRecheckNoWechatSend(t *testing.T) {
	root := os.Getenv("WECHAT_OWNER_MEDIA_RECHECK_ROOT")
	if root == "" {
		t.Skip("opt-in authorized received video only")
	}
	var selected struct{ Account, OwnerRequest, SourceMessage, SourceThread, Hash string }
	data, err := os.ReadFile(filepath.Join(root, "reported-video-recheck.json"))
	if err != nil || json.Unmarshal(data, &selected) != nil {
		t.Fatal("private acceptance selection unavailable")
	}
	native, err := linuxnative.New(linuxnative.Config{URL: "http://127.0.0.1:6174", TokenFile: filepath.Join(root, "api-token"), Account: selected.Account})
	if err != nil {
		t.Fatal("native unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	origin, err := native.Message(ctx, selected.OwnerRequest)
	if err != nil || origin.AccountID != selected.Account || origin.ThreadID != origin.SenderID {
		t.Fatal("not the original private owner request")
	}
	source, err := native.Message(ctx, selected.SourceMessage)
	if err != nil || source.AccountID != selected.Account || source.ThreadID != selected.SourceThread {
		t.Fatal("not the authorized source")
	}
	cfg, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal("configuration unavailable")
	}
	var settings agentSettings
	raw, err := os.ReadFile(filepath.Join(root, "admin/agent-config.json"))
	if err != nil || json.Unmarshal(raw, &settings) != nil || !contains(settings.OwnerIDs, origin.SenderID) || !settings.CrossConversation || !permittedRecipient(cfg, source.ThreadID) {
		t.Fatal("live owner or source not authorized")
	}
	f := setup(t)
	f.manager.config = cfg
	f.manager.native = native
	settings.Revision = f.manager.agent.config().Revision // isolated fixture, not the live config file
	if err = f.manager.agent.save(settings); err != nil {
		t.Fatalf("test configuration could not be saved: %v", err)
	}
	legacySpeechSource, err := loadSpeech(filepath.Join(root, "admin"))
	if err == nil {
		writeLegacySpeechMigrationFixture(t, f.manager, legacySpeechSource)
	}
	if err != nil {
		t.Fatal("speech unavailable")
	}
	digest := sha256.Sum256([]byte(selected.Account))
	workspace := os.Getenv("WECHAT_OWNER_MEDIA_WORKSPACE")
	bytes, err := os.ReadFile(filepath.Join(workspace, "pri_workspace", hex.EncodeToString(digest[:]), "assets/objects", selected.Hash))
	if err != nil {
		t.Fatal("authorized cached original unavailable")
	}
	actual := sha256.Sum256(bytes)
	if hex.EncodeToString(actual[:]) != selected.Hash {
		t.Fatal("original hash mismatch")
	}
	_, err = f.manager.rememberAssetFormats(companion.Input{Channel: "wechat", Account: selected.Account, Thread: source.ThreadID, TraceID: source.ID}, assetOriginal{Data: bytes, MIME: "video/mp4", Filename: "authorized-recheck.mp4"}, nil)
	if err != nil {
		t.Fatal("private test cache unavailable")
	}
	p, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal("tool access unavailable")
	}
	defer p.hub.Close()
	p.SetTrace(companion.Input{Channel: "wechat", Account: origin.AccountID, Thread: origin.ThreadID, Sender: origin.SenderID, TraceID: origin.ID})
	var result map[string]any
	for _, action := range []string{"info", "audio"} {
		raw, _ := json.Marshal(map[string]any{"action": action, "message_id": source.ID})
		out, err := p.Call(ctx, "video_inspect", raw)
		if err != nil || json.Unmarshal([]byte(out), &result) != nil || result["source_thread_id"] != source.ThreadID || result["origin_thread_id"] != origin.ThreadID {
			t.Fatalf("owner cross-media recheck failed: %v", err)
		}
	}
	if result["transcription_state"] != "no_recognizable_speech" || result["requested_range_complete"] != true {
		t.Fatal("reported audio outcome not reproduced")
	}
	report := map[string]any{"native_owner_verified": true, "source_account_verified": true, "cross_conversation_read_succeeded": true, "audio_state": result["transcription_state"], "range_complete": result["requested_range_complete"], "audio_end_seconds": result["end_seconds"], "wechat_sends": 0}
	file := filepath.Join(root, "owner-media-recheck-result.json")
	encoded, _ := json.Marshal(report)
	if err = os.WriteFile(file, encoded, 0600); err != nil {
		t.Fatal("could not save private acceptance result")
	}
	t.Log(string(encoded))
}
