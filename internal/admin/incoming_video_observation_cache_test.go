package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestIncomingVideoObservationCacheReusesContentWithCurrentSourceAndVariant(t *testing.T) {
	f, _, _, ctx := mediaPolicyFixture(t, false)
	a := mediaUnderstanding{manager: f.manager}
	capabilities, err := a.videoObservationCapabilities(ctx)
	if err != nil || !capabilities.Visual || capabilities.Audio {
		t.Fatal("synthetic vision-only capability unavailable", capabilities, err)
	}
	data := []byte("synthetic original deliberately cannot be sampled as video")
	first := linuxnative.Message{ID: "video-first", AccountID: "test", ThreadID: "filehelper", SenderID: "synthetic-user", Direction: "incoming", Type: 43, CreatedAt: "2026-11-25T01:00:00Z"}
	remember := func(msg linuxnative.Message) mediaAsset {
		t.Helper()
		asset, err := f.manager.rememberAssetFormats(companion.Input{Channel: "wechat", Account: msg.AccountID, Thread: msg.ThreadID, TraceID: msg.ID, Sender: msg.SenderID, MessageTime: msg.CreatedAt}, assetOriginal{InboundMessage: &msg, Data: data, MIME: "video/mp4", Filename: "received.mp4"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return asset
	}
	asset := remember(first)
	variant := "native-original"
	snapshot := incomingVideoObservation{Version: incomingVideoObservationVersion, SHA256: asset.Hash, Variant: variant, VisualEnabled: true, Duration: 12, FrameTimes: []float64{1, 6, 11}, Visual: "蓝色画面与字幕"}
	if err := f.manager.saveIncomingVideoObservation(snapshot, capabilities); err != nil {
		t.Fatal(err)
	}
	name := "videos-" + incomingVideoObservationKey(asset.Hash, variant, capabilities) + ".json"
	if _, err := os.Stat(filepath.Join(f.manager.messageMediaRoot(), "observations", name)); err != nil {
		t.Fatal("observation was not stored in the WeChat workspace", err)
	}
	second := first
	second.ID, second.CreatedAt = "video-second", "2026-11-25T01:01:00Z"
	asset = remember(second)
	if len(asset.Requests) != 2 || asset.Requests[0].TraceID != first.ID || asset.Requests[1].TraceID != second.ID {
		t.Fatal("same content lost independent message sources", asset.Requests)
	}
	evidence := bot.MediaObservationEvidence{}
	a.evidence = &evidence
	// Invalid video bytes would fail sampleVideo. A successful response therefore
	// proves the cache hit bypassed frame decoding and both providers.
	result, err := a.observeCachedIncomingVideo(ctx, second, data, asset, variant)
	if err != nil || !strings.Contains(result, second.ID) || strings.Contains(result, first.ID) || !strings.Contains(result, `"duration_seconds":12`) || !strings.Contains(evidence.Excerpt, snapshot.Visual) {
		t.Fatal("cached observation lost the current source or coverage", result, evidence, err)
	}
	if _, ok := f.manager.readIncomingVideoObservation(asset.Hash, "file_attachment", capabilities); ok {
		t.Fatal("different video variant reused an observation")
	}
	if _, ok := f.manager.readIncomingVideoObservation(asset.Hash, variant, videoObservationCapabilities{Visual: true, Audio: true}); ok {
		t.Fatal("different processing parameters reused an observation")
	}
}
