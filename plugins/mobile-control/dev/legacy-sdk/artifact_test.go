package mobile

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

type artifactBridge struct {
	data      []byte
	a         Artifact
	calls     int
	corrupt   bool
	afterCall func(int)
}

func (b *artifactBridge) Close() error { return nil }
func (b *artifactBridge) Call(_ context.Context, _ string, raw json.RawMessage) (string, error) {
	var in struct {
		Offset int `json:"offset"`
	}
	_ = json.Unmarshal(raw, &in)
	b.calls++
	if b.afterCall != nil {
		b.afterCall(b.calls)
	}
	end := min(in.Offset+8192, len(b.data))
	part := b.data[in.Offset:end]
	offset := in.Offset
	if b.corrupt {
		offset++
	}
	out, _ := json.Marshal(map[string]any{"id": b.a.ID, "offset": offset, "bytes": len(b.data), "sha256": b.a.SHA256, "data_base64": base64.StdEncoding.EncodeToString(part), "eof": end == len(b.data)})
	return string(out), nil
}

func TestRecordingRevokedAfterFinalChunkIsNeitherStoredNorAnalyzed(t *testing.T) {
	c, _ := mobileFixture(t, nil)
	var allowed atomic.Bool
	allowed.Store(true)
	c.SetAuthorizer(func(s Source) bool { return s.Admin && allowed.Load() })
	data := []byte("0000ftyp" + strings.Repeat("synthetic", 1000))
	sum := sha256.Sum256(data)
	task := Task{ID: "device-revoked-recording", DeviceID: "lab", State: "running", Source: Source{Admin: true, MessageID: "artifact-revoked"}}
	c.tasks[task.ID] = task
	a := Artifact{ID: "clip-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DeviceID: "lab", RequestKey: "request", Kind: "screen_recording", MIME: "video/mp4", Bytes: len(data), SHA256: hex.EncodeToString(sum[:]), RequestedSeconds: 3}
	b := &artifactBridge{data: data, a: a}
	b.afterCall = func(call int) {
		if call == 2 { // final chunk; this must still block storage/analysis.
			allowed.Store(false)
		}
	}
	var analyzed atomic.Bool
	c.SetArtifactAnalyzer(func(context.Context, Task, Artifact, []byte) (string, string, error) {
		analyzed.Store(true)
		return "must not run", "", nil
	})
	if err := c.collectArtifact(context.Background(), b, task, a, "request"); !errors.Is(err, errPermissionChanged) {
		t.Fatalf("collectArtifact error = %v, want permission change", err)
	}
	if analyzed.Load() {
		t.Fatal("revoked recording was passed to analyzer")
	}
	if _, _, err := c.Artifact(task.ID, a.ID); err == nil {
		t.Fatal("revoked recording was persisted")
	}
}
func TestRecordingChunksAreVerifiedAndScoped(t *testing.T) {
	c, _ := mobileFixture(t, nil)
	data := []byte("0000ftyp" + strings.Repeat("synthetic", 3000))
	sum := sha256.Sum256(data)
	task := Task{ID: "device-synthetic", DeviceID: "lab", State: "running", Source: Source{Admin: true, MessageID: "artifact-test"}}
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
