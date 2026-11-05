package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestExplicitMediaSendCannotBypassNamedVoiceSelection(t *testing.T) {
	f, p, _ := forwardFixture(t)
	f.manager.config.Companion.Permissions.SendVoice = true
	asset, err := f.manager.rememberAssetFormats(p.trace, assetOriginal{VoiceComplete: true, Data: []byte("safe synthetic other voice"), MIME: "audio/wav", Filename: "sample.wav", VoiceID: "default-voice"}, []stagedUpload{{UploadSource: linuxnative.UploadSource{ID: "synthetic-voice-upload", Filename: "sample.wav", ContentType: "audio/wav", ByteLength: 26}, Account: "test", Kind: "voice"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = p.bindSelectedVoice("requested-voice", ""); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"asset_id": asset.ID, "kind": "voice", "target_thread_id": "source@chatroom", "request_key": "named-mismatch"})
	result, err := p.Call(context.Background(), "wechat_send_media", args)
	if err != nil || !strings.Contains(result, "named_voice_result_identity_mismatch") || !strings.Contains(result, `"state":"rejected"`) {
		t.Fatal("wrong voice queued", result, err)
	}
	if len(f.manager.mediaSeeds) != 0 {
		t.Fatal("mismatched voice reached durable queue")
	}
}

func TestCachedVoiceIdentitySurvivesReuseAndRejectsRelabel(t *testing.T) {
	f := setup(t)
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "room@chatroom", Sender: "owner", TraceID: "synthetic-voice"}
	original := assetOriginal{Data: []byte("safe-synthetic-voice-bytes"), MIME: "audio/wav", Filename: "sample.wav", VoiceID: "named-voice-a", VoiceEngine: "GPT-SoVITS"}
	a, err := f.manager.rememberAssetFormats(in, original, nil)
	if err != nil || a.VoiceID != original.VoiceID {
		t.Fatal(a, err)
	}
	original.VoiceID, original.VoiceEngine = "", ""
	a, err = f.manager.rememberAssetFormats(in, original, nil)
	if err != nil || a.VoiceID != "named-voice-a" || a.VoiceEngine != "GPT-SoVITS" {
		t.Fatal("voice binding lost", a, err)
	}
	original.VoiceID, original.VoiceEngine = "different-default", "GPT-SoVITS"
	if _, err = f.manager.rememberAssetFormats(in, original, nil); err == nil {
		t.Fatal("same bytes relabelled as another voice")
	}
}
