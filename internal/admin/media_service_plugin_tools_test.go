package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

func TestMediaServiceArgumentsRejectOpaqueReferences(t *testing.T) {
	for _, raw := range []string{
		`{"source_message_id":"message-one","media_ref":"audio_0"}`,
		`{"source_message_id":"message-one","path":"C:\\private.wav"}`,
		`{"source_message_id":"message-one"}{"source_message_id":"message-two"}`,
	} {
		if _, err := decodeMediaServiceArguments(json.RawMessage(raw)); err == nil {
			t.Fatalf("unsafe argument accepted: %s", raw)
		}
	}
	if _, err := decodeMediaServiceArguments(json.RawMessage(`{"source_message_id":"message-one","prompt":"describe it","max_tokens":1024}`)); err != nil {
		t.Fatal(err)
	}
}

func TestMediaServiceSourceUsesCanonicalIncomingMessage(t *testing.T) {
	f := setup(t)
	n := &bindingChannelsNative{fakeNative: f.native, msg: linuxnative.Message{ID: "canonical", AccountID: "test", ThreadID: "filehelper", SenderID: "member", Direction: "incoming", Type: 3}}
	f.manager.native = n
	p := &permittedTools{manager: f.manager}
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "member"})
	msg, err := p.mediaServiceSource(context.Background(), "canonical")
	if err != nil || msg.ID != "canonical" {
		t.Fatalf("canonical source rejected: %#v %v", msg, err)
	}
	if _, err = p.mediaServiceSource(context.Background(), "forged-id"); err == nil {
		t.Fatal("native response for a different ID was accepted")
	}
	n.msg.Direction = "outgoing"
	if _, err = p.mediaServiceSource(context.Background(), "canonical"); err == nil {
		t.Fatal("outgoing source was accepted")
	}
}

func TestMediaServiceReadinessIsIndependentPerCapability(t *testing.T) {
	f, _, _, ctx := mediaPolicyFixture(t, false)
	if !f.manager.mediaObservationConfigured(ctx) || f.manager.mediaTranscriptionConfigured(ctx) {
		t.Fatal("vision-only configuration did not remain independent")
	}
	s, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Configuration(ctx, mediaServicePluginID)
	if err != nil {
		t.Fatal(err)
	}
	i, err := s.Installed(ctx, mediaServicePluginID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetEnabled(ctx, mediaServicePluginID, i.Revision, false); err != nil {
		t.Fatal(err)
	}
	_, err = s.SaveConfiguration(ctx, mediaServicePluginID, pluginhost.ConfigUpdate{
		Revision: c.Revision,
		Values: map[string]json.RawMessage{
			"asr_endpoint": json.RawMessage(`"https://provider.example/transcribe"`),
			"asr_model":    json.RawMessage(`"synthetic-asr"`),
		},
		Secrets: map[string]string{"asr_key": "synthetic-private-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.manager.mediaTranscriptionConfigured(ctx) {
		t.Fatal("disabled media package was reported ready for transcription")
	}
	i, err = s.Installed(ctx, mediaServicePluginID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetEnabled(ctx, mediaServicePluginID, i.Revision, true); err != nil {
		t.Fatal(err)
	}
	if f.manager.mediaObservationConfigured(ctx) || !f.manager.mediaTranscriptionConfigured(ctx) {
		t.Fatal("remote ASR-only configuration leaked a visual requirement")
	}
}

func TestMediaServiceDiscoveryPublishesHostAdapterSchema(t *testing.T) {
	f, n, _, ctx := mediaPolicyFixture(t, false)
	f.manager.mu.Lock()
	f.manager.config.Enabled = true
	f.manager.config.PrivateEnabled = true
	f.manager.config.DirectIDs = []string{n.msg.ThreadID}
	f.manager.mu.Unlock()
	ac := f.manager.agent.config()
	ac.Enabled = true
	if err := f.manager.agent.save(ac); err != nil {
		t.Fatal(err)
	}
	p, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: n.msg.ThreadID, Sender: n.msg.SenderID, TraceID: "turn-media", Mode: "direct"})
	s, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	h, err := pluginruntime.NewController(s, &packageTestExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	f.manager.packageRuntime, f.manager.packageRuntimeReady = h, true
	i, err := s.Installed(ctx, mediaServicePluginID)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.Start(ctx, mediaServicePluginID, i.Revision); err != nil {
		t.Fatal(err)
	}
	raw, err := p.callServicePlugin(ctx, "service_plugins", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Plugins []struct {
			Plugin       string                  `json:"plugin"`
			Capabilities []pluginhost.Capability `json:"capabilities"`
		} `json:"plugins"`
	}
	if json.Unmarshal([]byte(raw), &listed) != nil {
		t.Fatal(raw)
	}
	var observe *pluginhost.Capability
	for _, item := range listed.Plugins {
		if item.Plugin != mediaServicePluginID {
			continue
		}
		for i := range item.Capabilities {
			if item.Capabilities[i].ID == "media.observe" {
				observe = &item.Capabilities[i]
			}
		}
	}
	if observe == nil || !strings.Contains(string(observe.InputSchema), "source_message_id") || strings.Contains(string(observe.InputSchema), "images") || strings.Contains(string(observe.InputSchema), "media_ref") {
		t.Fatalf("discovery leaked private media protocol: %s", raw)
	}
}
