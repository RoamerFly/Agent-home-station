package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type conversionNative struct {
	*fakeTools
	parts [][]byte
}

func (n *conversionNative) Upload(_ context.Context, name, mime string, r io.Reader, size int64) (linuxnative.UploadSource, error) {
	b, err := io.ReadAll(r)
	n.parts = append(n.parts, b)
	return linuxnative.UploadSource{ID: fmt.Sprint("converted-", len(n.parts)), Filename: name, ContentType: mime, ByteLength: size}, err
}

func conversionPartDuration(t *testing.T, b []byte) float64 {
	t.Helper()
	file := filepath.Join(t.TempDir(), "part.wav")
	if err := os.WriteFile(file, b, 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", file).Output()
	seconds, parseErr := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || parseErr != nil {
		t.Fatal(err, parseErr)
	}
	return seconds
}

func TestVoiceConversionSelectsRangeCachesVariantAndPreservesFullBundle(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe required")
	}
	f := setup(t)
	n := &conversionNative{fakeTools: &fakeTools{fakeNative: f.native}}
	f.manager.native = n
	f.manager.config.Companion.Permissions.Remember = true
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "clip-request"}
	original := syntheticReferenceWAV(t, 100) // First70s silent, then a tone.
	a, err := f.manager.rememberAssetFormats(in, assetOriginal{VoiceComplete: true, Data: original, MIME: "audio/wav", Filename: "synthetic.wav"}, []stagedUpload{{UploadSource: linuxnative.UploadSource{ID: "full-1"}, Kind: "voice"}, {UploadSource: linuxnative.UploadSource{ID: "full-2"}, Kind: "voice"}})
	if err != nil {
		t.Fatal(err)
	}
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(in)
	raw := json.RawMessage(`{"asset_id":"` + a.ID + `","kind":"voice","conversion":{"start_seconds":80,"duration_seconds":12.5,"segment_seconds":5,"timeout_seconds":120}}`)
	result, err := p.Call(context.Background(), "media_reuse", raw)
	if err != nil || len(n.parts) != 3 || !strings.Contains(result, `"segment_seconds":5`) {
		t.Fatal(result, err, len(n.parts))
	}
	for i, want := range []float64{5, 5, 2.5} {
		if got := conversionPartDuration(t, n.parts[i]); math.Abs(got-want) > 0.002 {
			t.Fatal("wrong part duration", got, want)
		}
	}
	// Verify the start parameter selected the audible region rather than intro.
	if bytes.Count(n.parts[0], []byte{0}) > len(n.parts[0])*9/10 {
		t.Fatal("silent intro used instead of selected range")
	}
	if _, err = p.Call(context.Background(), "media_reuse", raw); err != nil || len(n.parts) != 3 {
		t.Fatal("variant uploaded again", err)
	}
	result, err = p.reuseAsset(context.Background(), a.ID, "voice")
	var out struct{ Media []companion.GeneratedMedia }
	if err != nil || json.Unmarshal([]byte(result), &out) != nil || len(out.Media) != 2 {
		t.Fatal(result, err)
	}
	for i, item := range out.Media {
		media, ok := p.Media(item.ID)
		if !ok || media.SourceID != fmt.Sprint("full-", i+1) {
			t.Fatal("clip replaced full bundle", media)
		}
	}
	items, err := f.manager.loadAssets()
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	got, err := f.manager.cachedAssetData(items[0])
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("original changed", err)
	}
	if n.submits.Load() != 0 {
		t.Fatal("preparation sent to WeChat")
	}
}

func TestCrossMediaConversionIdentityAndRecovery(t *testing.T) {
	f, p, old := forwardFixture(t)
	f.manager.config.Companion.Permissions.SendVoice = true
	n := &conversionNative{fakeTools: old.fakeTools}
	f.manager.native = n
	a, err := f.manager.rememberAssetFormats(p.trace, assetOriginal{Data: syntheticReferenceWAV(t, 100), MIME: "audio/wav", Filename: "synthetic.wav"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(key string, start float64) map[string]any {
		t.Helper()
		args, _ := json.Marshal(map[string]any{"asset_id": a.ID, "source_thread_id": p.trace.Thread, "target_thread_id": "source@chatroom", "kind": "voice", "request_key": key, "conversion": voiceConversion{StartSeconds: start, DurationSeconds: 10, SegmentSeconds: 5}})
		raw, e := p.Call(context.Background(), "wechat_send_media", args)
		var out map[string]any
		if e != nil || json.Unmarshal([]byte(raw), &out) != nil || out["parts"] != float64(2) {
			t.Fatal(raw, e)
		}
		return out
	}
	one := call("clip-1", 70)
	two := call("clip-2", 80)
	if one["operation_id"] == two["operation_id"] {
		t.Fatal("distinct audio ranges merged")
	}
	again := call("same-range-new-key", 80)
	if again["operation_id"] != two["operation_id"] || again["reused_existing_operation"] != true || len(f.manager.takeMediaSeeds()) != 4 {
		t.Fatal("duplicate variant queue", again)
	}
	f.manager.mediaSeeds = nil
	if err = os.Remove(filepath.Join(f.manager.dir, "media-recovery.json")); err != nil {
		t.Fatal(err)
	}
	if err = f.manager.restoreCrossMedia(); err != nil || len(f.manager.takeMediaSeeds()) != 4 {
		t.Fatal("restart lost variant batches", err)
	}
	if n.submits.Load() != 0 {
		t.Fatal("preparation submitted WeChat")
	}
}

func TestVoiceConversionReportsProtocolAndRangeBoundaries(t *testing.T) {
	for _, c := range []*voiceConversion{{SegmentSeconds: 61}, {StartSeconds: -1}, {TimeoutSeconds: math.Inf(1)}} {
		if err := c.validate("voice"); err == nil {
			t.Fatal("invalid conversion accepted", c)
		}
	}
	if err := (&voiceConversion{}).validate("file"); err == nil {
		t.Fatal("file conversion altered original contract")
	}
	data := syntheticReferenceWAV(t, 2)
	for _, c := range []voiceConversion{{StartSeconds: 3, SegmentSeconds: 60, TimeoutSeconds: 90}, {DurationSeconds: 3, SegmentSeconds: 60, TimeoutSeconds: 90}} {
		if _, err := preparePublicAudioRange(context.Background(), t.TempDir(), data, c); err == nil {
			t.Fatal("out of range silently clamped")
		}
	}
	if !sameVoiceConversion(nil, &voiceConversion{}) {
		t.Fatal("defaults produced distinct delivery identity")
	}
	if !sameVoiceConversion(&voiceConversion{TimeoutSeconds: 90}, &voiceConversion{TimeoutSeconds: 180}) {
		t.Fatal("budget changed output identity")
	}
}

func TestNonemptyLegacyVoiceBundleDoesNotTruncateCompleteOriginal(t *testing.T) {
	f := setup(t)
	n := &conversionNative{fakeTools: &fakeTools{fakeNative: f.native}}
	f.manager.native = n
	f.manager.config.Companion.Permissions.Remember = true
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "legacy-preview", Sender: "owner"}
	a, err := f.manager.rememberAssetFormats(in, assetOriginal{Data: syntheticReferenceWAV(t, 70), MIME: "audio/wav", Filename: "complete.wav"}, []stagedUpload{{UploadSource: linuxnative.UploadSource{ID: "old-first-60-seconds"}, Kind: "voice"}})
	if err != nil {
		t.Fatal(err)
	}
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(in)
	result, err := p.reuseAsset(context.Background(), a.ID, "voice")
	if err != nil || len(n.parts) != 2 || !strings.Contains(result, `"rebuilt_legacy_voice":true`) {
		t.Fatal(result, err, len(n.parts))
	}
	if math.Abs(conversionPartDuration(t, n.parts[0])-60) > 0.002 || math.Abs(conversionPartDuration(t, n.parts[1])-10) > 0.002 {
		t.Fatal("legacy preview reused")
	}
	items, err := f.manager.loadAssets()
	if err != nil || !items[0].Formats["voice"].FullOriginal || len(items[0].Formats["voice"].Sources) != 2 {
		t.Fatal(items, err)
	}
	if _, err = p.reuseAsset(context.Background(), a.ID, "voice"); err != nil || len(n.parts) != 2 {
		t.Fatal("verified full bundle rebuilt again", err)
	}
	if n.submits.Load() != 0 {
		t.Fatal("reconstruction sent WeChat")
	}
}

func TestCrossMediaOutOfRangeRejectsBeforeQueueAndAllowsCorrectedKey(t *testing.T) {
	f, p, old := forwardFixture(t)
	f.manager.config.Companion.Permissions.SendVoice = true
	n := &conversionNative{fakeTools: old.fakeTools}
	f.manager.native = n
	a, err := f.manager.rememberAssetFormats(p.trace, assetOriginal{Data: syntheticReferenceWAV(t, 2), MIME: "audio/wav", Filename: "short.wav"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(start float64) string {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"source_thread_id": p.trace.Thread, "target_thread_id": "source@chatroom", "asset_id": a.ID, "kind": "voice", "request_key": "correctable-range", "conversion": voiceConversion{StartSeconds: start}})
		r, e := p.Call(context.Background(), "wechat_send_media", raw)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	if r := call(3); !strings.Contains(r, `"state":"rejected"`) || len(f.manager.takeMediaSeeds()) != 0 || len(n.parts) != 0 {
		t.Fatal("invalid range became uncertain send", r)
	}
	if r := call(0); !strings.Contains(r, `"state":"queued"`) || len(f.manager.takeMediaSeeds()) != 1 {
		t.Fatal("corrected parameters blocked", r)
	}
}
