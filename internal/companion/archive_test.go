package companion

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMemoryRetentionArchivesTextAndMediaWithoutReloadingModelContext(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if opened := s; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	if err != nil {
		t.Fatal(err)
	}
	c := Defaults()
	c.Permissions.Remember = true
	c.MemoryMessages = 2
	in := Input{Channel: "wechat", Account: "synthetic-account", Thread: "synthetic-room", Sender: "alice", TraceID: "one", Text: "旧消息"}
	now := time.Now()
	if err = s.Observe(in, c, now); err != nil {
		t.Fatal(err)
	}
	if err = s.ObserveMedia(in, c, MediaMemory{Key: "voice-key", Kind: "voice", Title: "合成歌", AssetID: "asset-synthetic", State: "accepted"}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	in.TraceID = "two"
	in.Text = "最新问题"
	if err = s.Observe(in, c, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = s.Accepted(in, "最新回复", c, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	id := ScopeID(in.Channel, in.Account, in.Thread)
	items, _, err := s.ArchiveList(id, "")
	if err != nil || len(items) != 2 {
		t.Fatal(items, err)
	}
	text, media := false, false
	for _, item := range items {
		scope, err := s.ArchiveRead(id, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, turn := range scope.Messages {
			text = text || turn.Text == "旧消息"
			media = media || turn.Media != nil && turn.Media.AssetID == "asset-synthetic"
		}
	}
	if !text || !media {
		t.Fatal("text/media association lost")
	}
	s, err = NewStore(dir)
	if opened := s; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	if err != nil {
		t.Fatal(err)
	}
	active := s.Context(in, c, now.Add(4*time.Second))
	if len(active.Messages) != 2 || active.Messages[0].Text != "最新问题" {
		t.Fatal("archived history leaked into model context", active)
	}
	if items, _, err = s.ArchiveList(id, ""); err != nil || len(items) != 2 {
		t.Fatal("restart lost archive", err)
	}
}
func TestMemoryArchiveFailureLeavesOriginalState(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	if opened := s; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	c := Defaults()
	c.Permissions.Remember = true
	c.MemoryMessages = 1
	in := Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "first", Text: "原始内容"}
	now := time.Now()
	if err := s.Observe(in, c, now); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "archive"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	in.TraceID = "second"
	in.Text = "新内容"
	if err = s.Observe(in, c, now.Add(time.Second)); err == nil {
		t.Fatal("archive failure ignored")
	}
	after, _ := os.ReadFile(s.path)
	if string(before) != string(after) {
		t.Fatal("original persisted state changed")
	}
	if scope := s.Context(in, c, now); len(scope.Messages) != 1 || scope.Messages[0].Text != "原始内容" {
		t.Fatal(scope)
	}
}
func TestClearActiveMemoryAlsoClearsCompressedSummary(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	if opened := s; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	c := Defaults()
	in := Input{Channel: "wechat", Account: "test", Thread: "filehelper", Text: "synthetic"}
	now := time.Now()
	s.Observe(in, c, now)
	id := ScopeID(in.Channel, in.Account, in.Thread)
	s.transaction(func(data *persisted) error {
		v := data.Scopes[id]
		v.Summary = "old compressed content"
		v.CompressedCount = 1
		data.Scopes[id] = v
		return nil
	})
	if err := s.Clear(id); err != nil {
		t.Fatal(err)
	}
	v := s.Context(in, c, now)
	if v.Summary != "" || v.CompressedCount != 0 || len(v.Messages) != 0 {
		t.Fatal("clear left model summary", v)
	}
}
