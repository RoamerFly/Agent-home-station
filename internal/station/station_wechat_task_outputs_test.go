package station

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/workspacefs"
)

func TestWechatTaskOutputsStayPrivateAndRejectCorruption(t *testing.T) {
	dir := t.TempDir()
	worker := &stationWechatWorker{dir: dir}
	root, err := workspacefs.EnsurePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
	items := []wechatTaskOutput{{Artifact: "work-" + strings.Repeat("a", 64), Account: "one", Thread: "room", Message: "source", Added: time.Now().UTC()}}
	writeRoot, err := workspacefs.OpenPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`[{"artifact_id":"work-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","account":"one","thread":"room","message":"source","added":"2026-11-12T00:00:00Z"}]`)
	if err := writeRoot.WriteFile(wechatTaskOutputsFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	writeRoot.Close()
	got, err := readWechatTaskOutputs(worker)
	if err != nil || len(got) != len(items) || got[0].Account != "one" || got[0].Thread != "room" {
		t.Fatalf("outputs: %+v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, wechatTaskOutputsFile)); err != nil {
		t.Fatal(err)
	}
	root, err = workspacefs.OpenPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile(wechatTaskOutputsFile, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	root.Close()
	if _, err := readWechatTaskOutputs(worker); err == nil {
		t.Fatal("corrupt bindings accepted")
	}
	if raw, err := os.ReadFile(filepath.Join(dir, wechatTaskOutputsFile)); err != nil || string(raw) != "corrupt" {
		t.Fatal("corrupt file changed")
	}
}
