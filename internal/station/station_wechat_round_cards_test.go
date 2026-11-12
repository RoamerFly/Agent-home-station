package station

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type roundCardNative struct {
	taskEvidenceNative
	sender *linuxnative.Sender
}

func (n roundCardNative) WorkerSender(context.Context, string) (*linuxnative.Sender, error) {
	return n.sender, nil
}

func TestRoundCardMessagePreviewHonorsHistoryGrantAndMemory(t *testing.T) {
	dir := t.TempDir()
	worker := &stationWechatWorker{dir: dir, policy: stationWechatPolicy{HistoryGrants: map[string]time.Time{}}}
	now := time.Now().UTC()
	message := linuxnative.Message{ID: "message", AccountID: "one", ThreadID: "group",
		SenderID: "member", ServerID: "server-message", Type: 1, Direction: "incoming",
		CreatedAt: now.Format(time.RFC3339Nano), Body: "synthetic received request"}
	reads := 0
	nativeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads++
		_ = json.NewEncoder(w).Encode(message)
	}))
	defer nativeServer.Close()
	token := filepath.Join(dir, "synthetic-token")
	if err := os.WriteFile(token, []byte("synthetic-round-token"), 0600); err != nil {
		t.Fatal(err)
	}
	native, err := linuxnative.New(linuxnative.Config{URL: nativeServer.URL, TokenFile: token, Account: "one"})
	if err != nil {
		t.Fatal(err)
	}
	memory, err := companion.NewStore(filepath.Join(dir, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	config := companion.Defaults()
	config.Permissions.Remember = true
	input := companion.Input{Channel: "wechat", Account: "one", Thread: "group", Sender: "member",
		TraceID: message.ID, Text: message.Body, MessageTime: message.CreatedAt,
		Source: &companion.SourceRef{MessageID: message.ID, ServerID: message.ServerID}}
	if err := memory.Observe(input, config, now); err != nil {
		t.Fatal(err)
	}
	if _, visible := memory.SourceForInput(input); !visible {
		t.Fatal("synthetic source was not recorded")
	}
	defer memory.Close()
	id := wechatapp.ConnectionID("one")
	if err := worker.recordRound(id, "one", now, bot.Record{Event: "message_received", ThreadID: "group", MessageID: message.ID, Category: "received"}); err != nil {
		t.Fatal(err)
	}
	host := &stationWechat{ctx: context.Background(), workers: map[string]*stationWechatWorker{id: worker},
		stationWechatNative: roundCardNative{taskEvidenceNative: taskEvidenceNative{connection: apphost.Connection{ID: id, AccountID: "one"}}, sender: native}}
	read := func(query string) string {
		request := httptest.NewRequest("GET", "/?"+query, nil)
		request.SetPathValue("connection", id)
		response := httptest.NewRecorder()
		host.readExecutionRounds(response, request)
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		return response.Body.String()
	}
	if strings.Contains(read("include_messages=true"), message.Body) || reads != 0 {
		t.Fatal("ungranted history was read")
	}
	worker.policy.HistoryGrants["group"] = now
	if strings.Contains(read(""), message.Body) || reads != 0 {
		t.Fatal("default journal read loaded original messages")
	}
	if result := read("include_messages=true"); !strings.Contains(result, message.Body) {
		t.Fatal("verified preview missing", result, reads)
	}
	message.Body = "synthetic changed message"
	if strings.Contains(read("include_messages=true"), message.Body) {
		t.Fatal("changed native body bypassed stored source")
	}
	message.Body = input.Text
	if err := memory.Clear(companion.ScopeID("wechat", "one", "group")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read("include_messages=true"), message.Body) {
		t.Fatal("forgotten message remained in round card")
	}
}
