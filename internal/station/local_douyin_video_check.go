package station

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/mediadecode"
	"wechat-bot/internal/workspacefs"
)

const localDouyinVideoCheckToolName = "douyin_video_check"

func localDouyinVideoCheckTool() mcptools.Tool {
	return mcptools.NewTool(localDouyinVideoCheckToolName,
		"只读核验已登记的抖音视频成品。按 artifact_id 核对当前主人、原来源和 SHA-256，然后检查 MP4/WebM 容器签名、视频元数据、指定帧能否解码，以及指定区间音轨能否解码。默认第 0 帧和开头 1 秒音轨；frame_index、audio_start_seconds、audio_sample_seconds、time_budget_seconds 可调整。返回实际采样时间与覆盖，单帧/片段通过不表示全片可播放；不会修改成品或投稿。",
		`{"type":"object","properties":{"artifact_id":{"type":"string","pattern":"^work-[0-9a-f]{64}$"},"frame_index":{"type":"integer","minimum":0},"audio_start_seconds":{"type":"integer","minimum":0},"audio_sample_seconds":{"type":"integer","minimum":1,"maximum":240},"time_budget_seconds":{"type":"integer","minimum":10,"maximum":300}},"required":["artifact_id"],"additionalProperties":false}`)
}

type localDouyinVideoCheckInput struct {
	ArtifactID         string `json:"artifact_id"`
	FrameIndex         int    `json:"frame_index"`
	AudioStartSeconds  int    `json:"audio_start_seconds"`
	AudioSampleSeconds int    `json:"audio_sample_seconds"`
	TimeBudgetSeconds  int    `json:"time_budget_seconds"`
}

type localDouyinVideoCheckResult struct {
	ArtifactID            string   `json:"artifact_id"`
	SHA256                string   `json:"sha256"`
	Bytes                 int64    `json:"bytes"`
	State                 string   `json:"state"`
	StopReason            string   `json:"stop_reason,omitempty"`
	Container             string   `json:"container"`
	ContainerSignature    bool     `json:"container_signature_verified"`
	VideoTrack            string   `json:"video_track"`
	DurationSeconds       float64  `json:"duration_seconds,omitempty"`
	Width                 int      `json:"width,omitempty"`
	Height                int      `json:"height,omitempty"`
	FPS                   float64  `json:"fps,omitempty"`
	RequestedFrameIndex   int      `json:"requested_frame_index"`
	DecodedFrameIndex     *int     `json:"decoded_frame_index,omitempty"`
	FrameTimeSeconds      *float64 `json:"frame_time_seconds,omitempty"`
	RequestedAudioStart   int      `json:"requested_audio_start_seconds"`
	RequestedAudioSeconds int      `json:"requested_audio_sample_seconds"`
	AudioTrack            string   `json:"audio_track"`
	AudioSampledSeconds   float64  `json:"audio_sampled_seconds"`
	TimeBudgetSeconds     int      `json:"time_budget_seconds"`
	Coverage              string   `json:"coverage"`
}

// The model may answer after the tool call. Keep the exact artifact record and
// owner grant in the turn so a later forget, rebind or parent-lineage change
// invalidates every response derived from this private media check.
type localDouyinVideoCheckProof struct {
	Grant        localWorkspaceGrant
	ArtifactID   string
	SHA256       string
	Bytes        int64
	Fish, Owner  string
	RecordDigest string
}

func (t *localToolSet) checkDouyinVideoCheckProof(ctx context.Context, proof localDouyinVideoCheckProof) error {
	if t.run == nil || t.access == nil || t.access.home == nil || t.access.home.artifacts == nil ||
		t.run.CheckSource(ctx) != nil || !t.access.current(proof.Grant) {
		return errors.New("成品检查的请求来源或主人授权已变化")
	}
	current, err := t.access.home.artifacts.read(proof.ArtifactID)
	if err != nil || current.ID != proof.ArtifactID || current.SHA256 != proof.SHA256 ||
		current.Bytes != proof.Bytes || current.Fish != proof.Fish || current.Owner != proof.Owner ||
		localCheckpointKey("douyin-video-check-record-v1", current) != proof.RecordDigest {
		return errors.New("成品检查的原记录或来源已变化")
	}
	return nil
}

func (t *localToolSet) checkDouyinVideoCheckProofs(ctx context.Context) error {
	for _, proof := range t.douyinVideoCheckProofs {
		if err := t.checkDouyinVideoCheckProof(ctx, proof); err != nil {
			return err
		}
	}
	return nil
}

func parseLocalDouyinVideoCheckInput(raw json.RawMessage) (localDouyinVideoCheckInput, error) {
	var in localDouyinVideoCheckInput
	if strictLocalMemoryDecode(raw, &in) != nil || !artifactID(in.ArtifactID, "work-") {
		return in, mcptools.ArgumentError("$.artifact_id", "请提供已登记的视频成品编号")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return in, mcptools.ArgumentError("$", "视频检查参数无效")
	}
	if in.FrameIndex < 0 {
		return in, mcptools.ArgumentError("$.frame_index", "采样帧编号必须为非负整数")
	}
	if in.AudioStartSeconds < 0 {
		return in, mcptools.ArgumentError("$.audio_start_seconds", "音频起点必须为非负秒数")
	}
	if _, set := fields["audio_sample_seconds"]; set && (in.AudioSampleSeconds < 1 || in.AudioSampleSeconds > 240) {
		return in, mcptools.ArgumentError("$.audio_sample_seconds", "单次音频片段须为 1–240 秒；更长范围请分段检查")
	}
	if _, set := fields["time_budget_seconds"]; set && (in.TimeBudgetSeconds < 10 || in.TimeBudgetSeconds > 300) {
		return in, mcptools.ArgumentError("$.time_budget_seconds", "单次检查时间预算须为 10–300 秒")
	}
	if in.AudioSampleSeconds == 0 {
		in.AudioSampleSeconds = 1
	}
	if in.TimeBudgetSeconds == 0 {
		in.TimeBudgetSeconds = 90
	}
	return in, nil
}

func (t *localToolSet) callDouyinVideoCheck(ctx context.Context, raw json.RawMessage) (string, error) {
	if t.publicUseOnly || t.run == nil || !t.run.Tracked || t.access == nil || t.access.home == nil || t.access.home.artifacts == nil {
		return "", errors.New("本轮没有成品检查授权")
	}
	in, err := parseLocalDouyinVideoCheckInput(raw)
	if err != nil {
		return "", err
	}
	grant, err := t.access.resolve()
	if err != nil || t.run.CheckSource(ctx) != nil {
		return "", errors.New("当前成品检查来源或主人授权已变化")
	}
	store := t.access.home.artifacts
	artifact, err := store.read(in.ArtifactID)
	if err != nil || artifact.Fish != grant.FishID || artifact.Owner != grant.OwnerID {
		return "", errors.New("成品不属于当前主人或原来源已失效")
	}
	result := localDouyinVideoCheckResult{ArtifactID: artifact.ID, SHA256: artifact.SHA256, Bytes: artifact.Bytes,
		State: "unverified", VideoTrack: "unverified", AudioTrack: "unverified",
		RequestedFrameIndex: in.FrameIndex, RequestedAudioStart: in.AudioStartSeconds,
		RequestedAudioSeconds: in.AudioSampleSeconds, TimeBudgetSeconds: in.TimeBudgetSeconds,
		Coverage: "只检查指定的一帧及一段音频；不证明全片可播放或内容完整"}
	proof := localDouyinVideoCheckProof{Grant: grant, ArtifactID: artifact.ID, SHA256: artifact.SHA256,
		Bytes: artifact.Bytes, Fish: artifact.Fish, Owner: artifact.Owner,
		RecordDigest: localCheckpointKey("douyin-video-check-record-v1", artifact)}
	check := func() error { return t.checkDouyinVideoCheckProof(ctx, proof) }
	finish := func() (string, error) {
		if err := check(); err != nil {
			return "", err
		}
		value, err := localProcessJSON(result)
		if err != nil {
			return "", err
		}
		t.douyinVideoCheckProofs = append(t.douyinVideoCheckProofs, proof)
		return value, nil
	}
	if err := check(); err != nil {
		return "", err
	}
	workCtx, cancel := context.WithTimeout(ctx, time.Duration(in.TimeBudgetSeconds)*time.Second)
	defer cancel()
	file, err := store.open(workCtx, artifact, func() bool { return t.access.current(grant) })
	if err != nil {
		if errors.Is(workCtx.Err(), context.DeadlineExceeded) {
			if err := check(); err != nil {
				return "", err
			}
			return "", errors.New("成品哈希检查超时；可增大 time_budget_seconds 后重试")
		}
		return "", errors.New("成品 SHA-256、来源或读取授权无法核对")
	}
	defer file.Close()
	root := filepath.Join(stationApplicationsDir(t.access.home.dataDir), "douyin", "video-check")
	pinned, err := workspacefs.EnsurePath(root)
	if err != nil || pinned.Close() != nil {
		return "", errors.New("视频检查临时目录不可用")
	}
	temporary, err := os.MkdirTemp(root, "check-")
	if err != nil {
		return "", errors.New("视频检查临时目录不可用")
	}
	defer os.RemoveAll(temporary)
	// FFmpeg opens paths itself. Decode only a private copy whose copied bytes
	// were independently counted and hashed after the artifact store's initial
	// verification; the original may change between those two operations.
	snapshot, err := localDouyinVideoSnapshot(workCtx, file, temporary, artifact.Name,
		artifact.Bytes, artifact.SHA256, func() bool {
			return workCtx.Err() == nil && t.access.current(grant) && t.run.CheckSource(ctx) == nil
		})
	if err != nil {
		if errors.Is(workCtx.Err(), context.DeadlineExceeded) {
			if err := check(); err != nil {
				return "", err
			}
			return "", errors.New("成品快照核对超时；可增大 time_budget_seconds 后重试")
		}
		return "", errors.New("成品快照与原记录的字节或来源不一致")
	}
	if err := check(); err != nil {
		return "", err
	}
	snapshotFile, err := os.Open(snapshot)
	if err != nil {
		return "", errors.New("视频检查快照不可读")
	}
	result.Container, result.ContainerSignature = localDouyinVideoContainer(snapshotFile, artifact.Name)
	if err := snapshotFile.Close(); err != nil {
		return "", errors.New("视频检查快照不可读")
	}
	if !result.ContainerSignature {
		result.State, result.StopReason = "invalid", "container_signature_unrecognized"
		return finish()
	}
	if !mediadecode.Available() {
		result.State, result.StopReason = "unavailable", "local_decoder_unavailable"
		return finish()
	}
	metadata, err := mediadecode.ProbeMetadata(workCtx, snapshot)
	if err != nil {
		result.State, result.StopReason = "unverified", localDouyinVideoCheckReason(workCtx, "video_metadata_unavailable")
		return finish()
	}
	result.VideoTrack, result.DurationSeconds = "present", metadata.Seconds
	result.Width, result.Height, result.FPS = metadata.Width, metadata.Height, metadata.FPS
	if float64(in.AudioStartSeconds) >= metadata.Seconds {
		result.State, result.StopReason = "unverified", "audio_start_outside_video"
		return finish()
	}
	frames, times, err := mediadecode.ExtractFramesSized(workCtx, snapshot, temporary, in.FrameIndex, 1, 1, 64)
	if err != nil || len(frames) != 1 || len(times) != 1 {
		result.State, result.StopReason = "unverified", localDouyinVideoCheckReason(workCtx, "frame_decode_failed")
		return finish()
	}
	result.DecodedFrameIndex, result.FrameTimeSeconds = &in.FrameIndex, &times[0]
	audioPath := filepath.Join(temporary, "audio.wav")
	_, seconds, err := mediadecode.ExtractAudioRange(workCtx, snapshot, audioPath,
		float64(in.AudioStartSeconds), float64(in.AudioSampleSeconds))
	switch {
	case errors.Is(err, mediadecode.ErrNoAudioTrack):
		result.AudioTrack = "absent"
	case err == nil && seconds > 0:
		result.AudioTrack, result.AudioSampledSeconds = "decodable", seconds
	case errors.Is(err, mediadecode.ErrAudioRangeEmpty):
		result.AudioTrack, result.State, result.StopReason = "present_sample_empty", "unverified", "audio_sample_empty"
		return finish()
	default:
		result.State, result.StopReason = "unverified", localDouyinVideoCheckReason(workCtx, "audio_sample_unavailable")
		return finish()
	}
	result.State = "checked"
	return finish()
}

// The output exists only after it matches the original artifact's exact size
// and SHA-256. A failed or canceled copy leaves no file for the decoder.
func localDouyinVideoSnapshot(ctx context.Context, source *os.File, dir, name string,
	expectedBytes int64, expectedSHA string, allowed func() bool) (string, error) {
	if source == nil || allowed == nil || expectedBytes < 1 ||
		!artifactID("sha-"+expectedSHA, "sha-") || !allowed() {
		return "", errors.New("video_snapshot_source_invalid")
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return "", errors.New("video_snapshot_source_invalid")
	}
	target, err := os.CreateTemp(dir, "source-*"+strings.ToLower(filepath.Ext(name)))
	if err != nil {
		return "", errors.New("video_snapshot_staging_unavailable")
	}
	path := target.Name()
	completed := false
	defer func() {
		if !completed {
			_ = os.Remove(path)
		}
	}()
	hash := sha256.New()
	count, copyErr := io.CopyBuffer(attachmentGrantWriter{
		allow: func() bool { return ctx.Err() == nil && allowed() },
		out:   io.MultiWriter(target, hash),
	}, io.LimitReader(source, expectedBytes+1), make([]byte, 1<<20))
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil || count != expectedBytes ||
		hex.EncodeToString(hash.Sum(nil)) != expectedSHA || ctx.Err() != nil || !allowed() {
		return "", errors.New("video_snapshot_integrity_or_authorization_changed")
	}
	completed = true
	return path, nil
}

func localDouyinVideoCheckReason(ctx context.Context, fallback string) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "time_budget_exhausted"
	}
	return fallback
}

// A signature check is a cheap independent guard before handing verified
// artifact bytes to ffprobe. The decoder still checks the actual video stream.
func localDouyinVideoContainer(file *os.File, name string) (string, bool) {
	var header [4096]byte
	n, err := file.ReadAt(header[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "unknown", false
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4":
		return "mp4", n >= 12 && string(header[4:8]) == "ftyp"
	case ".webm":
		if n < 16 || !bytes.Equal(header[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) {
			return "webm", false
		}
		for i := 4; i+7 <= n; i++ {
			if header[i] == 0x42 && header[i+1] == 0x82 && header[i+2] == 0x84 && string(header[i+3:i+7]) == "webm" {
				return "webm", true
			}
		}
		return "webm", false
	default:
		return "unsupported", false
	}
}
