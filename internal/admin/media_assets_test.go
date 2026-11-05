package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/plugins"
)

func TestCachedRecordingRemainsSearchableBeyondChatMemoryAge(t *testing.T) {
	f := setup(t)
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "local-song"}
	a, err := f.manager.rememberAssetFormats(in, assetOriginal{Data: []byte("synthetic local recording"), MIME: "audio/wav", Filename: "原曲-合成歌曲.wav"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	items, err := f.manager.loadAssets()
	if err != nil {
		t.Fatal(err)
	}
	for i := range items {
		items[i].Updated = time.Now().Add(-365 * 24 * time.Hour)
	}
	if err = f.manager.saveAssets(items); err != nil {
		t.Fatal(err)
	}
	rows, total, err := f.manager.findAssetPage(in, "", "", "合成歌曲", 0, 20)
	if err != nil || total != 1 || rows[0]["asset_id"] != a.ID {
		t.Fatal("local original hidden by chat expiry", rows, total, err)
	}
	in.Thread = "other-room"
	_, total, err = f.manager.findAssetPage(in, "", "", "合成歌曲", 0, 20)
	if err != nil || total != 0 {
		t.Fatal("local lookup crossed session", total, err)
	}
}

func TestObservationCacheUsesPhysicalSpaceNotAggregateIndexQuota(t *testing.T) {
	f := setup(t)
	// Synthetic metadata only: no large or real user files are created.
	items := []mediaAsset{{ID: "synthetic-old-a", Bytes: 300 << 20}, {ID: "synthetic-old-b", Bytes: 300 << 20}}
	if err := f.manager.saveAssets(items); err != nil {
		t.Fatal(err)
	}
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "synthetic-video-observation"}
	original := []byte("synthetic verified video bytes")
	asset, err := f.manager.rememberAssetFormats(in, assetOriginal{Data: original, MIME: "video/mp4", Filename: "synthetic.mp4"}, nil)
	if err != nil {
		t.Fatal("aggregate index quota blocked a small observation", err)
	}
	got, err := f.manager.cachedAssetData(asset)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("verified original was not reusable", err)
	}
	if asset.FirstRequest == nil || asset.FirstRequest.TraceID != in.TraceID {
		t.Fatal("observation source identity lost")
	}
}

func TestCachedPublicSharePreservesPlaybackParametersOnlyOnKnownPage(t *testing.T) {
	f := setup(t)
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "synthetic-share"}
	for _, test := range []struct{ url, want string }{
		{"https://v.m.chenzhongtech.com/fw/photo/3xsynthetic12345?photoId=3xsynthetic12345&shareToken=synthetic-share-value", "https://v.m.chenzhongtech.com/fw/photo/3xsynthetic12345?photoId=3xsynthetic12345&shareToken=synthetic-share-value"},
		{"https://media.example.org/video.mp4?signature=synthetic-value", "https://media.example.org/video.mp4"},
	} {
		asset, err := f.manager.rememberAssetFormats(in, assetOriginal{Data: []byte(test.url), MIME: "video/mp4", URL: test.url}, nil)
		if err != nil || asset.URL != test.want {
			t.Fatal("source parameters changed or arbitrary signed URL leaked", asset.URL, err)
		}
	}
}

func TestCachedSongCanBeReusedAsFileAfterEngineRecreation(t *testing.T) {
	f := setup(t)
	native := &fakeTools{fakeNative: f.native}
	f.manager.native = native
	f.manager.config.Companion.Permissions.Remember = true
	access, _, e := f.manager.toolAccess()
	if e != nil {
		t.Fatal(e)
	}
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "one"}
	access.SetTrace(in)
	original := assetOriginal{Data: []byte("real original bytes retained separately from WAV"), MIME: "audio/mpeg", URL: "https://example.org/Artist%20-%20Song.mp3?private=removed"}
	result, e := access.stageOutputWithAsset(context.Background(), plugins.Output{Media: []plugins.MediaResult{{Kind: "voice", Data: []byte("converted WAV"), MIMEType: "audio/wav", Filename: "part.wav"}}}, &original)
	if e != nil {
		t.Fatal(e)
	}
	var output struct {
		Media []companion.GeneratedMedia `json:"media"`
	}
	if json.Unmarshal([]byte(result), &output) != nil || len(output.Media) != 1 || output.Media[0].AssetID == "" {
		t.Fatal(result)
	}
	again, _, _ := f.manager.toolAccess()
	again.SetTrace(in)
	result, e = again.Call(context.Background(), "media_reuse", json.RawMessage(`{"asset_id":"`+output.Media[0].AssetID+`","kind":"file"}`))
	if e != nil {
		t.Fatal(e)
	}
	if json.Unmarshal([]byte(result), &output) != nil || len(output.Media) != 1 {
		t.Fatal(result)
	}
	media, ok := again.Media(output.Media[0].ID)
	if !ok || media.Kind != "file" || media.SourceID != "source-one" || !bytes.Contains([]byte(result), []byte(`"network_downloads":0`)) {
		t.Fatal("source alias not available", result)
	}
	stored, _ := f.manager.loadAssets()
	if len(stored) != 1 || stored[0].URL != "https://example.org/Artist%20-%20Song.mp3" {
		t.Fatal("unsafe URL retention", stored)
	}
	// A different thread cannot list or reuse the source, even with its ID.
	in.Thread = "other@chatroom"
	again.SetTrace(in)
	if _, e = again.reuseAsset(context.Background(), stored[0].ID, "file"); e == nil {
		t.Fatal("cross-conversation source leak")
	}
	list, e := f.manager.recentAssets(in)
	if e != nil || len(list) != 0 {
		t.Fatal("cross-conversation list leak")
	}
	in.Thread = "filehelper"
	again.SetTrace(in)
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.SendFile = false
	f.manager.mu.Unlock()
	if _, e = again.reuseAsset(context.Background(), stored[0].ID, "file"); e == nil {
		t.Fatal("revoked permission ignored")
	}
	if native.submits.Load() != 0 {
		t.Fatal("preparing a reusable asset sent it")
	}
}
func TestLegacyVoiceSourceCanBecomeRealWAVFileWithoutNetwork(t *testing.T) {
	f := setup(t)
	f.manager.native = &fakeTools{fakeNative: f.native}
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper"}
	e := f.manager.tools.upload(stagedUpload{UploadSource: linuxnative.UploadSource{ID: "voice-source", Filename: "audio-part.wav", ContentType: "audio/wav", ByteLength: 123}, Kind: "voice", Account: "test"})
	if e != nil {
		t.Fatal(e)
	}
	a, e := f.manager.rememberLegacyAsset(in, "voice", "voice-source")
	if e != nil {
		t.Fatal(e)
	}
	tools, _, _ := f.manager.toolAccess()
	tools.SetTrace(in)
	result, e := tools.reuseAsset(context.Background(), a.ID, "file")
	if e != nil {
		t.Fatal(e)
	}
	media, ok := tools.Media(a.ID + "-file")
	if !ok || media.SourceID != "voice-source" || media.Title != "audio-part.wav" || !bytes.Contains([]byte(result), []byte(`"kind":"file"`)) {
		t.Fatal("legacy source lost")
	}
}
func TestDrainRoutesRequireAuthCSRFTicketAndDoNotCancelWork(t *testing.T) {
	f := setup(t)
	if code, _, _ := f.request("POST", "/api/runtime/drain", map[string]any{}, true, true); code != 401 {
		t.Fatal(code)
	}
	f.login()
	if code, _, _ := f.request("POST", "/api/runtime/drain", map[string]any{}, true, false); code != 403 {
		t.Fatal(code)
	}
	done := make(chan struct{})
	f.manager.mu.Lock()
	f.manager.done = done
	f.manager.mu.Unlock()
	code, b, _ := f.request("POST", "/api/runtime/drain", map[string]any{}, true, true)
	var ticket struct{ Ticket string }
	_ = json.Unmarshal(b, &ticket)
	if code != 200 || ticket.Ticket == "" {
		t.Fatal(code)
	}
	_, b, _ = f.request("GET", "/api/runtime/drain?ticket="+ticket.Ticket, nil, true, false)
	if !bytes.Contains(b, []byte(`"ready":false`)) {
		t.Fatal("did not wait")
	}
	close(done)
	_, b, _ = f.request("GET", "/api/runtime/drain?ticket="+ticket.Ticket, nil, true, false)
	if !bytes.Contains(b, []byte(`"ready":true`)) {
		t.Fatal("did not finish")
	}
	if code, _, _ := f.request("GET", "/api/runtime/drain?ticket=bad", nil, true, false); code != 409 {
		t.Fatal("ticket unbound")
	}
}

func TestAssetSynthesisScriptIsNotReattributedOnReuse(t *testing.T) {
	request := assetRequest{TraceID: "original", RequesterID: "alice"}
	asset := mediaAsset{SynthesisText: "cloth。fabric。", SynthesisTextKnown: true, FirstRequest: &request, Formats: map[string]assetFormat{"voice": {FullOriginal: true}}}
	input := companion.Input{TraceID: "original", Sender: "alice"}
	if text, ok := assetSynthesisForRequest(asset, input, "voice", nil); !ok || text != asset.SynthesisText {
		t.Fatal("exact original request lost verified script", text, ok)
	}
	input.Sender = "bob"
	if text, ok := assetSynthesisForRequest(asset, input, "voice", nil); ok || text != "" {
		t.Fatal("reused voice inherited another requester's script", text, ok)
	}
	input.Sender = "alice"
	if text, ok := assetSynthesisForRequest(asset, input, "voice", &voiceConversion{}); ok || text != "" {
		t.Fatal("converted voice inherited full-original script", text, ok)
	}
}
