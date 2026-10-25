package admin

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"wechat-bot/internal/mcptools"
)

// Public MP4s commonly contain alternate audio/subtitle/data tracks. Native
// WeChat preparation accepts one video and at most one audio stream. Remux
// those streams locally without cutting duration or claiming to generate video.
func preparePublicVideo(ctx context.Context, dir string, data []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tmp, err := os.MkdirTemp(dir, "public-video-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	input, output := filepath.Join(tmp, "input.bin"), filepath.Join(tmp, "output.mp4")
	if err = os.WriteFile(input, data, 0600); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-nostdin", "-y", "-max_alloc", "67108864", "-protocol_whitelist", "file", "-format_whitelist", "mov,matroska,webm,avi,flv,mpeg,mpegts,ogg", "-i", input, "-map", "0:v:0", "-map", "0:a:0?", "-c", "copy", "-map_metadata", "-1", "-map_chapters", "-1", "-sn", "-dn", "-movflags", "+faststart", "-f", "mp4", output)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err = cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &mcptools.WebError{Code: "video_prepare_failed"}
	}
	f, err := os.Open(output)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	result, err := io.ReadAll(io.LimitReader(f, toolsLimit("video")+1))
	if err != nil {
		return nil, err
	}
	if len(result) == 0 || int64(len(result)) > toolsLimit("video") {
		return nil, &mcptools.WebError{Code: "response_too_large"}
	}
	return result, nil
}

func publicVideoDuration(ctx context.Context, dir string, data []byte) (float64, error) {
	tmp, err := os.MkdirTemp(dir, "public-video-duration-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tmp)
	file := filepath.Join(tmp, "input.bin")
	if err = os.WriteFile(file, data, 0600); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file", "-format_whitelist", "mov,matroska,webm,avi,flv,mpeg,mpegts,ogg", "-show_entries", "format=duration", "-of", "json", file).Output()
	if err != nil || len(result) > 65536 {
		return 0, &mcptools.WebError{Code: "video_prepare_failed"}
	}
	var probe struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if json.Unmarshal(result, &probe) != nil {
		return 0, &mcptools.WebError{Code: "video_prepare_failed"}
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(probe.Format.Duration), 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds > 24*3600 {
		return 0, &mcptools.WebError{Code: "video_duration_limit"}
	}
	return seconds, nil
}
