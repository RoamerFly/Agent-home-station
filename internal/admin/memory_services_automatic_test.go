package admin

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"wechat-bot/internal/companion"
)

func automaticMemoryServicesFixture(enabled bool, embeddingKey, maintenanceKey string) memoryServices {
	return memoryServices{
		Settings: companion.MemorySettings{
			Enabled: enabled, BatchMessages: 12, IdleSeconds: 0, DailyTokens: 0,
			RecentTokens: 1, RecallTokens: 1, ContinuityTokens: 0, ContextWindow: 0, OutputReserve: 0,
		},
		Embedding: companion.EmbeddingConfig{
			BaseURL: "https://embedding.invalid/v1", Model: "embedding-test", APIKey: embeddingKey,
		},
		Maintenance: memoryModelConfig{
			BaseURL: "https://maintenance.invalid/v1", Model: "maintenance-test", APIKey: maintenanceKey, Provider: "openai-compatible",
		},
	}
}

func writeAutomaticMemoryServices(t *testing.T, m *Manager, value memoryServices) {
	t.Helper()
	if err := m.saveMemoryServices(value); err != nil {
		t.Fatal(err)
	}
}

func TestAutomaticMemoryServiceTogglePreservesAccountConfiguration(t *testing.T) {
	f, accounts := setupAccounts(t)
	second := accounts.lookup("second")
	if second == nil {
		t.Fatal("second account was not loaded")
	}
	primaryBefore := automaticMemoryServicesFixture(true, "primary-embedding-secret", "primary-maintenance-secret")
	secondBefore := automaticMemoryServicesFixture(true, "second-embedding-secret", "second-maintenance-secret")
	writeAutomaticMemoryServices(t, f.manager, primaryBefore)
	writeAutomaticMemoryServices(t, second.manager, secondBefore)

	code, body, _ := f.request("POST", "/api/memory/services/automatic", map[string]bool{"enabled": false}, true, true)
	if code != 200 {
		t.Fatalf("primary toggle: %d %s", code, body)
	}
	if string(body) == "" || containsSecret(body, primaryBefore.Embedding.APIKey, primaryBefore.Maintenance.APIKey) {
		t.Fatalf("toggle response exposed a secret: %s", body)
	}
	var view struct {
		Settings  companion.MemorySettings `json:"settings"`
		Embedding struct {
			APIKey string `json:"api_key"`
		} `json:"embedding"`
		Maintenance struct {
			APIKey string `json:"api_key"`
		} `json:"maintenance"`
		EmbeddingKeyConfigured   bool `json:"embedding_key_configured"`
		MaintenanceKeyConfigured bool `json:"maintenance_key_configured"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if view.Settings.Enabled || view.Settings.DailyTokens != 0 || view.Embedding.APIKey != "" || view.Maintenance.APIKey != "" || !view.EmbeddingKeyConfigured || !view.MaintenanceKeyConfigured {
		t.Fatalf("unexpected sanitized toggle view: %s", body)
	}

	primaryAfter, err := f.manager.loadMemoryServices()
	if err != nil {
		t.Fatal(err)
	}
	primaryExpected := primaryBefore
	primaryExpected.Settings.Enabled = false
	if !reflect.DeepEqual(primaryAfter, primaryExpected) {
		t.Fatalf("primary configuration was not preserved\n got: %#v\nwant: %#v", primaryAfter, primaryExpected)
	}
	secondAfter, err := second.manager.loadMemoryServices()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(secondAfter, secondBefore) {
		t.Fatalf("primary account toggle changed second account\n got: %#v\nwant: %#v", secondAfter, secondBefore)
	}

	code, body, _ = f.request("POST", "/api/memory/services/automatic?account=second", map[string]bool{"enabled": false}, true, true)
	if code != 200 {
		t.Fatalf("second toggle: %d %s", code, body)
	}
	secondAfter, err = second.manager.loadMemoryServices()
	if err != nil {
		t.Fatal(err)
	}
	secondExpected := secondBefore
	secondExpected.Settings.Enabled = false
	if !reflect.DeepEqual(secondAfter, secondExpected) {
		t.Fatalf("second configuration was not preserved\n got: %#v\nwant: %#v", secondAfter, secondExpected)
	}
	code, body, _ = f.request("GET", "/api/memory/services?account=second", nil, true, false)
	if code != 200 || containsSecret(body, secondBefore.Embedding.APIKey, secondBefore.Maintenance.APIKey) {
		t.Fatalf("second readback was not sanitized: %d %s", code, body)
	}
	var readback struct {
		Settings companion.MemorySettings `json:"settings"`
	}
	if err := json.Unmarshal(body, &readback); err != nil || readback.Settings.Enabled || readback.Settings.DailyTokens != 0 {
		t.Fatalf("second readback did not retain the saved setting: %v %s", err, body)
	}
}

func TestAutomaticMemoryServiceToggleRequiresEnabled(t *testing.T) {
	f := setup(t)
	f.login()
	for _, input := range []any{map[string]any{}, map[string]any{"enabled": false, "unexpected": true}} {
		code, _, _ := f.request("POST", "/api/memory/services/automatic", input, true, true)
		if code != 400 {
			t.Fatalf("invalid automatic payload accepted: %#v (%d)", input, code)
		}
	}
}

func containsSecret(body []byte, secrets ...string) bool {
	for _, secret := range secrets {
		if secret != "" && bytes.Contains(body, []byte(secret)) {
			return true
		}
	}
	return false
}
