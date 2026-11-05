package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestOwnerGlobalMemoryIncludesNonReplyThreadsButNotOtherAccounts(t *testing.T) {
	f, p := globalSearchFixture(t)
	now := time.Now()
	for _, account := range []string{"test", "other-account"} {
		in := companion.Input{Channel: "wechat", Account: account, Thread: "quiet@chatroom", Sender: "member", Text: "stored-" + account, TraceID: "source-" + account}
		if err := f.manager.memory.Observe(in, f.manager.config.Companion, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.manager.memory.Observe(companion.Input{Channel: "other-channel", Account: "test", Thread: "quiet@chatroom", Sender: "member", Text: "stored-other-channel"}, f.manager.config.Companion, now); err != nil {
		t.Fatal(err)
	}
	if permittedRecipient(f.manager.Config().Config, "quiet@chatroom") {
		t.Fatal("fixture enabled automatic replies")
	}
	result, err := p.Call(context.Background(), "agent_global_search", json.RawMessage(`{"thread_id":"quiet@chatroom"}`))
	if err != nil || !strings.Contains(result, "stored-test") || strings.Contains(result, "stored-other-account") || strings.Contains(result, "stored-other-channel") {
		t.Fatal(result, err)
	}
	if !p.conversationReadAllowed("quiet@chatroom") {
		t.Fatal("owner denied stored account conversation")
	}
	p.trace.Sender = "wxid_member"
	if p.conversationReadAllowed("quiet@chatroom") {
		t.Fatal("member inherited owner's global scope")
	}
	if _, err := p.Call(context.Background(), "agent_global_search", json.RawMessage(`{}`)); err == nil {
		t.Fatal("member got global memory")
	}
}

type accountDirectoryNative struct {
	Native
	queries int
	mode    string
}

func (n *accountDirectoryNative) Catalog(_ context.Context, kind, thread, search, cursor string) (linuxnative.CatalogPage, error) {
	n.queries++
	if n.mode == "offline" {
		return linuxnative.CatalogPage{}, errors.New("synthetic unavailable")
	}
	if kind == "groups" {
		return linuxnative.CatalogPage{Items: []map[string]any{{"roomId": "group-directory@chatroom", "displayName": "群目录作品"}}}, nil
	}
	if kind == "threads" {
		return linuxnative.CatalogPage{Items: []map[string]any{{"peerId": "discovered@chatroom", "displayName": "目录群"}}}, nil
	}
	if kind == "contacts" && cursor == "" {
		next := "second-page"
		return linuxnative.CatalogPage{Items: []map[string]any{{"id": "wxid_directory", "displayName": "目录好友"}, {"id": "wxid_stranger", "relation": "stranger"}}, NextCursor: &next}, nil
	}
	if kind == "contacts" {
		if n.mode == "repeated" {
			next := "second-page"
			return linuxnative.CatalogPage{NextCursor: &next}, nil
		}
		return linuxnative.CatalogPage{Items: []map[string]any{{"id": "wxid_page2", "displayName": "第二页好友"}}}, nil
	}
	return linuxnative.CatalogPage{}, errors.New("unexpected directory kind")
}

func TestOwnerDirectoryPartialFailureRetainsVerifiedProgressAndStopsCursorLoop(t *testing.T) {
	f, p := globalSearchFixture(t)
	n := &accountDirectoryNative{Native: f.manager.native, mode: "repeated"}
	f.manager.native = n
	result, err := p.Call(context.Background(), "wechat_conversations", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(result, "contacts_cursor_repeated") || !strings.Contains(result, `"native_refresh_complete":false`) || !f.manager.accountThreadKnown("wxid_directory") || n.queries > 6 {
		t.Fatal(result, err, n.queries)
	}
	n.mode = "offline"
	result, err = p.Call(context.Background(), "wechat_conversations", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(result, "wxid_directory") || !strings.Contains(result, "groups_unavailable") {
		t.Fatal(result, err)
	}
}

func TestOwnerNativeDirectorySurvivesPagingAndDoesNotEnableReplies(t *testing.T) {
	f, p := globalSearchFixture(t)
	n := &accountDirectoryNative{Native: f.manager.native}
	f.manager.native = n
	result, err := p.Call(context.Background(), "wechat_conversations", json.RawMessage(`{"limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Total, Next int
		More        bool `json:"has_more"`
	}
	if json.Unmarshal([]byte(result), &page) != nil || !page.More || page.Total < 4 {
		t.Fatal(result)
	}
	for _, id := range []string{"discovered@chatroom", "wxid_directory", "wxid_page2"} {
		if !f.manager.accountThreadKnown(id) {
			t.Fatal("native directory not retained", id)
		}
		if permittedRecipient(f.manager.Config().Config, id) {
			t.Fatal("directory enabled automatic replies", id)
		}
	}
	if f.manager.accountThreadKnown("wxid_stranger") || f.manager.accountThreadKnown("invented@chatroom") {
		t.Fatal("unknown/stranger accepted")
	}
	queries := n.queries
	result, err = p.Call(context.Background(), "wechat_conversations", json.RawMessage(`{"refresh":false,"offset":1,"limit":100}`))
	if err != nil || n.queries != queries || !strings.Contains(result, "目录好友") {
		t.Fatal(result, err)
	}
	result, err = p.Call(context.Background(), "wechat_conversations", json.RawMessage(`{"refresh":false,"query":"第二页"}`))
	if err != nil || !strings.Contains(result, `"total":1`) || !strings.Contains(result, "wxid_page2") || strings.Contains(result, "wxid_directory") {
		t.Fatal(result, err)
	}
	// A fresh manager view reads the account-bound cache, not a trace-local grant.
	other := &Manager{dir: f.manager.dir, native: f.manager.native, config: f.manager.config}
	if !other.accountThreadKnown("discovered@chatroom") {
		t.Fatal("directory lost on reopen")
	}
	index := accountConversationIndex{Account: "foreign", Names: map[string]string{"foreign@chatroom": "foreign"}}
	data, _ := json.Marshal(index)
	if err := os.WriteFile(filepath.Join(f.manager.dir, "account-conversations.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if other.accountThreadKnown("foreign@chatroom") {
		t.Fatal("foreign directory crossed account boundary")
	}
}

func TestOwnerMediaQueueToNonReplyGroupRemainsTargetBoundAndRevocable(t *testing.T) {
	f, p, batch := crossRecoveryFixture(t, 1)
	rules := []GroupRule{}
	for _, rule := range f.manager.config.GroupRules {
		if rule.ID != batch.Target {
			rules = append(rules, rule)
		}
	}
	f.manager.config.GroupRules = rules
	if err := f.manager.rememberNativeConversations(map[string]string{batch.Target: "合成目录目标群"}); err != nil {
		t.Fatal(err)
	}
	if permittedRecipient(f.manager.Config().Config, batch.Target) {
		t.Fatal("fixture still allows auto reply")
	}
	if !p.crossAllowed(batch.Source, batch.Target) || !f.manager.crossSeedAllowed(batch.Parts[0]) {
		t.Fatal("owner task confused with auto-reply setting")
	}
	wrong := batch.Parts[0]
	wrong.Input.Thread = "invented@chatroom"
	if f.manager.crossSeedAllowed(wrong) {
		t.Fatal("queue target identity changed")
	}
	c := f.manager.agent.config()
	c.CrossConversation = false
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	if p.crossAllowed(batch.Source, batch.Target) || f.manager.crossSeedAllowed(batch.Parts[0]) {
		t.Fatal("revoked owner authority continued")
	}
}
