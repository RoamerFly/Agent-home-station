package admin

import (
	"context"
	"strings"
	"testing"
	"wechat-bot/internal/channels"
	"wechat-bot/internal/linuxnative"
)

func TestChannelsSettingsProtectAndNeverReturnCookie(t *testing.T) {
	f := setup(t)
	f.login()
	if code, _, _ := f.request("POST", "/api/channels/settings", map[string]any{"enabled": true, "cookie": "hy_user=fixture; hy_token=synthetic-fixture-session"}, true, false); code != 403 {
		t.Fatal("csrf bypass", code)
	}
	code, b, _ := f.request("POST", "/api/channels/settings", map[string]any{"enabled": true, "cookie": "hy_user=fixture; hy_token=synthetic-fixture-session"}, true, true)
	if code != 200 || strings.Contains(string(b), "synthetic-fixture-session") {
		t.Fatal("secret leaked or save failed", code)
	}
	code, b, _ = f.request("GET", "/api/channels/settings", nil, false, false)
	if code != 200 || strings.Contains(string(b), "synthetic-fixture-session") || !strings.Contains(string(b), `"cookie_configured":true`) {
		t.Fatal("credential view unsafe", code)
	}
	f.request("POST", "/api/channels/settings", map[string]any{"enabled": true}, true, true)
	c, e := loadChannelsSettings(f.dir)
	if e != nil || !strings.Contains(c.Cookie, "synthetic-fixture-session") {
		t.Fatal("blank save erased credential")
	}
	f.request("POST", "/api/channels/settings", map[string]any{"clear_cookie": true}, true, true)
	c, _ = loadChannelsSettings(f.dir)
	if c.Cookie != "" || c.Enabled {
		t.Fatal("clear did not revoke resolver")
	}
}

type bindingChannelsNative struct {
	*fakeNative
	msg    linuxnative.Message
	source linuxnative.ChannelsSource
}

func (n *bindingChannelsNative) Message(context.Context, string) (linuxnative.Message, error) {
	return n.msg, nil
}
func (n *bindingChannelsNative) ChannelsMedia(context.Context, string) (linuxnative.ChannelsSource, error) {
	return n.source, nil
}

func TestChannelsBindingStoresSameRoomObjectAliasAndRejectsOtherRooms(t *testing.T) {
	f := setup(t)
	f.login()
	kind := 51
	n := &bindingChannelsNative{fakeNative: f.native, msg: linuxnative.Message{ID: "card-one", ThreadID: "filehelper", Direction: "incoming", AppMessageType: &kind}, source: linuxnative.ChannelsSource{AccountID: "test", ThreadID: "filehelper", MessageID: "card-one", ObjectID: "18446744073709551614"}}
	f.manager.native = n
	code, _, _ := f.request("POST", "/api/channels/bind", map[string]any{"message_id": "card-one", "url": "https://weixin.qq.com/sph/Synthetic1?tracking=discarded"}, true, true)
	if code != 200 {
		t.Fatal("authorized bind failed", code)
	}
	bindings, e := loadChannelsBindings(f.dir)
	alias, ok := bindings[channelBindingID("test", "filehelper", "object:"+n.source.ObjectID)]
	if e != nil || len(bindings) != 2 || !ok || alias.MessageID != "card-one" || alias.URL != "https://weixin.qq.com/sph/Synthetic1" {
		t.Fatal("alias missing or not normalized", e)
	}
	if _, exists := bindings[channelBindingID("test", "other@chatroom", "object:"+n.source.ObjectID)]; exists {
		t.Fatal("binding leaked to another room")
	}
	n.msg.ThreadID = "unapproved@chatroom"
	if code, _, _ := f.request("POST", "/api/channels/bind", map[string]any{"message_id": "card-one", "url": "https://weixin.qq.com/sph/Synthetic1"}, true, true); code != 400 {
		t.Fatal("unapproved card accepted", code)
	}
}

func TestResolvedVideoRequiresExactWorkOrTitleAndAuthor(t *testing.T) {
	s := linuxnative.ChannelsSource{ObjectID: "123", Title: "合成测试原视频的标题", Author: "作者甲"}
	v := channels.ResolvedVideo{ObjectID: "456", Title: s.Title, Author: s.Author}
	if matchChannelsVideo(s, linuxnative.Message{}, v) {
		t.Fatal("different object used")
	}
	v.ObjectID = ""
	if !matchChannelsVideo(s, linuxnative.Message{}, v) {
		t.Fatal("exact metadata rejected")
	}
	v.Author = "作者乙"
	if matchChannelsVideo(s, linuxnative.Message{}, v) {
		t.Fatal("same title on wrong author accepted")
	}
}

func TestChannelsMemoryRetainsActualAudioWithinTurnBudget(t *testing.T) {
	msg := linuxnative.Message{Link: &linuxnative.LinkCard{Title: "合成视频", Description: "作者甲"}}
	text := channelsMemoryObservation(msg, "[视频实际观察]16秒\n[画面观察]"+strings.Repeat("画面细节", 500)+"\n[实际音轨转写]这是实际转写的音轨")
	if len(text) > 2048 || !strings.Contains(text, "[实际音轨转写]") || !strings.Contains(text, "[画面观察]") {
		t.Fatal("one modality lost from memory", len(text))
	}
}
