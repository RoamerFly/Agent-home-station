package admin

import (
	"encoding/json"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestGroupCollectionWindowRoundTrip(t *testing.T) {
	f := setup(t)
	f.login()
	c := f.manager.Config().Config
	c.Companion.Conversation = companion.ConversationDefaults()
	const room = "room@chatroom"
	if c.Companion.Conversation.CollectionWindow(room) != 20*time.Second {
		t.Fatal("new group did not get the default window")
	}
	for _, ms := range []int{45000, 20000, 0, -1} {
		c.Companion.Conversation.GroupAggregateMS = &ms
		code, _, _ := f.request("POST", "/api/config", ConfigInput{Config: c}, true, true)
		if ms < 0 {
			if code != 400 {
				t.Fatalf("invalid duration accepted: HTTP %d", code)
			}
			continue
		}
		loaded, err := loadConfig(f.dir)
		if code != 200 || err != nil {
			t.Fatalf("save %dms: HTTP %d, %v", ms, code, err)
		}
		s := loaded.Companion.ConversationConfig()
		if s.CollectionWindow(room) != time.Duration(ms)*time.Millisecond || s.CollectionWindow("friend") != 2*time.Second {
			t.Fatal("group and private collection windows should be independent")
		}
	}
}

func TestConversationSettingsPreserveExplicitFalseAndValidateWakeBounds(t *testing.T) {
	f := setup(t)
	f.login()
	c := f.manager.Config().Config
	c.Companion.Conversation = companion.ConversationDefaults()
	c.Companion.Conversation.Enabled = false
	c.Companion.Conversation.RecheckOnMessage = false
	c.Companion.Conversation.AllowWake = false
	c.Companion.Conversation.AggregateMS = 0
	code, body, _ := f.request("POST", "/api/config", ConfigInput{Config: c}, true, true)
	if code != 200 {
		t.Fatal(code, string(body))
	}
	loaded, err := loadConfig(f.dir)
	if err != nil || loaded.Companion.Conversation == nil || loaded.Companion.Conversation.Enabled || loaded.Companion.Conversation.RecheckOnMessage || loaded.Companion.Conversation.AllowWake || loaded.Companion.Conversation.AggregateMS != 0 {
		t.Fatal("false settings lost", err, loaded.Companion.Conversation)
	}
	c.Companion.Conversation.WakeMinSeconds = 200
	c.Companion.Conversation.WakeMaxSeconds = 30
	code, _, _ = f.request("POST", "/api/config", ConfigInput{Config: c}, true, true)
	if code != 400 {
		t.Fatal("reversed wake range admitted", code)
	}
	code, body, _ = f.request("GET", "/api/config", nil, true, false)
	var got ConfigView
	_ = json.Unmarshal(body, &got)
	if code != 200 || got.Companion.Conversation.WakeMinSeconds != 30 {
		t.Fatal("invalid input mutated saved config")
	}
}
