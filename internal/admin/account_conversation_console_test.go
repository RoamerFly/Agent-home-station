package admin

import (
	"context"
	"strings"
	"testing"
	"wechat-bot/internal/linuxnative"
)

type consoleDirectoryNative struct {
	*accountDirectoryNative
	deadlines []bool
	reads     int
}

func (n *consoleDirectoryNative) Catalog(ctx context.Context, kind, thread, search, cursor string) (linuxnative.CatalogPage, error) {
	_, bounded := ctx.Deadline()
	n.deadlines = append(n.deadlines, bounded)
	if kind == "messages" || kind == "members" {
		n.reads++
		return linuxnative.CatalogPage{Items: []map[string]any{{"id": "synthetic-member-row"}}}, nil
	}
	return n.accountDirectoryNative.Catalog(ctx, kind, thread, search, cursor)
}

func TestConsoleAccountDirectoryAndReadsDoNotRequireAutomaticReplies(t *testing.T) {
	f, _ := globalSearchFixture(t)
	n := &consoleDirectoryNative{accountDirectoryNative: &accountDirectoryNative{Native: f.manager.native}}
	f.manager.native = n
	if code, _, _ := f.request("GET", "/api/agent/conversations", nil, false, false); code != 401 || n.queries != 0 {
		t.Fatal("unauthenticated directory", code)
	}
	f.login()
	for _, suffix := range []string{"offset=-1", "limit=oops", "refresh=maybe", "timeout_seconds=-1", "timeout_seconds=9223372036854775807"} {
		if code, _, _ := f.request("GET", "/api/agent/conversations?"+suffix, nil, false, false); code != 400 {
			t.Fatal("invalid query accepted", suffix, code)
		}
	}
	if code, data, _ := f.request("GET", "/api/agent/conversations?timeout_seconds=0", nil, false, false); code != 200 || !strings.Contains(string(data), "目录群") {
		t.Fatal(code, string(data))
	}
	for _, bounded := range n.deadlines {
		if bounded {
			t.Fatal("explicit unlimited request was clamped")
		}
	}
	if permittedRecipient(f.manager.Config().Config, "discovered@chatroom") {
		t.Fatal("directory enabled replies")
	}
	if label := f.manager.memoryLabel(context.Background(), "discovered@chatroom"); label != "目录群" {
		t.Fatal("cached native name lost", label)
	}
	for _, kind := range []string{"messages", "members"} {
		if code, _, _ := f.request("GET", "/api/catalog?kind="+kind+"&thread=discovered@chatroom", nil, false, false); code != 200 {
			t.Fatal("known non-reply conversation denied", kind, code)
		}
	}
	if f.manager.accountThreadKnown("synthetic-member-row") {
		t.Fatal("member/message row became recipient")
	}
	reads := n.reads
	if code, _, _ := f.request("GET", "/api/catalog?kind=messages&thread=invented@chatroom", nil, false, false); code != 403 || n.reads != reads {
		t.Fatal("unknown scope read", code)
	}
	c := f.manager.agent.config()
	c.CrossConversation = false
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.request("GET", "/api/catalog?kind=members&thread=discovered@chatroom", nil, false, false); code != 403 || n.reads != reads {
		t.Fatal("revoked global read continued", code)
	}
	if code, _, _ := f.request("GET", "/api/agent/conversations?refresh=false", nil, false, false); code != 403 {
		t.Fatal("revoked directory access", code)
	}
}

func TestConsoleNativeDirectoryPagesRegisterOnlyConversationRows(t *testing.T) {
	f, _ := globalSearchFixture(t)
	f.manager.native = &consoleDirectoryNative{accountDirectoryNative: &accountDirectoryNative{Native: f.manager.native}}
	f.login()
	if code, _, _ := f.request("GET", "/api/catalog?kind=groups", nil, false, false); code != 200 || !f.manager.accountThreadKnown("group-directory@chatroom") {
		t.Fatal("group page was not indexed", code)
	}
	if code, _, _ := f.request("GET", "/api/catalog?kind=contacts", nil, false, false); code != 200 || !f.manager.accountThreadKnown("wxid_directory") || f.manager.accountThreadKnown("wxid_stranger") {
		t.Fatal("contact identity filtering failed", code)
	}
}

func TestConsoleFileReadUsesAccountIdentityWithoutEnablingReplies(t *testing.T) {
	f, _, n := forwardFixture(t)
	f.manager.config.GroupRules = nil
	if err := f.manager.rememberNativeConversations(map[string]string{"source@chatroom": "合成来源"}); err != nil {
		t.Fatal(err)
	}
	f.login()
	input := map[string]string{"message_id": "file-message", "thread_id": "source@chatroom"}
	if code, _, _ := f.request("POST", "/api/agent/file-read", input, true, true); code != 200 || n.downloads != 1 || n.submits.Load() != 0 {
		t.Fatal("file acquisition failed or sent", code, n.downloads, n.submits.Load())
	}
	if permittedRecipient(f.manager.Config().Config, "source@chatroom") {
		t.Fatal("file read enabled replies")
	}
	n.message.AccountID = "foreign"
	if code, _, _ := f.request("POST", "/api/agent/file-read", input, true, true); code != 400 || n.downloads != 1 {
		t.Fatal("foreign attachment read", code)
	}
	c := f.manager.agent.config()
	c.CrossConversation = false
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.request("POST", "/api/agent/file-read", input, true, true); code != 403 || n.downloads != 1 {
		t.Fatal("revoked file read continued", code)
	}
}
