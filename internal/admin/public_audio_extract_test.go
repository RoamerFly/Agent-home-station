package admin

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
)

func TestPublicAudioSixtySecondPartsPreserveActualTailWithoutPadding(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("FFmpeg unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("FFprobe unavailable")
	}
	for _, tc := range []struct {
		seconds int
		want    []int
	}{{60, []int{60}}, {61, []int{60, 1}}, {120, []int{60, 60}}, {140, []int{60, 60, 20}}} {
		t.Run(strconv.Itoa(tc.seconds), func(t *testing.T) {
			pcm := make([]byte, tc.seconds*16000*2)
			for i := 0; i < len(pcm)/2; i++ {
				binary.LittleEndian.PutUint16(pcm[i*2:], uint16(int16(math.Sin(float64(i)*440*2*math.Pi/16000)*5000)))
			}
			var input bytes.Buffer
			input.WriteString("RIFF")
			binary.Write(&input, binary.LittleEndian, uint32(len(pcm)+36))
			input.WriteString("WAVEfmt ")
			binary.Write(&input, binary.LittleEndian, uint32(16))
			binary.Write(&input, binary.LittleEndian, uint16(1))
			binary.Write(&input, binary.LittleEndian, uint16(1))
			binary.Write(&input, binary.LittleEndian, uint32(16000))
			binary.Write(&input, binary.LittleEndian, uint32(32000))
			binary.Write(&input, binary.LittleEndian, uint16(2))
			binary.Write(&input, binary.LittleEndian, uint16(16))
			input.WriteString("data")
			binary.Write(&input, binary.LittleEndian, uint32(len(pcm)))
			input.Write(pcm)
			out, err := preparePublicAudio(context.Background(), t.TempDir(), input.Bytes())
			if err != nil || len(out.Media) != len(tc.want) {
				t.Fatal(len(out.Media), err)
			}
			for i, m := range out.Media {
				frames := 0
				signal := false
				for pos := 12; pos+8 <= len(m.Data); {
					n := int(binary.LittleEndian.Uint32(m.Data[pos+4:]))
					start := pos + 8
					if start+n > len(m.Data) {
						t.Fatal("invalid WAV chunk")
					}
					if string(m.Data[pos:pos+4]) == "data" {
						frames = n / 2
						for j := start; j < start+n; j++ {
							signal = signal || m.Data[j] != 0
						}
					}
					pos = start + n + (n & 1)
				}
				if frames != tc.want[i]*16000 || !signal {
					t.Fatal("padded, empty or truncated part", i, frames, tc.want[i])
				}
			}
		})
	}
}

func TestPublicVideoAudioExtractionProducesReusableAudioOnlyFile(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("FFmpeg runtime unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("FFprobe runtime unavailable")
	}
	dir := t.TempDir()
	video := filepath.Join(dir, "synthetic.mp4")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=size=32x32:rate=5", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "2", "-c:v", "mpeg4", "-c:a", "aac", "-shortest", video).CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	b, err := os.ReadFile(video)
	if err != nil {
		t.Fatal(err)
	}
	audio, err := extractPublicAudioFile(context.Background(), dir, b)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "audio.m4a")
	if err = os.WriteFile(file, audio, 0600); err != nil {
		t.Fatal(err)
	}
	probe, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_type", "-of", "json", file).Output()
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Streams []struct {
			Kind string `json:"codec_type"`
		} `json:"streams"`
	}
	_ = json.Unmarshal(probe, &meta)
	if len(meta.Streams) != 1 || meta.Streams[0].Kind != "audio" {
		t.Fatal("cached music file still contains video", string(probe))
	}
	output, err := preparePublicAudio(context.Background(), dir, audio)
	if err != nil || len(output.Media) != 1 || output.Media[0].Kind != "voice" || len(output.Media[0].Data) < 44 {
		t.Fatal("extracted audio cannot become voice", err)
	}
}

func TestMusicPreviewRetainsCompleteOriginalForFileReuse(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("FFmpeg runtime unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("FFprobe runtime unavailable")
	}
	f := setup(t)
	// This fixture holds a complete 70-second PCM file, so declare a backend
	// that supports it rather than the generic 2 KiB upload mock.
	native := &streamingFileNative{fakeTools: &fakeTools{fakeNative: f.native}}
	f.manager.native = native
	f.manager.config.Companion.Permissions.Remember = true
	file := filepath.Join(t.TempDir(), "original.wav")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=16000", "-t", "70", "-c:a", "pcm_s16le", file).CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	full, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := preparePublicAudioPreview(context.Background(), f.dir, full, 45)
	if err != nil || len(preview.Media) != 1 || !strings.Contains(preview.Text, "原音频完整保留") {
		t.Fatal(preview.Text, err)
	}
	part := filepath.Join(t.TempDir(), "preview.wav")
	if err = os.WriteFile(part, preview.Media[0].Data, 0600); err != nil {
		t.Fatal(err)
	}
	probe, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", part).Output()
	seconds, _ := strconv.ParseFloat(strings.TrimSpace(string(probe)), 64)
	if err != nil || seconds < 44.9 || seconds > 45.1 {
		t.Fatal("preview length", seconds, err)
	}
	access, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	access.SetTrace(companion.Input{Channel: "isolated", Account: "test", Thread: "filehelper"})
	result, err := access.stageOutputWithAsset(context.Background(), preview, &assetOriginal{Data: full, MIME: "audio/wav", Filename: "whole-song.wav", URL: "https://example.org/whole-song.wav"})
	if err != nil {
		t.Fatal(err)
	}
	var prepared struct {
		Media []companion.GeneratedMedia `json:"media"`
	}
	if err = json.Unmarshal([]byte(result), &prepared); err != nil || len(prepared.Media) != 1 {
		t.Fatal(err, result)
	}
	assets, err := f.manager.loadAssets()
	if err != nil || len(assets) != 1 {
		t.Fatal(err)
	}
	cached, err := os.ReadFile(f.manager.assetPath(assets[0].Hash))
	if err != nil || !bytes.Equal(cached, full) || len(cached) <= len(preview.Media[0].Data) {
		t.Fatal("original replaced by preview", err)
	}
	result, err = access.reuseAsset(context.Background(), prepared.Media[0].AssetID, "file")
	if err != nil || !strings.Contains(result, `"kind":"file"`) || !strings.Contains(result, `"network_downloads":0`) || native.bytes != int64(len(full)) {
		t.Fatal(err, result)
	}
}
