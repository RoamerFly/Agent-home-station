package admin

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/plugins"
)

func TestMediaLineageRetainsVideoParentAndRequestAcrossFormatReuse(t *testing.T) {
	f := setup(t)
	native := &fakeTools{fakeNative: f.native}
	f.manager.native = native
	f.manager.config.Companion.Permissions.Remember = true
	p, _, _ := f.manager.toolAccess()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "room@chatroom", Sender: "alice", SenderName: "小甲", TraceID: "delivery-1", OriginTraceID: "request-1", RequestedText: "把那个视频唱给我听", Text: "[内部交付继续]", TaskID: "task-1"}
	p.SetTrace(in)
	video := &assetOriginal{Data: []byte("synthetic video with images and sound"), MIME: "video/mp4", URL: "https://www.bilibili.com/video/BV1425hzpEtn/?p=2", Filename: "猫咪原视频.mp4"}
	audio := &assetOriginal{Data: []byte("synthetic extracted audio"), MIME: "audio/mp4", URL: video.URL, Filename: "猫咪音轨.m4a", Parent: video}
	result, err := p.stageOutputWithAsset(context.Background(), plugins.Output{Media: []plugins.MediaResult{{Kind: "voice", Data: []byte("synthetic converted voice"), MIMEType: "audio/wav"}}}, audio)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Media []companion.GeneratedMedia `json:"media"`
	}
	if json.Unmarshal([]byte(result), &output) != nil || len(output.Media) != 1 {
		t.Fatal(result)
	}
	m := output.Media[0]
	if m.AssetID == "" || m.ParentAssetID == "" || m.AssetID == m.ParentAssetID || m.CachedMIME != "audio/mp4" {
		t.Fatal(m)
	}
	assets, err := f.manager.loadAssets()
	if err != nil || len(assets) != 2 {
		t.Fatal(assets, err)
	}
	for _, a := range assets {
		if len(a.Requests) != 1 || a.Requests[0].TraceID != "request-1" || a.Requests[0].RequesterID != "alice" || a.Requests[0].Text != in.RequestedText {
			t.Fatal(a)
		}
	}
	// A different participant may explicitly request the same group resource,
	// but the original request remains attributed to Alice.
	in.TraceID, in.OriginTraceID, in.TaskID, in.Sender, in.Text = "request-bob", "", "", "bob", "把刚才小甲那份原视频文件发我"
	p, _, _ = f.manager.toolAccess()
	p.SetTrace(in)
	f.manager.config.Companion.Permissions.SendVideo = true
	missing, err := p.reuseAsset(context.Background(), m.AssetID, "video")
	if err != nil || !strings.Contains(missing, `"parent_asset_id":"`+m.ParentAssetID+`"`) || !strings.Contains(missing, `"requested_format_unavailable":true`) {
		t.Fatal("conversion source hidden", missing, err)
	}
	missing, err = p.reuseAsset(context.Background(), m.ParentAssetID, "video")
	if err != nil || !strings.Contains(missing, `"file_available":true`) {
		t.Fatal("cached original treated as requiring network", missing, err)
	}
	result, err = p.reuseAsset(context.Background(), m.ParentAssetID, "file")
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal([]byte(result), &output) != nil || len(output.Media) != 1 || output.Media[0].CachedMIME != "video/mp4" || !strings.HasSuffix(output.Media[0].Title, ".mp4") || output.Media[0].URL != video.URL {
		t.Fatal(result)
	}
	assets, _ = f.manager.loadAssets()
	for _, a := range assets {
		if a.ID == m.ParentAssetID && (len(a.Requests) != 2 || a.Requests[0].RequesterID != "alice" || a.Requests[1].RequesterID != "bob") {
			t.Fatal(a)
		}
	}
	list, err := f.manager.recentAssets(in)
	if err != nil || len(list) != 2 {
		t.Fatal(list, err)
	}
	in.Thread = "other-room@chatroom"
	p.SetTrace(in)
	if _, err := p.reuseAsset(context.Background(), m.ParentAssetID, "file"); err == nil {
		t.Fatal("parent escaped conversation scope")
	}
	if native.submits.Load() != 0 {
		t.Fatal("lineage or reuse sent WeChat")
	}
}

type capturedVoiceNative struct {
	*fakeTools
	voice []byte
	parts [][]byte
}

func (n *capturedVoiceNative) Upload(ctx context.Context, name, mime string, r io.Reader, size int64) (linuxnative.UploadSource, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return linuxnative.UploadSource{}, err
	}
	n.voice = data
	n.parts = append(n.parts, data)
	return linuxnative.UploadSource{ID: "rebuilt-source", Filename: name, ContentType: mime, ByteLength: int64(len(data))}, nil
}

func TestLegacyVoiceIndexRebuildsFullCachedAudioInsteadOfLastPart(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("FFmpeg unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("FFprobe unavailable")
	}
	f := setup(t)
	native := &capturedVoiceNative{fakeTools: &fakeTools{fakeNative: f.native}}
	f.manager.native = native
	f.manager.config.Companion.Permissions.Remember = true
	file := filepath.Join(t.TempDir(), "original.wav")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=16000", "-t", "70", "-c:a", "pcm_s16le", file).CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "old-song", Sender: "alice"}
	a, err := f.manager.rememberAsset(in, assetOriginal{Data: data, MIME: "audio/wav", Filename: "完整歌曲.wav"}, "voice", linuxnative.UploadSource{ID: "last-part-only"})
	if err != nil {
		t.Fatal(err)
	}
	a.Formats = nil // Before this upgrade only Sources[voice] existed.
	if err := f.manager.saveAssets([]mediaAsset{a}); err != nil {
		t.Fatal(err)
	}
	p, _, _ := f.manager.toolAccess()
	in.TraceID = "resend"
	p.SetTrace(in)
	result, err := p.reuseAsset(context.Background(), a.ID, "voice")
	if err != nil || !strings.Contains(result, `"rebuilt_legacy_voice":true`) || !strings.Contains(result, `"network_downloads":0`) {
		t.Fatal(result, err)
	}
	if len(native.voice) == 0 {
		t.Fatal("old partial source reused without rebuilding")
	}
	seconds := float64(0)
	for _, part := range native.parts {
		if err := os.WriteFile(file, part, 0600); err != nil {
			t.Fatal(err)
		}
		probe, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", file).Output()
		length, _ := strconv.ParseFloat(strings.TrimSpace(string(probe)), 64)
		if err != nil || length <= 0 || length > 60.001 {
			t.Fatal("invalid voice segment", length, err)
		}
		seconds += length
	}
	if seconds < 69.9 || seconds > 70.1 || len(native.parts) != 2 {
		t.Fatal("legacy source truncated or reordered", seconds, len(native.parts))
	}
	if native.submits.Load() != 0 {
		t.Fatal("rebuilding sent WeChat")
	}
}

func TestReuseWholeVoiceBundleAfterRecreationAndNewPreparation(t *testing.T) {
	f := setup(t)
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "first", Sender: "alice"}
	original := assetOriginal{Data: []byte("synthetic full original song"), MIME: "audio/mpeg", Filename: "完整歌曲.mp3"}
	parts := []stagedUpload{}
	for _, id := range []string{"part-1", "part-2", "part-3"} {
		parts = append(parts, stagedUpload{UploadSource: linuxnative.UploadSource{ID: id}, Kind: "voice"})
	}
	a, err := f.manager.rememberAssetFormats(in, original, parts)
	if err != nil {
		t.Fatal(err)
	}
	check := func(want []string) {
		t.Helper()
		p, _, _ := f.manager.toolAccess()
		p.SetTrace(in)
		raw, err := p.reuseAsset(context.Background(), a.ID, "voice")
		var out struct {
			Media []companion.GeneratedMedia `json:"media"`
		}
		if err != nil || json.Unmarshal([]byte(raw), &out) != nil || len(out.Media) != len(want) {
			t.Fatal(raw, err)
		}
		for i, item := range out.Media {
			actual, ok := p.Media(item.ID)
			if !ok || actual.SourceID != want[i] {
				t.Fatal("truncated/reordered bundle", actual, want)
			}
		}
	}
	check([]string{"part-1", "part-2", "part-3"})
	// A new preparation replaces that format's bundle atomically in the index;
	// it must not concatenate old and new upload materials.
	in.TraceID = "second"
	parts = []stagedUpload{}
	for _, id := range []string{"new-1", "new-2"} {
		parts = append(parts, stagedUpload{UploadSource: linuxnative.UploadSource{ID: id}, Kind: "voice"})
	}
	if _, err := f.manager.rememberAssetFormats(in, original, parts); err != nil {
		t.Fatal(err)
	}
	check([]string{"new-1", "new-2"})
}

func TestMediaCatalogueFiltersOldRequesterAssetWithinModelBudget(t *testing.T) {
	f := setup(t)
	f.manager.config.Companion.Permissions.Remember = true
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "room@chatroom", Sender: "alice", TraceID: "old"}
	old, err := f.manager.rememberAsset(in, assetOriginal{Data: []byte("old requested audio"), MIME: "audio/mpeg", Filename: "晚风.mp3"}, "voice", linuxnative.UploadSource{ID: "old"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 18; i++ {
		in.Sender, in.TraceID = "bob", strconv.Itoa(i)
		if _, err := f.manager.rememberAsset(in, assetOriginal{Data: []byte("new video " + strconv.Itoa(i)), MIME: "video/mp4", Filename: "新视频" + strconv.Itoa(i) + ".mp4"}, "video", linuxnative.UploadSource{ID: "new-" + strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	p, _, _ := f.manager.toolAccess()
	in.Sender = "alice"
	p.SetTrace(in)
	for _, raw := range []string{`{"requester_id":"self"}`, `{"asset_id":"` + old.ID + `"}`, `{"query":"晚风"}`} {
		result, err := p.Call(context.Background(), "media_recent", json.RawMessage(raw))
		var rows []map[string]any
		if err != nil || json.Unmarshal([]byte(result), &rows) != nil || len(rows) != 1 || rows[0]["asset_id"] != old.ID {
			t.Fatal(result, err)
		}
	}
	all, err := f.manager.recentAssets(in)
	b, _ := json.Marshal(all)
	if err != nil || !json.Valid(b) || len(b) > 5400 {
		t.Fatal("catalogue would be truncated in model context", len(b), err)
	}
	in.Thread = "other@chatroom"
	p.SetTrace(in)
	result, err := p.Call(context.Background(), "media_recent", json.RawMessage(`{"asset_id":"`+old.ID+`"}`))
	if err != nil || result != "[]" {
		t.Fatal("filtered catalogue escaped scope", result, err)
	}
}
