package admin

import (
	"encoding/json"
	"testing"

	"wechat-bot/internal/linuxnative"
)

func writeLegacyShareBindings(t *testing.T, dir string, bindings map[string]channelsBinding) {
	t.Helper()
	raw, err := json.Marshal(bindings)
	if err != nil {
		t.Fatal(err)
	}
	if err = writePrivate(dir+"/channels-bindings.json", raw); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyShareBindingURLUsesExactMessageThenSameScopeObjectAlias(t *testing.T) {
	f := setup(t)
	source := linuxnative.ChannelsSource{AccountID: "test", ThreadID: "room@chatroom", MessageID: "incoming-1", ObjectID: "18446744073709551614"}
	messageKey := channelBindingID(source.AccountID, source.ThreadID, source.MessageID)
	aliasKey := channelBindingID(source.AccountID, source.ThreadID, "object:"+source.ObjectID)
	writeLegacyShareBindings(t, f.dir, map[string]channelsBinding{
		messageKey: {Account: source.AccountID, Thread: source.ThreadID, MessageID: source.MessageID, URL: "https://weixin.qq.com/sph/Message01?tracking=ignored"},
		aliasKey:   {Account: source.AccountID, Thread: source.ThreadID, MessageID: "old-message", ObjectID: source.ObjectID, URL: "https://weixin.qq.com/sph/Object002"},
	})
	url, found, err := legacyShareBindingURL(f.dir, source)
	if err != nil || !found || url != "https://weixin.qq.com/sph/Message01" {
		t.Fatal("message binding was not preferred", url, found, err)
	}

	deleteBinding := map[string]channelsBinding{aliasKey: {Account: source.AccountID, Thread: source.ThreadID, MessageID: "old-message", ObjectID: source.ObjectID, URL: "https://weixin.qq.com/sph/Object002"}}
	writeLegacyShareBindings(t, f.dir, deleteBinding)
	url, found, err = legacyShareBindingURL(f.dir, source)
	if err != nil || !found || url != "https://weixin.qq.com/sph/Object002" {
		t.Fatal("same-scope object alias was not accepted", url, found, err)
	}
}

func TestLegacyShareBindingURLFailsClosedForScopeAndURL(t *testing.T) {
	f := setup(t)
	source := linuxnative.ChannelsSource{AccountID: "test", ThreadID: "room@chatroom", MessageID: "incoming-1", ObjectID: "123"}
	key := channelBindingID(source.AccountID, source.ThreadID, source.MessageID)
	writeLegacyShareBindings(t, f.dir, map[string]channelsBinding{key: {Account: source.AccountID, Thread: "other@chatroom", MessageID: source.MessageID, URL: "https://weixin.qq.com/sph/Valid123"}})
	if _, _, err := legacyShareBindingURL(f.dir, source); err == nil || err.Error() != "share_legacy_binding_scope_mismatch" {
		t.Fatal("cross-thread binding was accepted", err)
	}
	// An object match stored under a message key is not an exact message
	// binding. Only the explicit object alias may use the object identity.
	writeLegacyShareBindings(t, f.dir, map[string]channelsBinding{key: {Account: source.AccountID, Thread: source.ThreadID, MessageID: "other-message", ObjectID: source.ObjectID, URL: "https://weixin.qq.com/sph/Valid123"}})
	if _, _, err := legacyShareBindingURL(f.dir, source); err == nil || err.Error() != "share_legacy_binding_scope_mismatch" {
		t.Fatal("object binding under message key was accepted", err)
	}
	writeLegacyShareBindings(t, f.dir, map[string]channelsBinding{key: {Account: source.AccountID, Thread: source.ThreadID, MessageID: source.MessageID, URL: "https://example.invalid/not-a-share"}})
	if _, _, err := legacyShareBindingURL(f.dir, source); err == nil || err.Error() != "share_legacy_binding_invalid" {
		t.Fatal("invalid legacy URL was accepted", err)
	}
	writeLegacyShareBindings(t, f.dir, map[string]channelsBinding{})
	if url, found, err := legacyShareBindingURL(f.dir, source); err != nil || found || url != "" {
		t.Fatal("missing binding did not fall through", url, found, err)
	}
}
