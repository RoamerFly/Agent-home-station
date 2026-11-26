package admin

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"wechat-bot/internal/linuxnative"
)

func TestLegacyMentionContextConfigRoundTripsZeroAndDefaults(t *testing.T) {
	config := defaults()
	config.GroupRules = []GroupRule{{ID: "synthetic@chatroom", Mode: "mention"}}
	if err := validate(config); err != nil {
		t.Fatal("old config without the field should remain valid", err)
	}
	zero := 0
	config.GroupRules[0].MentionContextEachSide = &zero
	if err := validate(config); err != nil {
		t.Fatal("explicit zero was rejected", err)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Config
	if err := json.Unmarshal(encoded, &decoded); err != nil || len(decoded.GroupRules) != 1 ||
		decoded.GroupRules[0].MentionContextEachSide == nil || *decoded.GroupRules[0].MentionContextEachSide != 0 {
		t.Fatal("explicit zero was lost on config reload", err)
	}
	for _, invalid := range []int{-1, 201} {
		config.GroupRules[0].MentionContextEachSide = &invalid
		if err := validate(config); err == nil {
			t.Fatal("out of range context count accepted", invalid)
		}
	}
}

func TestMentionHistoryAllowedRechecksCurrentLegacyRule(t *testing.T) {
	f := setup(t)
	m := f.manager
	const room = "synthetic@chatroom"
	m.mu.Lock()
	m.config.GroupRules = []GroupRule{{ID: room, Mode: "mention"}}
	m.config.Companion.Permissions.Remember = true
	m.mu.Unlock()
	candidate := linuxnative.Message{ID: "old-native-message", AccountID: "test", ThreadID: room,
		CreatedAt: time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339Nano)}
	if !m.mentionHistoryAllowed(context.Background(), candidate) {
		t.Fatal("old group history was hidden despite current mention authorization")
	}
	candidate.AccountID = "other"
	if m.mentionHistoryAllowed(context.Background(), candidate) {
		t.Fatal("other account was authorized")
	}
	candidate.AccountID = "test"
	m.mu.Lock()
	m.config.GroupRules = nil
	m.mu.Unlock()
	if m.mentionHistoryAllowed(context.Background(), candidate) {
		t.Fatal("revoked group was authorized")
	}
}

func TestAutomaticMemoryScopeSleepsMentionGroupsWithoutActiveTasks(t *testing.T) {
	f := setup(t)
	m := f.manager
	const room = "synthetic@chatroom"
	m.mu.Lock()
	m.config.GroupRules = []GroupRule{{ID: room, Mode: "mention"}}
	m.mu.Unlock()
	if m.automaticMemoryScopeAllowed("test", room) || !m.automaticMemoryScopeAllowed("test", "friend") {
		t.Fatal("idle mention group was selected for background maintenance")
	}
	m.agent.mu.Lock()
	m.agent.data.Tasks["synthetic-task"] = agentTask{ID: "synthetic-task", Account: "test", Thread: room, State: "waiting"}
	m.agent.mu.Unlock()
	if !m.automaticMemoryScopeAllowed("test", room) {
		t.Fatal("unfinished group task lost maintenance")
	}
	m.agent.mu.Lock()
	task := m.agent.data.Tasks["synthetic-task"]
	task.State = "completed"
	m.agent.data.Tasks["synthetic-task"] = task
	m.agent.mu.Unlock()
	if m.automaticMemoryScopeAllowed("test", room) {
		t.Fatal("completed task kept an idle group awake")
	}
	m.mu.Lock()
	m.config.GroupRules[0].Mode = "social"
	m.mu.Unlock()
	if !m.automaticMemoryScopeAllowed("test", room) {
		t.Fatal("social group background maintenance was suppressed")
	}
}
