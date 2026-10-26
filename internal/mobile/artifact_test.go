package mobile

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

type artifactBridge struct {
	data    []byte
	a       Artifact
	calls   int
	corrupt bool
}

func (b *artifactBridge) Close() error { return nil }
func (b *artifactBridge) Call(_ context.Context, _ string, raw json.RawMessage) (string, error) {
	var in struct {
		Offset int `json:"offset"`
	}
	_ = json.Unmarshal(raw, &in)
	b.calls++
	end := min(in.Offset+8192, len(b.data))
	part := b.data[in.Offset:end]
	offset := in.Offset
	if b.corrupt {
		offset++
	}
	out, _ := json.Marshal(map[string]any{"id": b.a.ID, "offset": offset, "bytes": len(b.data), "sha256": b.a.SHA256, "data_base64": base64.StdEncoding.EncodeToString(part), "eof": end == len(b.data)})
	return string(out), nil
}
func TestRecordingChunksAreVerifiedAndScoped(t *testing.T) {
	c, _ := mobileFixture(t, nil)
	data := []byte("0000ftyp" + strings.Repeat("synthetic", 3000))
	sum := sha256.Sum256(data)
	task := Task{ID: "device-synthetic", DeviceID: "lab", State: "running"}
	c.tasks[task.ID] = task
	a := Artifact{ID: "clip-0123456789abcdef0123456789abcdef", DeviceID: "lab", RequestKey: "request", Kind: "screen_recording", MIME: "video/mp4", Bytes: len(data), SHA256: hex.EncodeToString(sum[:]), RequestedSeconds: 3, Summary: "untrusted", AssetID: "fake-asset"}
	b := &artifactBridge{data: data, a: a}
	if err := c.collectArtifact(context.Background(), b, task, a, "request"); err != nil {
		t.Fatal(err)
	}
	if b.calls < 2 {
		t.Fatal("not chunked")
	}
	got, actual, err := c.Artifact(task.ID, a.ID)
	if err != nil || string(actual) != string(data) || got.AssetID != "" || got.Summary == "untrusted" {
		t.Fatal("recording evidence", err)
	}
	if _, _, err = c.Artifact("other", a.ID); err == nil {
		t.Fatal("cross-task leak")
	}
	b.corrupt = true
	if err = c.collectArtifact(context.Background(), b, task, a, "request"); err == nil {
		t.Fatal("wrong chunk accepted")
	}
	a.Audio = true
	if err = c.collectArtifact(context.Background(), b, task, a, "request"); err == nil {
		t.Fatal("invented audio")
	}
}
