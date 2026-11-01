package admin

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"wechat-bot/internal/pluginhost"
)

func listenSyntheticWAV(seconds int) []byte {
	return listenSyntheticWAVDuration(float64(seconds))
}

func listenSyntheticWAVDuration(seconds float64) []byte {
	const rate = 16000
	data := make([]byte, int(seconds*rate)*2)
	wav := make([]byte, 44+len(data))
	copy(wav[:4], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:8], uint32(len(wav)-8))
	copy(wav[8:12], "WAVE")
	copy(wav[12:16], "fmt ")
	binary.LittleEndian.PutUint32(wav[16:20], 16)
	binary.LittleEndian.PutUint16(wav[20:22], 1)
	binary.LittleEndian.PutUint16(wav[22:24], 1)
	binary.LittleEndian.PutUint32(wav[24:28], rate)
	binary.LittleEndian.PutUint32(wav[28:32], rate*2)
	binary.LittleEndian.PutUint16(wav[32:34], 2)
	binary.LittleEndian.PutUint16(wav[34:36], 16)
	copy(wav[36:40], "data")
	binary.LittleEndian.PutUint32(wav[40:44], uint32(len(data)))
	copy(wav[44:], data)
	return wav
}

func TestMediaListenArgumentsBindExactlyOneOrigin(t *testing.T) {
	for _, raw := range []string{
		`{"source_message_id":"voice","asset_id":"asset"}`,
		`{"source_message_id":"voice","path":"c:/secret.wav"}`,
		`{"asset_id":"asset","seconds":0,"offset_seconds":0}`,
		`{"asset_id":"asset","timeout_seconds":-1}`,
	} {
		if _, err := decodeMediaServiceArguments(json.RawMessage(raw)); err == nil {
			t.Fatalf("unsafe listen arguments accepted: %s", raw)
		}
	}
	in, err := decodeMediaServiceArguments(json.RawMessage(`{"asset_id":"asset","offset_seconds":12,"seconds":121,"max_tokens":640,"timeout_seconds":90}`))
	if err != nil || in.AssetID != "asset" || in.Seconds != 121 {
		t.Fatalf("valid listen origin rejected: %#v %v", in, err)
	}
}

func TestMediaListenPromptUsesUnicodeRuneLimit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prompt string
		valid  bool
	}{
		{name: "three_thousand_chinese", prompt: strings.Repeat("听", 3000), valid: true},
		{name: "exact_limit", prompt: strings.Repeat("听", 8192), valid: true},
		{name: "over_limit", prompt: strings.Repeat("听", 8193)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"source_message_id": "voice", "prompt": tc.prompt})
			_, err := decodeMediaServiceArguments(raw)
			if (err == nil) != tc.valid {
				t.Fatalf("Unicode prompt acceptance=%v err=%v", err == nil, err)
			}
		})
	}
}

func TestMediaListenWAVDurationUsesActualDataChunk(t *testing.T) {
	wav := listenSyntheticWAV(91)
	d, err := wavDuration(wav)
	if err != nil || d != 91 {
		t.Fatalf("unexpected WAV duration: %v %v", d, err)
	}
	if _, err := wavDuration(wav[:43]); err == nil {
		t.Fatal("truncated WAV accepted")
	}
}

func TestMediaListenClipUsesActualWindowAndNeverExceedsBatchLimit(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	clip, seconds, err := mediaListenClip(context.Background(), t.TempDir(), listenSyntheticWAV(95), 89, mediaListenBatchSeconds)
	if err != nil || seconds <= 0 || seconds > 6.1 || len(clip) > 3<<20 {
		t.Fatalf("end-of-audio window was not measured from actual WAV: seconds=%v bytes=%d err=%v", seconds, len(clip), err)
	}
}

func TestMediaListenFractionalTailAlwaysRequestsARealFinalWindow(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	for _, end := range []float64{90.5, 180.5} {
		wav := listenSyntheticWAVDuration(end)
		start := 0
		batches := 0
		processed := 0.0
		for float64(start) < end {
			want := mediaListenBatchLength(end, start)
			if want < 1 || want > mediaListenBatchSeconds {
				t.Fatalf("end %v produced invalid final request %d at %d", end, want, start)
			}
			clip, covered, err := mediaListenClip(context.Background(), t.TempDir(), wav, start, want)
			if err != nil || len(clip) == 0 || covered <= 0 || covered > float64(want)+.1 {
				t.Fatalf("end %v clip failed at %d: covered=%v bytes=%d err=%v", end, start, covered, len(clip), err)
			}
			processed += covered
			// A normalized WAV clip may end fractionally before its requested
			// second. The next integer cursor must still move beyond that tail.
			next := start + int(math.Ceil(covered))
			if next <= start {
				t.Fatalf("end %v made no progress at %d", end, start)
			}
			start, batches = next, batches+1
		}
		if batches != int(math.Ceil(end/float64(mediaListenBatchSeconds))) {
			t.Fatalf("end %v produced %d batches", end, batches)
		}
		if processed < end-.1 {
			t.Fatalf("end %v left an unobserved fractional tail: %v", end, processed)
		}
	}
}

func TestMediaListenDiscoveryNeverExposesPrivateAudioReference(t *testing.T) {
	cap := mediaServiceCapabilityView(pluginhost.Capability{ID: "media.listen", InputSchema: json.RawMessage(`{"type":"object"}`)})
	if !json.Valid(cap.InputSchema) || !containsAll(string(cap.InputSchema), "source_message_id", "asset_id", "timeout_seconds") || containsAll(string(cap.InputSchema), "media_ref") {
		t.Fatalf("unsafe public listen schema: %s", cap.InputSchema)
	}
}

func TestMediaListenDoesNotExposeEarlierBatchesAfterAuthorityRevocation(t *testing.T) {
	f := setup(t)
	p := &permittedTools{manager: f.manager}
	revoked := false
	check := func(context.Context) error {
		if revoked {
			return errors.New("source_revoked")
		}
		return nil
	}
	calls := 0
	observe := func(_ context.Context, _ []byte, _ pluginhost.Invocation, _ int, _ string, _ int, _ int) (mediaAudioObservation, func(context.Context) error, error) {
		calls++
		revoked = true
		return mediaAudioObservation{Status: "observed", Text: "first private batch", Model: "synthetic", Sources: []mediaAudioReference{{Ref: "audio_0", SHA: "synthetic", Version: 1, MIME: "audio/wav"}}, VoiceEvidence: "model_opinion_only"}, func(context.Context) error { return check(context.Background()) }, nil
	}
	inv := pluginhost.Invocation{Account: "test", Thread: "filehelper", Requester: "member", Purpose: "listen", RequestKey: "listen-revoke", Authorization: "proof", SourceVersions: map[string]int{"source": 1}}
	out, err := p.listenAudioBatchesWithObserver(t.Context(), listenSyntheticWAV(181), inv, check, mediaServiceArguments{Seconds: 181}, observe)
	if err == nil || out != nil || calls != 1 {
		t.Fatalf("revoked second batch leaked an earlier result: out=%#v calls=%d err=%v", out, calls, err)
	}
}

func TestMediaListenAfterEndStillChecksOrigin(t *testing.T) {
	f := setup(t)
	p := &permittedTools{manager: f.manager}
	called := 0
	check := func(context.Context) error { called++; return errors.New("source_revoked") }
	inv := pluginhost.Invocation{Account: "test", Thread: "filehelper", Requester: "member", Purpose: "listen", RequestKey: "listen-after-end", Authorization: "proof", SourceVersions: map[string]int{"source": 1}}
	out, err := p.listenAudioBatchesWithObserver(t.Context(), listenSyntheticWAV(1), inv, check, mediaServiceArguments{OffsetSeconds: 9, Seconds: 1}, func(context.Context, []byte, pluginhost.Invocation, int, string, int, int) (mediaAudioObservation, func(context.Context) error, error) {
		t.Fatal("observer was called for an out-of-range request")
		return mediaAudioObservation{}, nil, nil
	})
	if err == nil || out != nil || called == 0 {
		t.Fatalf("after-end response bypassed source authorization: out=%#v calls=%d err=%v", out, called, err)
	}
}

func longCompressedListenFixture(t *testing.T, seconds int) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	path := filepath.Join(t.TempDir(), "long-silence.mp3")
	if err := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-y", "-f", "lavfi", "-i", "anullsrc=r=8000:cl=mono", "-t", strconv.Itoa(seconds), "-c:a", "libmp3lame", "-b:a", "8k", path).Run(); err != nil {
		t.Skipf("cannot create synthetic compressed audio: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 || len(data) > 25<<20 {
		t.Fatal("invalid synthetic compressed fixture", err, len(data))
	}
	return data
}

func TestMediaListenLongCompressedAssetDecodesOnlyRequestedWindows(t *testing.T) {
	f := setup(t)
	p := &permittedTools{manager: f.manager}
	duration, clipper, cleanup, err := mediaListenAssetClipper(t.Context(), f.dir, longCompressedListenFixture(t, 451))
	if err != nil || duration < 450 {
		t.Fatalf("long compressed asset was not probeable: duration=%v err=%v", duration, err)
	}
	defer cleanup()
	calls := 0
	check := func(context.Context) error { return nil }
	observe := func(_ context.Context, wav []byte, _ pluginhost.Invocation, _ int, _ string, _ int, _ int) (mediaAudioObservation, func(context.Context) error, error) {
		calls++
		if len(wav) > 3<<20 {
			t.Fatal("whole long asset was materialized as one WAV")
		}
		return mediaAudioObservation{Status: "observed", Text: "synthetic", Model: "synthetic", Sources: []mediaAudioReference{{Ref: "audio_0", SHA: "synthetic", Version: 1, MIME: "audio/wav"}}, VoiceEvidence: "model_opinion_only"}, func(context.Context) error { return nil }, nil
	}
	inv := pluginhost.Invocation{Account: "test", Thread: "filehelper", Requester: "member", Purpose: "listen", RequestKey: "long-compressed", Authorization: "proof", SourceVersions: map[string]int{"asset": 1}}
	out, err := p.listenAudioBatchesFromSource(t.Context(), duration, clipper, inv, check, mediaServiceArguments{OffsetSeconds: 0, Seconds: 451}, observe)
	if err != nil || calls < 5 || out["status"] == "partial" {
		t.Fatalf("long request did not complete in real windows: calls=%d out=%#v err=%v", calls, out, err)
	}
	coverage := out["coverage"].(mediaListenCoverage)
	if coverage.ProcessedSeconds < 450 || coverage.ActualDuration < 450 {
		t.Fatalf("long coverage was fabricated: %#v", coverage)
	}
	// Explicitly ask beyond the actual end: the response must disclose the
	// endpoint instead of claiming all requested seconds were present.
	out, err = p.listenAudioBatchesFromSource(t.Context(), duration, clipper, inv, check, mediaServiceArguments{OffsetSeconds: 420, Seconds: 90}, observe)
	if err != nil || out["coverage"].(mediaListenCoverage).Reason != "audio_ended_before_requested_range" {
		t.Fatalf("end-of-asset coverage was not explicit: %#v %v", out, err)
	}
}

func TestMediaListenLongCompressedAssetRevocationLeaksNoPriorBatch(t *testing.T) {
	f := setup(t)
	p := &permittedTools{manager: f.manager}
	duration, clipper, cleanup, err := mediaListenAssetClipper(t.Context(), f.dir, longCompressedListenFixture(t, 451))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	revoked, calls := false, 0
	check := func(context.Context) error {
		if revoked {
			return errors.New("asset_revoked")
		}
		return nil
	}
	observe := func(context.Context, []byte, pluginhost.Invocation, int, string, int, int) (mediaAudioObservation, func(context.Context) error, error) {
		calls++
		revoked = true
		return mediaAudioObservation{Status: "observed", Text: "must not leak", Model: "synthetic", Sources: []mediaAudioReference{{Ref: "audio_0", SHA: "synthetic", Version: 1, MIME: "audio/wav"}}, VoiceEvidence: "model_opinion_only"}, func(context.Context) error { return check(context.Background()) }, nil
	}
	inv := pluginhost.Invocation{Account: "test", Thread: "filehelper", Requester: "member", Purpose: "listen", RequestKey: "long-revoked", Authorization: "proof", SourceVersions: map[string]int{"asset": 1}}
	out, err := p.listenAudioBatchesFromSource(t.Context(), duration, clipper, inv, check, mediaServiceArguments{Seconds: 451}, observe)
	if err == nil || out != nil || calls != 1 {
		t.Fatalf("revoked long asset exposed prior batch: out=%#v calls=%d err=%v", out, calls, err)
	}
}

func TestMediaListenUntrustedDurationDoesNotPreallocateWholeRange(t *testing.T) {
	p := &permittedTools{}
	const duration = 68719476735
	clipCalls := 0
	result, err := p.listenAudioBatchesFromSource(t.Context(), duration,
		func(context.Context, int, int) ([]byte, float64, error) {
			clipCalls++
			return nil, 0, errors.New("invalid_audio_data")
		}, pluginhost.Invocation{}, func(context.Context) error { return nil },
		mediaServiceArguments{Seconds: duration},
		func(context.Context, []byte, pluginhost.Invocation, int, string, int, int) (mediaAudioObservation, func(context.Context) error, error) {
			t.Fatal("invalid audio reached observer")
			return mediaAudioObservation{}, nil, nil
		})
	if err != nil || clipCalls != 1 || result["status"] != "partial" {
		t.Fatalf("invalid long source did not return resumable partial: %v %#v calls=%d", err, result, clipCalls)
	}
	coverage := result["coverage"].(mediaListenCoverage)
	if coverage.ProcessedSeconds != 0 || coverage.NextStartSeconds == nil || *coverage.NextStartSeconds != 0 || coverage.Reason != "audio_clip_failed" {
		t.Fatalf("invalid source invented coverage: %#v", coverage)
	}
}

func containsAll(s string, values ...string) bool {
	for _, value := range values {
		if !strings.Contains(s, value) {
			return false
		}
	}
	return true
}
