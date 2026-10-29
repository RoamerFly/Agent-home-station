package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type incomingFileEventSource struct {
	*forwardNative
	initialRead bool
}

func (s *incomingFileEventSource) Events(_ context.Context, after string) (linuxnative.EventPage, error) {
	// The first poll establishes an empty startup tail. Only then does a new
	// native event arrive, independently of any management file-read request.
	if !s.initialRead {
		s.initialRead = true
		return linuxnative.EventPage{NextCursor: "0"}, nil
	}
	page := linuxnative.EventPage{NextCursor: after}
	if after == "0" {
		e := linuxnative.Event{Cursor: "1", Kind: "message.committed"}
		e.Data.MessageID, e.Data.ThreadID, e.Data.Direction, e.Data.Origin = s.message.ID, s.message.ThreadID, "incoming", "remote"
		page.Items, page.NextCursor = []linuxnative.Event{e}, "1"
	}
	return page, nil
}

type incomingFileModel func(context.Context, string, []map[string]string) (string, error)

func (m incomingFileModel) Chat(ctx context.Context, system string, turns []map[string]string) (string, error) {
	return m(ctx, system, turns)
}

func TestIncomingFileEventAutomaticallyCachesOriginalWithVisionDisabled(t *testing.T) {
	f, _, n := forwardFixture(t)
	subtype := 6
	n.message.Type, n.message.AppMessageType, n.message.Body = 49, &subtype, ""
	n.message.ThreadID, n.message.SenderID = "wxid_sender", "wxid_sender"
	n.message.Direction, n.message.CreatedAt = "incoming", time.Now().UTC().Format(time.RFC3339)
	n.message.Attachments = []linuxnative.Attachment{{Kind: "file", Filename: "notes.txt", State: "identified", MessageID: n.message.ID, Bytes: int64(len("synthetic attachment bytes"))}}
	c := f.manager.Config().Companion
	c.Enabled, c.Permissions.Remember = true, true
	c.QuietStart, c.QuietEnd = "00:00", "00:00"
	c.Conversation = companion.ConversationDefaults()
	c.Conversation.AggregateMS = 0
	modelCalls := 0
	model := incomingFileModel(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
		modelCalls++
		current := turns[len(turns)-1]["content"]
		if !strings.Contains(current, "收到文件原件") || !strings.Contains(current, "asset_id") || strings.Contains(current, "收到了一条分享卡片") {
			t.Error("automatic model input lost its received original")
		}
		return `{"action":"silent"}`, nil
	})
	engine, err := companion.New(c, f.manager.memory, model, "synthetic file-event acceptance")
	if err != nil {
		t.Fatal(err)
	}
	o := bot.DefaultOptions()
	o.ReadImages, o.ReadVideos, o.RandomDelay, o.RecoverBacklog = false, false, false, false
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	settled := false
	w, err := bot.New(bot.Config{StateDir: filepath.Join(f.dir, "event-worker"), AllowedThreads: []string{n.message.ThreadID}, PollInterval: time.Millisecond, Options: &o, Analyzer: mediaUnderstanding{manager: f.manager}, Record: func(record bot.Record) {
		f.manager.record(record)
		if record.MessageID == n.message.ID && record.Status == "silent" {
			settled = true
			cancel()
		}
	}}, &incomingFileEventSource{forwardNative: n}, engine, n)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !settled || modelCalls != 1 || n.downloads != 1 || n.submits.Load() != 0 {
		t.Fatal("event did not settle once without sending", settled, modelCalls, n.downloads, n.submits.Load())
	}
	assets, err := f.manager.loadAssets()
	if err != nil || len(assets) != 1 || assets[0].FirstRequest == nil || assets[0].FirstRequest.TraceID != n.message.ID || assets[0].FirstRequest.MessageTime != n.message.CreatedAt {
		t.Fatal("automatic reception lost provenance", err)
	}
	data, err := f.manager.cachedAssetData(assets[0])
	if err != nil || string(data) != "synthetic attachment bytes" {
		t.Fatal("automatic cache differs from original", err)
	}
	inbox := filepath.Join(f.manager.privateWorkspaceFor(n.message.AccountID), "conversations", assets[0].Scope, "inbox", agentID(n.message.ID)[:24]+".txt")
	data, err = os.ReadFile(inbox)
	sum := sha256.Sum256(data)
	if err != nil || hex.EncodeToString(sum[:]) != assets[0].Hash {
		t.Fatal("automatic inbox differs from cache", err)
	}
	var state struct {
		Inbox []struct {
			Phase string `json:"phase"`
		} `json:"inbox"`
	}
	data, err = os.ReadFile(filepath.Join(f.dir, "event-worker", "messages-v2.json"))
	if err != nil || json.Unmarshal(data, &state) != nil || len(state.Inbox) != 1 || state.Inbox[0].Phase != "silent" {
		t.Fatal("model decision was not persisted", err)
	}
}
