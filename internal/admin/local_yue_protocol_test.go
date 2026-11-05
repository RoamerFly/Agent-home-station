package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/comfyui"
	"wechat-bot/internal/mediadecode"
	"wechat-bot/internal/stationyue"
)

// The owned Windows service has an empty model directory. Live YuE2 node
// metadata exercises the real recipe limits, and built-in audio nodes verify
// the complete reference upload and FLAC evidence path without inference.
func TestLocalYuENativeWindowsProtocol(t *testing.T) {
	root := t.TempDir()
	data, workflows := filepath.Join(root, "data"), filepath.Join(root, "workflows")
	if err := os.MkdirAll(workflows, 0700); err != nil {
		t.Fatal(err)
	}
	service := configureLocalComfyPortable(t, data, workflows)
	defer service.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	deployment, err := service.Info(ctx, "dafeiyu")
	if err != nil {
		t.Fatal(err)
	}
	decoder, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal("fixture requires the local FFmpeg decoder", err)
	}
	decoder, err = filepath.Abs(decoder)
	if err != nil {
		t.Fatal(err)
	}
	const sampleRate, frames = 8000, 8000
	reference := syntheticMonoWAV(t, sampleRate, frames)
	ref, err := comfyui.NewJobRef(strings.Repeat("7", 64), "10")
	if err != nil {
		t.Fatal(err)
	}
	err = service.Use(ctx, "dafeiyu", deployment.Binding, true, func(client localComfyClient) error {
		audio := client.(localComfyAudioClient)
		metadata, err := audio.YuERequiredNodeSchemas(ctx)
		if err != nil {
			return err
		}
		basename, err := audio.UploadWAV(ctx, ref, reference)
		if err != nil {
			return err
		}
		compileNativeYuESmoke(t, metadata, basename, sha256Hex(reference))
		graph, err := json.Marshal(map[string]any{
			"1": map[string]any{"class_type": "LoadAudio", "inputs": map[string]any{"audio": basename}},
			"10": map[string]any{"class_type": "SaveAudioAdvanced", "inputs": map[string]any{
				"audio": []any{"1", 0}, "filename_prefix": "ignored", "format": "flac",
			}},
		})
		if err != nil {
			return err
		}
		job, err := comfyui.PrepareFLAC(graph, sha256Hex(graph), ref)
		if err != nil {
			return err
		}
		if err := client.Submit(ctx, job); err != nil {
			return err
		}
		if err := waitForYuEAudio(ctx, audio, ref); err != nil {
			return err
		}
		output, err := audio.FetchFLAC(ctx, ref)
		if err != nil {
			return err
		}
		path := filepath.Join(root, "synthetic-song.flac")
		if err := os.WriteFile(path, output.Data, 0600); err != nil {
			return err
		}
		receipt, err := mediadecode.VerifyFLAC(ctx, path, mediadecode.FLACVerificationOptions{
			FFmpegPath: decoder, MaxInputBytes: 2 << 20, MaxDecodedPCMBytes: 1 << 20,
			MaxSampleRate: 48000, MaxChannels: 2,
		})
		if err != nil {
			return err
		}
		if receipt.SHA256 != output.SHA256 || receipt.SampleRate != sampleRate || receipt.DecodedFrames != frames || receipt.Channels != 1 {
			return errors.New("verified audio differs from the synthetic source")
		}
		precision := 1 / float64(receipt.SampleRate)
		quality, err := stationyue.ValidateDuration(receipt.Duration.Seconds(), 1, .99, &precision)
		if err != nil || !quality.Accepted {
			return errors.New("verified audio duration differs from the synthetic source")
		}
		t.Logf("owned native audio: %d Hz, %d frames, %d channel, matching SHA-256", receipt.SampleRate, receipt.DecodedFrames, receipt.Channels)
		return client.MarkSettled(ctx, ref)
	})
	if err != nil {
		logPath := filepath.Join(data, "station-comfyui-logs", localComfyFishHash("dafeiyu")+".log")
		if logBytes, logErr := os.ReadFile(logPath); logErr == nil {
			t.Logf("owned synthetic fixture log tail:\n%s", logBytes[max(0, len(logBytes)-4000):])
		}
		t.Fatal(err)
	}
}

func compileNativeYuESmoke(t *testing.T, metadata json.RawMessage, basename, referenceHash string) {
	t.Helper()
	workflowDir, err := filepath.Abs(filepath.Join("..", "..", "components", "yue2", "app", "workflows_api"))
	if err != nil {
		t.Fatal(err)
	}
	pins := map[stationyue.Mode]string{}
	for mode, filename := range map[stationyue.Mode]string{
		"original": "audio_yue2_text_to_music.json", "cover": "audio_yue2_music_cover.json",
	} {
		raw, err := os.ReadFile(filepath.Join(workflowDir, filename))
		if err != nil {
			t.Fatal(err)
		}
		pins[mode] = sha256Hex(raw)
	}
	compiler := stationyue.Compiler{WorkflowDir: workflowDir, PinnedTemplateSHA256: pins, ObjectInfo: metadata}
	request := stationyue.Request{Mode: "original", Style: "acoustic folk", Lyrics: "[verse]\nA quiet test.", DurationSeconds: 18, Seed: 42, PlanningMode: "melody"}
	original, err := compiler.Compile(request)
	if err != nil {
		t.Fatal("native original recipe:", err)
	}
	if original.SourceSHA256 != pins["original"] || sha256Hex(original.Graph) != original.EffectiveSHA256 {
		t.Fatal("original workflow digest does not match")
	}
	request.Mode, request.PlanningMode = "cover", "reference"
	request.ReferenceBasename, request.ReferenceSHA256 = basename, referenceHash
	cover, err := compiler.Compile(request)
	if err != nil {
		t.Fatal("native cover recipe:", err)
	}
	if cover.SourceSHA256 != pins["cover"] || sha256Hex(cover.Graph) != cover.EffectiveSHA256 || !strings.Contains(string(cover.Graph), basename) {
		t.Fatal("cover workflow digest or original reference does not match")
	}
	t.Log("compiled original and cover templates with live native YuE2 node metadata")
}

func waitForYuEAudio(ctx context.Context, client localComfyAudioClient, ref comfyui.JobRef) error {
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := client.AudioStatus(ctx, ref)
		if err != nil {
			return err
		}
		switch state.State {
		case comfyui.Completed:
			return nil
		case comfyui.Running, comfyui.Pending:
		default:
			return fmt.Errorf("native audio fixture did not complete: %s", state.State)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
