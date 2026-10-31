package mobile

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func (c *Control) collectArtifact(ctx context.Context, b Bridge, t Task, a Artifact, requestKey string) error {
	if !artifactPattern.MatchString(a.ID) || a.DeviceID != t.DeviceID || a.RequestKey != requestKey || a.Kind != "screen_recording" || a.MIME != "video/mp4" || a.Audio || a.Original || a.Bytes < 32 || a.Bytes > 2<<20 || a.RequestedSeconds < 2 || a.RequestedSeconds > 12 {
		return errors.New("device_recording_invalid")
	}
	data := make([]byte, 0, a.Bytes)
	for len(data) < a.Bytes {
		if !c.Allowed(t.Source) {
			return errPermissionChanged
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		args, _ := json.Marshal(map[string]any{"device_id": t.DeviceID, "artifact_id": a.ID, "request_key": requestKey, "offset": len(data)})
		value, err := b.Call(ctx, "read_artifact", args)
		if err != nil {
			return err
		}
		var chunk struct {
			ID     string `json:"id"`
			Offset int    `json:"offset"`
			Bytes  int    `json:"bytes"`
			SHA    string `json:"sha256"`
			Data   string `json:"data_base64"`
			EOF    bool   `json:"eof"`
		}
		if len(value) > 16000 || json.Unmarshal([]byte(value), &chunk) != nil || chunk.ID != a.ID || chunk.Offset != len(data) || chunk.Bytes != a.Bytes || chunk.SHA != a.SHA256 {
			return errors.New("device_recording_chunk_invalid")
		}
		part, err := base64.StdEncoding.DecodeString(chunk.Data)
		if err != nil || len(part) == 0 || len(part) > 8192 || len(data)+len(part) > a.Bytes || chunk.EOF != (len(data)+len(part) == a.Bytes) {
			return errors.New("device_recording_chunk_invalid")
		}
		data = append(data, part...)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != a.SHA256 || string(data[4:8]) != "ftyp" {
		return errors.New("device_recording_integrity_invalid")
	}
	// A bridge call can revoke the requester on its final chunk. Do not write
	// the recording or expose it to an analyzer after that point.
	if !c.Allowed(t.Source) {
		return errPermissionChanged
	}
	dir := filepath.Join(c.dir, "recordings")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, a.ID+".mp4"), data, 0600); err != nil {
		return err
	}
	// Never accept model-supplied summary or asset IDs from bridge metadata.
	a.Summary = "实际屏幕录制；没有音轨，尚未分析画面；不是原视频下载"
	a.AssetID = ""
	var analysisErr error
	if c.analyze != nil {
		if !c.Allowed(t.Source) {
			return errPermissionChanged
		}
		summary, assetID, err := c.analyze(ctx, t, a, data)
		if err == nil {
			a.Summary, a.AssetID = summary, assetID
		} else {
			analysisErr = errors.New("device_recording_analysis_unavailable")
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.allowed(c.settings, t.Source) {
		return errPermissionChanged
	}
	old := clone(c.tasks[t.ID])
	current := clone(old)
	if current.State == "cancelled" {
		return context.Canceled
	}
	if len(current.Artifacts) >= 10 {
		return errors.New("device_recording_limit")
	}
	current.Artifacts = append(current.Artifacts, a)
	c.tasks[t.ID] = current
	if err := c.persistTasks(); err != nil {
		c.tasks[t.ID] = old
		return err
	}
	return analysisErr
}
func (c *Control) Artifact(taskID, artifactID string) (Artifact, []byte, error) {
	t, ok := c.Get(taskID)
	if ok && artifactPattern.MatchString(artifactID) {
		for _, a := range t.Artifacts {
			if a.ID == artifactID {
				data, err := os.ReadFile(filepath.Join(c.dir, "recordings", a.ID+".mp4"))
				return a, data, err
			}
		}
	}
	return Artifact{}, nil, errors.New("device_recording_not_found")
}
