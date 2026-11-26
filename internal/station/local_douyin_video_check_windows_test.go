//go:build windows

package station

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"wechat-bot/internal/companion"
)

func TestDouyinVideoCheckUsesVerifiedArtifactAndReportsSampleOnly(t *testing.T) {
	f := localChatFixture(t)
	_, home := f.request("GET", "/api/local/home", nil, false)
	works := filepath.Join(home["directory"].(string), "works")
	withAudio, withoutAudio := syntheticDouyinAudioMedia(t, true), syntheticDouyinAudioMedia(t, false)
	for name, data := range map[string][]byte{"with-audio.mp4": withAudio, "without-audio.mp4": withoutAudio} {
		if err := os.WriteFile(filepath.Join(works, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	invoke := func(request, name string, frameIndex, audioStart, audioSeconds, budget int) (localDouyinVideoCheckResult, string) {
		t.Helper()
		var result localDouyinVideoCheckResult
		var artifact string
		calls := 0
		f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
			return localModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
				calls++
				switch calls {
				case 1:
					return localToolCall(localArtifactPublishTool, map[string]any{"path": "works/" + name}), nil
				case 2:
					ids := localPublicationWorkIDs(localModelHistory(turns))
					if len(ids) != 1 {
						t.Fatalf("registered artifact absent: %v", ids)
					}
					artifact = ids[0]
					return localToolCall(localDouyinVideoCheckToolName, map[string]any{
						"artifact_id": artifact, "frame_index": frameIndex,
						"audio_start_seconds": audioStart, "audio_sample_seconds": audioSeconds,
						"time_budget_seconds": budget,
					}), nil
				default:
					for i := len(turns) - 1; i >= 0; i-- {
						var envelope struct{ Tool, Result string }
						if json.Unmarshal([]byte(turns[i]["content"]), &envelope) == nil && envelope.Tool == localDouyinVideoCheckToolName {
							if err := json.Unmarshal([]byte(envelope.Result), &result); err != nil {
								t.Fatal(err)
							}
							break
						}
					}
					return `{"action":"reply","messages":["已核对成品的指定采样。"]}`, nil
				}
			}), nil
		}
		if code, out := localChatSend(f, request, "登记并只读检查合成视频"); code != 200 || out["status"] != "completed" {
			t.Fatalf("video check chat: %d %v", code, out)
		}
		if calls != 3 || result.ArtifactID != artifact || !artifactID(artifact, "work-") {
			t.Fatalf("Agent did not receive verified video check: calls=%d result=%+v", calls, result)
		}
		return result, artifact
	}
	audio, audioID := invoke("douyin-video-check-audio", "with-audio.mp4", 0, 0, 1, 90)
	if audio.State != "checked" || audio.Container != "mp4" || !audio.ContainerSignature ||
		audio.SHA256 != sha256Hex(withAudio) || audio.Bytes != int64(len(withAudio)) ||
		audio.VideoTrack != "present" || audio.Width != 64 || audio.Height != 48 || audio.DurationSeconds < 2.9 ||
		audio.DecodedFrameIndex == nil || *audio.DecodedFrameIndex != 0 || audio.FrameTimeSeconds == nil ||
		audio.AudioTrack != "decodable" || audio.AudioSampledSeconds < .9 ||
		audio.RequestedAudioSeconds != 1 || audio.TimeBudgetSeconds != 90 || !strings.Contains(audio.Coverage, "不证明全片") {
		t.Fatalf("audio-video sample overstated or incomplete: %+v", audio)
	}
	silent, silentID := invoke("douyin-video-check-silent", "without-audio.mp4", 5, 1, 2, 60)
	if silent.State != "checked" || silent.AudioTrack != "absent" || silent.AudioSampledSeconds != 0 ||
		silent.DecodedFrameIndex == nil || *silent.DecodedFrameIndex != 5 || silent.FrameTimeSeconds == nil ||
		*silent.FrameTimeSeconds < .5 || silent.RequestedAudioStart != 1 || silent.RequestedAudioSeconds != 2 ||
		silent.TimeBudgetSeconds != 60 {
		t.Fatalf("silent video or requested range was misstated: %+v", silent)
	}
	webmPath := filepath.Join(t.TempDir(), "generated.webm")
	webmCommand := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-y", "-f", "lavfi", "-i", "color=c=green:s=64x48:r=5:d=2",
		"-an", "-c:v", "libvpx", "-deadline", "realtime", "-cpu-used", "8", webmPath)
	webmCommand.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if output, err := webmCommand.CombinedOutput(); err != nil {
		t.Logf("optional WebM fixture encoder unavailable: %v %.100s", err, output)
	} else {
		webm, err := os.ReadFile(webmPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(works, "generated.webm"), webm, 0600); err != nil {
			t.Fatal(err)
		}
		checked, _ := invoke("douyin-video-check-webm", "generated.webm", 0, 0, 1, 90)
		if checked.State != "checked" || checked.Container != "webm" || !checked.ContainerSignature ||
			checked.SHA256 != sha256Hex(webm) || checked.VideoTrack != "present" || checked.AudioTrack != "absent" {
			t.Fatalf("WebM artifact was not verified: %+v", checked)
		}
	}
	// The tool never trusts a registry record without rehashing its immutable
	// original. A later local request cannot inspect a corrupted snapshot.
	if err := os.WriteFile(filepath.Join(f.dir, "artifacts", audioID, "original"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	tools := noticeToolSet(t, f, "douyin-video-check-tamper", true)
	raw, _ := json.Marshal(map[string]any{"artifact_id": audioID})
	if value, err := tools.Call(t.Context(), localDouyinVideoCheckToolName, raw); err == nil || value != "" {
		t.Fatalf("corrupt registered artifact reached decoder: %q %v", value, err)
	}
	wrongOwner := noticeToolSet(t, f, "douyin-video-check-wrong-owner", true)
	wrongOwner.access.ownerID = "another-owner"
	silentRaw, _ := json.Marshal(map[string]any{"artifact_id": silentID})
	if value, err := wrongOwner.Call(t.Context(), localDouyinVideoCheckToolName, silentRaw); err == nil || value != "" {
		t.Fatalf("another owner inspected the artifact: %q %v", value, err)
	}
	store, in, err := f.handler.chat.conversation()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(companion.ScopeID(in.Channel, in.Account, in.Thread)); err != nil {
		t.Fatal(err)
	}
	forgotten := noticeToolSet(t, f, "douyin-video-check-forgotten", true)
	if value, err := forgotten.Call(t.Context(), localDouyinVideoCheckToolName, silentRaw); err == nil || value != "" {
		t.Fatalf("forgotten artifact source remained readable: %q %v", value, err)
	}
}

func TestDouyinVideoCheckContainerSignatureAndArguments(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "video-header-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	webm := []byte{0x1a, 0x45, 0xdf, 0xa3, 0x9f, 0x42, 0x82, 0x84, 'w', 'e', 'b', 'm', 0, 0, 0, 0}
	if _, err := file.Write(webm); err != nil {
		t.Fatal(err)
	}
	if kind, ok := localDouyinVideoContainer(file, "sample.webm"); !ok || kind != "webm" {
		t.Fatalf("WebM EBML signature not recognized: %q %v", kind, ok)
	}
	if kind, ok := localDouyinVideoContainer(file, "sample.mp4"); ok || kind != "mp4" {
		t.Fatalf("EBML accepted as MP4: %q %v", kind, ok)
	}
	valid := "work-" + strings.Repeat("a", 64)
	for _, args := range []map[string]any{
		{"artifact_id": valid, "audio_sample_seconds": 0},
		{"artifact_id": valid, "audio_sample_seconds": 241},
		{"artifact_id": valid, "time_budget_seconds": 9},
		{"artifact_id": valid, "frame_index": -1},
		{"artifact_id": valid, "audio_start_seconds": -1},
	} {
		raw, _ := json.Marshal(args)
		if _, err := parseLocalDouyinVideoCheckInput(raw); err == nil {
			t.Fatalf("invalid range accepted: %v", args)
		}
	}
	raw, _ := json.Marshal(map[string]any{"artifact_id": valid})
	if in, err := parseLocalDouyinVideoCheckInput(raw); err != nil || in.AudioSampleSeconds != 1 || in.TimeBudgetSeconds != 90 {
		t.Fatalf("defaults changed: %+v %v", in, err)
	}
}

func TestDouyinVideoCheckProofRejectsForgottenOriginAfterToolReturn(t *testing.T) {
	f := localChatFixture(t)
	_, home := f.request("GET", "/api/local/home", nil, false)
	path := filepath.Join(home["directory"].(string), "works", "invalid.mp4")
	if err := os.WriteFile(path, []byte("synthetic invalid video container"), 0600); err != nil {
		t.Fatal(err)
	}
	const origin = "douyin-video-proof-origin"
	var artifact string
	calls := 0
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
			calls++
			if calls == 1 {
				return localToolCall(localArtifactPublishTool, map[string]any{"path": "works/invalid.mp4"}), nil
			}
			ids := localPublicationWorkIDs(localModelHistory(turns))
			if len(ids) != 1 {
				t.Fatalf("registered artifact absent: %v", ids)
			}
			artifact = ids[0]
			return `{"action":"reply","messages":["已登记。"]}`, nil
		}), nil
	}
	if code, out := localChatSend(f, origin, "登记合成视频成品"); code != 200 || out["status"] != "completed" || artifact == "" {
		t.Fatalf("source artifact registration: %d %v artifact=%q", code, out, artifact)
	}
	tools := noticeToolSet(t, f, "douyin-video-proof-inspection", true)
	raw, _ := json.Marshal(map[string]any{"artifact_id": artifact})
	value, err := tools.Call(t.Context(), localDouyinVideoCheckToolName, raw)
	if err != nil {
		t.Fatal(err)
	}
	var checked localDouyinVideoCheckResult
	if err := json.Unmarshal([]byte(value), &checked); err != nil {
		t.Fatal(err)
	}
	if checked.State != "invalid" || checked.StopReason != "container_signature_unrecognized" ||
		len(tools.douyinVideoCheckProofs) != 1 || !tools.RequestScopedMemory() {
		t.Fatalf("invalid media inspection had no final-answer proof: %+v %+v", checked, tools.douyinVideoCheckProofs)
	}
	if err := tools.ValidateGenerationSources(t.Context()); err != nil {
		t.Fatalf("valid origin failed the final-answer check: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(stationMemoryDir(f.handler.home.dataDir), tools.access.fishID, "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	updated, err := db.Exec("UPDATE events SET suppressed=1 WHERE trace=? AND role='user'", origin)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := updated.RowsAffected(); err != nil || count != 1 {
		t.Fatalf("expected one forgotten artifact source, got %d %v", count, err)
	}
	if err := tools.run.CheckSource(t.Context()); err != nil {
		t.Fatalf("inspection request unexpectedly lost source: %v", err)
	}
	if err := tools.ValidateGenerationSources(t.Context()); err == nil {
		t.Fatal("final reply could still use video metadata after its artifact source was forgotten")
	}
}

func TestDouyinVideoSnapshotPinsBytesAcrossOriginalMutation(t *testing.T) {
	original := bytes.Repeat([]byte("synthetic-video-bytes-"), 70000)
	sourcePath := filepath.Join(t.TempDir(), "original.mp4")
	source, err := os.OpenFile(sourcePath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.Write(original); err != nil {
		t.Fatal(err)
	}
	wantHash := sha256Hex(original)
	stableDir := t.TempDir()
	stable, err := localDouyinVideoSnapshot(t.Context(), source, stableDir, "work.mp4",
		int64(len(original)), wantHash, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	// Model a mutation after the store's first SHA check. The already copied
	// private snapshot remains the exact bytes the decoder will reopen.
	if _, err := source.WriteAt([]byte("changed"), 0); err != nil {
		t.Fatal(err)
	}
	if copied, err := os.ReadFile(stable); err != nil || !bytes.Equal(copied, original) {
		t.Fatal("private decoder snapshot changed with the original", err)
	}
	failureDir := t.TempDir()
	if path, err := localDouyinVideoSnapshot(t.Context(), source, failureDir, "work.mp4",
		int64(len(original)), wantHash, func() bool { return true }); err == nil || path != "" {
		t.Fatal("mutated original passed the second SHA check", path, err)
	}
	if files, err := os.ReadDir(failureDir); err != nil || len(files) != 0 {
		t.Fatal("failed copied snapshot was retained", files, err)
	}
	if _, err := source.WriteAt(original, 0); err != nil {
		t.Fatal(err)
	}
	authorized := 0
	if path, err := localDouyinVideoSnapshot(t.Context(), source, failureDir, "work.mp4",
		int64(len(original)), wantHash, func() bool {
			authorized++
			return authorized < 3
		}); err == nil || path != "" {
		t.Fatal("authorization revoked mid-copy left a usable snapshot", path, err)
	}
	if files, err := os.ReadDir(failureDir); err != nil || len(files) != 0 {
		t.Fatal("revoked partial snapshot was retained", files, err)
	}
}
