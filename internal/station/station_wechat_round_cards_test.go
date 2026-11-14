package station

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	sender         *linuxnative.Sender
	accountChanged *atomic.Bool
}

func (n roundCardNative) Inspect(ctx context.Context, id string) (apphost.ConnectionStatus, error) {
	status, err := n.taskEvidenceNative.Inspect(ctx, id)
	if n.accountChanged != nil && n.accountChanged.Load() {
		status.AccountID = "different-account"
	}
	return status, err
}

func (n roundCardNative) WorkerSender(context.Context, string) (*linuxnative.Sender, error) {
	return n.sender, nil
}

func TestRoundCardMessagePreviewHonorsHistoryGrantAndMemory(t *testing.T) {
	dir := t.TempDir()
	worker := &stationWechatWorker{dir: dir, policy: stationWechatPolicy{Grants: map[string]time.Time{}, HistoryGrants: map[string]time.Time{}}}
	now := time.Now().UTC()
	message := linuxnative.Message{ID: "message", AccountID: "one", ThreadID: "group",
		SenderID: "member", ServerID: "server-message", Type: 1, Direction: "incoming",
		CreatedAt: now.Format(time.RFC3339Nano), Body: "synthetic received request"}
	reads := 0
	var onRead func()
	var accountChanged atomic.Bool
	nativeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads++
		if onRead != nil {
			onRead()
		}
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
		stationWechatNative: roundCardNative{taskEvidenceNative: taskEvidenceNative{connection: apphost.Connection{ID: id, AccountID: "one"}}, sender: native, accountChanged: &accountChanged}}
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
	worker.policy.Grants["group"] = now.Add(-time.Second)
	if strings.Contains(read(""), message.Body) || reads != 0 {
		t.Fatal("default journal read loaded original messages")
	}
	if result := read("include_messages=true"); !strings.Contains(result, message.Body) {
		t.Fatal("owner preview of an observed received message required an unrelated Agent history grant", result, reads)
	}
	onRead = func() {
		worker.mu.Lock()
		delete(worker.policy.Grants, "group")
		worker.mu.Unlock()
	}
	if result := read("include_messages=true"); strings.Contains(result, message.Body) || !strings.Contains(result, "no_conversation_grant") {
		t.Fatal("grant revoked during native read still exposed message", result)
	}
	onRead = nil
	worker.policy.Grants["group"] = now.Add(-time.Second)
	onRead = func() { accountChanged.Store(true) }
	if result := read("include_messages=true"); strings.Contains(result, message.Body) {
		t.Fatal("account changed during native read still exposed message", result)
	}
	onRead = nil
	accountChanged.Store(false)
	delete(worker.policy.Grants, "group")
	if result := read("include_messages=true"); strings.Contains(result, message.Body) || !strings.Contains(result, "no_conversation_grant") {
		t.Fatal("revoked conversation retained original message", result)
	}
	worker.policy.Grants["group"] = now.Add(-time.Second)
	message.Body = "synthetic changed message"
	if strings.Contains(read("include_messages=true"), message.Body) {
		t.Fatal("changed native body bypassed stored source")
	}
	message.Body = input.Text
	clearResult := make(chan error, 1)
	onRead = func() { clearResult <- memory.Clear(companion.ScopeID("wechat", "one", "group")) }
	if strings.Contains(read("include_messages=true"), message.Body) {
		t.Fatal("message forgotten during native read remained in round card")
	}
	if err := <-clearResult; err != nil {
		t.Fatal(err)
	}
	onRead = nil
	if strings.Contains(read("include_messages=true"), message.Body) {
		t.Fatal("forgotten message reappeared")
	}
}

func TestRoundCardMediaSummaryDoesNotExposeTransportXML(t *testing.T) {
	for _, test := range []struct {
		message    linuxnative.Message
		text, kind string
	}{
		{linuxnative.Message{Type: 3, Body: "<private transport xml>"}, "[收到图片]", "image"},
		{linuxnative.Message{Type: 34, Body: "<private voice key>"}, "[收到微信语音]", "voice"},
		{linuxnative.Message{Type: 43, Body: "<private video url>"}, "[收到视频]", "video"},
		{linuxnative.Message{Type: 47}, "[收到表情包]", "sticker"},
		{linuxnative.Message{Type: 49, Body: `<msg><appmsg><type>6</type><title>合成文件.txt</title><cdnkey>private</cdnkey></appmsg></msg>`}, "[文件] 合成文件.txt", "file"},
		{linuxnative.Message{Type: 49, Link: &linuxnative.LinkCard{Title: "合成分享标题", URL: "private"}}, "[分享] 合成分享标题", "share"},
	} {
		text, kind, literal := wechatRoundMessageText(test.message)
		if text != test.text || kind != test.kind || literal != "" || strings.Contains(text, "private") {
			t.Fatal("unsafe or incorrect media preview", text, kind, literal)
		}
	}
}
