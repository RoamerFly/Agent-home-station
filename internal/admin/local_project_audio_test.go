package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"wechat-bot/internal/companion"
)

func testStereoWAV(t *testing.T, sampleRate, frames int) []byte {
	t.Helper()
	if sampleRate < 1 || frames < 1 {
		t.Fatal("invalid synthetic WAV dimensions")
	}
	dataSize := frames * 4
	body := make([]byte, 44+dataSize)
	copy(body[:4], "RIFF")
	binary.LittleEndian.PutUint32(body[4:8], uint32(36+dataSize))
	copy(body[8:12], "WAVE")
	copy(body[12:16], "fmt ")
	binary.LittleEndian.PutUint32(body[16:20], 16)
	binary.LittleEndian.PutUint16(body[20:22], 1)
	binary.LittleEndian.PutUint16(body[22:24], 2)
	binary.LittleEndian.PutUint32(body[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(body[28:32], uint32(sampleRate*4))
	binary.LittleEndian.PutUint16(body[32:34], 4)
	binary.LittleEndian.PutUint16(body[34:36], 16)
	copy(body[36:40], "data")
	binary.LittleEndian.PutUint32(body[40:44], uint32(dataSize))
	for index := 0; index < frames; index++ {
		// Equal L/R samples make this invariant under the standard mono mixing choice.
		value := int16((index % 2048) - 1024)
		binary.LittleEndian.PutUint16(body[44+index*4:46+index*4], uint16(value))
		binary.LittleEndian.PutUint16(body[46+index*4:48+index*4], uint16(value))
	}
	return body
}

func localWAVPCM16(t *testing.T, body []byte) (sampleRate, channels, frames int, samples []int16) {
	t.Helper()
	if len(body) < 44 || string(body[:4]) != "RIFF" || string(body[8:12]) != "WAVE" || string(body[12:16]) != "fmt " || string(body[36:40]) != "data" {
		t.Fatalf("download is not a simple WAV: %q", body[:min(16, len(body))])
	}
	if binary.LittleEndian.Uint16(body[20:22]) != 1 || binary.LittleEndian.Uint16(body[34:36]) != 16 {
		t.Fatalf("output is not PCM 16-bit")
	}
	sampleRate, channels = int(binary.LittleEndian.Uint32(body[24:28])), int(binary.LittleEndian.Uint16(body[22:24]))
	dataBytes := int(binary.LittleEndian.Uint32(body[40:44]))
	if channels < 1 || len(body) != 44+dataBytes || dataBytes%(channels*2) != 0 {
		t.Fatalf("invalid WAV layout")
	}
	frames, samples = dataBytes/(channels*2), make([]int16, dataBytes/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(body[44+i*2 : 46+i*2]))
	}
	return
}

func projectArtifactIDs(turns []map[string]string) []string {
	all, seen, ids := localModelHistory(turns), map[string]bool{}, []string{}
	for offset := 0; ; {
		start := strings.Index(all[offset:], "work-")
		if start < 0 {
			return ids
		}
		start += offset
		if len(all) >= start+69 {
			candidate := all[start : start+69]
			if artifactID(candidate, "work-") && !seen[candidate] {
				seen[candidate] = true
				ids = append(ids, candidate)
			}
		}
		offset = start + len("work-")
	}
}

// TestLiveLocalProjectAudioPreparesRegisteredWAVThroughChat uses synthetic
// PCM only. The actual transform must run through the installed MCP + Windows
// LPAC path, then the test validates the immutable result through download.
func TestLiveLocalProjectAudioPreparesRegisteredWAVThroughChat(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("AHS_LOCAL_PROJECT_AUDIO_LPAC_TEST") != "1" {
		t.Skip("set AHS_LOCAL_PROJECT_AUDIO_LPAC_TEST=1 on Windows for audio LPAC acceptance")
	}
	options := localProjectToolWindowsOptionsFor(t, localAudioPackage)
	base := t.TempDir()
	f := newLocalHomeFixture(t, filepath.Join(base, "host"), filepath.Join(base, "workspace"))
	f.handler.chat.projectTools.options = options
	f.login()
	if code, out := f.request("POST", "/api/local/home", map[string]string{"action": "initialize"}, true); code != http.StatusOK {
		t.Fatalf("initialize: %d %#v", code, out)
	}

	before := 0
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, system string, _ []map[string]string) (string, error) {
			before++
			if strings.Contains(system, "project_audio_prepare") {
				t.Error("uninstalled audio tool disclosed")
			}
			return `{"action":"reply","messages":["准备音频。"]}`, nil
		}), nil
	}
	if code, out := localChatSend(f, "audio-before-install", "准备音频"); code != http.StatusOK || out["status"] != "completed" || before != 1 {
		t.Fatalf("pre-install chat: %d %#v", code, out)
	}

	initial := localProjectToolByID(t, f, localAudioPackage)
	if initial["catalog_available"] != true || initial["runtime_ready"] != true || initial["installed"] != false || initial["state"] != "not_installed" {
		t.Fatalf("unexpected audio state: %#v", initial)
	}
	const route = "/api/local/project-tools/audio-workshop"
	if code, _ := f.request("PUT", route, map[string]any{"action": "install"}, true); code != http.StatusBadRequest {
		t.Fatalf("installed without permission: %d", code)
	}
	if code, out := f.request("PUT", route, map[string]any{"action": "install", "approve_artifacts": true}, true); code != http.StatusOK {
		t.Fatalf("install audio tool: %d %#v", code, out)
	}
	installed := localProjectToolByID(t, f, localAudioPackage)
	if installed["installed"] != true || installed["enabled"] != false || installed["state"] != "disabled" {
		t.Fatalf("installed state: %#v", installed)
	}
	if code, out := f.request("PUT", route, map[string]any{"action": "enable", "revision": localProjectToolRevision(t, installed)}, true); code != http.StatusOK {
		t.Fatalf("enable audio tool: %d %#v", code, out)
	}
	if enabled := localProjectToolByID(t, f, localAudioPackage); enabled["enabled"] != true || enabled["state"] != "idle" {
		t.Fatalf("enabled state: %#v", enabled)
	}

	_, home := f.request("GET", "/api/local/home", nil, true)
	source := testStereoWAV(t, 8000, 8000)
	path := filepath.Join(home["directory"].(string), "Project Tools", "synthetic-stereo.wav")
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(source)
	_, registered := f.request("POST", "/api/local/artifacts", map[string]string{"path": "Project Tools/synthetic-stereo.wav", "expected_sha256": hex.EncodeToString(sum[:]), "request_key": "audio-source-fixture"}, true)
	inputID, _ := registered["artifact_id"].(string)
	if !artifactID(inputID, "work-") {
		t.Fatalf("source not registered: %#v", registered)
	}

	var outputID string
	rounds := 0
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, system string, turns []map[string]string) (string, error) {
			rounds++
			if rounds == 1 {
				if !strings.Contains(system, "project_audio_prepare") {
					t.Error("enabled audio tool absent")
				}
				return localToolCall("project_audio_prepare", map[string]any{"artifact_id": inputID, "filename": "prepared-mono.wav", "start_seconds": 0.25, "duration_seconds": 0.5, "channels": "mono"}), nil
			}
			for _, candidate := range projectArtifactIDs(turns) {
				if candidate != inputID {
					outputID = candidate
				}
			}
			return `{"action":"reply","messages":["音频已登记：` + outputID + `。"]}`, nil
		}), nil
	}
	if code, out := localChatSend(f, "audio-prepare", "截取中间部分并转单声道，保存成品"); code != http.StatusOK || out["status"] != "completed" {
		t.Fatalf("audio chat: %d %#v", code, out)
	}
	if rounds != 2 || !artifactID(outputID, "work-") || outputID == inputID {
		if host := f.handler.chat.projectTools.host; host != nil {
			if page, err := host.store.Jobs(t.Context(), localAudioPackage, 0, 10); err == nil {
				for _, job := range page.Items {
					t.Logf("audio job state=%s arguments=%s result=%s diagnostic=%+v", job.State, job.Arguments, job.Result, job.Diagnostic)
				}
			}
		}
		t.Fatalf("audio output missing: rounds=%d input=%q output=%q", rounds, inputID, outputID)
	}
	if code, view := f.request("GET", "/api/local/artifacts/"+outputID, nil, true); code != http.StatusOK || view["filename"] != "prepared-mono.wav" || view["input_artifact_id"] != inputID || view["input_sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("output provenance: %d %#v", code, view)
	}
	body := localArtifactDownload(t, f, outputID)
	sampleRate, channels, frames, samples := localWAVPCM16(t, body)
	if sampleRate != 8000 || channels != 1 || frames != 4000 || len(samples) != 4000 {
		t.Fatalf("output shape: rate=%d channels=%d frames=%d samples=%d", sampleRate, channels, frames, len(samples))
	}
	if samples[0] != int16(2000-1024) || samples[len(samples)-1] != int16((5999%2048)-1024) {
		t.Fatalf("output trim mismatch: first=%d last=%d", samples[0], samples[len(samples)-1])
	}

	current := localProjectToolByID(t, f, localAudioPackage)
	if code, out := f.request("PUT", route, map[string]any{"action": "disable", "revision": localProjectToolRevision(t, current)}, true); code != http.StatusOK {
		t.Fatalf("disable audio tool: %d %#v", code, out)
	}
	after := 0
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, system string, _ []map[string]string) (string, error) {
			after++
			if strings.Contains(system, "project_audio_prepare") {
				t.Error("disabled audio tool disclosed")
			}
			return `{"action":"reply","messages":["音频工具已停止。"]}`, nil
		}), nil
	}
	if code, out := localChatSend(f, "audio-after-disable", "继续处理音频"); code != http.StatusOK || out["status"] != "completed" || after != 1 {
		t.Fatalf("post-disable chat: %d %#v", code, out)
	}
	if view := localProjectToolByID(t, f, localAudioPackage); view["enabled"] != false || view["state"] != "disabled" {
		t.Fatalf("not disabled: %#v", view)
	}
	if again := localArtifactDownload(t, f, outputID); !bytes.Equal(body, again) {
		t.Fatal("disabling tool changed immutable WAV")
	}
}
