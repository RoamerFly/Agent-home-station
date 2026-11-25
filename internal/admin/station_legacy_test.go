package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareStationLegacyDataCopiesMemoryAndMapsPolicy(t *testing.T) {
	root := t.TempDir()
	worker := filepath.Join(root, "worker")
	legacy := filepath.Join(root, "legacy")
	if err := os.MkdirAll(filepath.Join(worker, "memory"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worker, "memory", "memory.sqlite"), []byte("synthetic-sqlite"), 0600); err != nil {
		t.Fatal(err)
	}
	model := map[string]any{"provider": "deepseek", "base_url": "https://api.deepseek.com", "model": "deepseek-flash", "max_tokens": 768, "reasoning_effort": "low", "persona": "合成角色", "api_key": "synthetic-key", "key_configured": true}
	writeJSON := func(path string, value any) {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON(filepath.Join(root, "local-model.json"), model)
	writeJSON(filepath.Join(worker, "worker.json"), map[string]any{
		"enabled": true, "paused": false,
		"reply_grants":         map[string]string{"test@chatroom": "2026-01-01T00:00:00Z"},
		"station_image_vision": true,
		"group_rules":          map[string]any{"test@chatroom": map[string]any{"mode": "all", "force_reply_when_mentioned": true}},
	})
	if err := PrepareStationLegacyData(legacy, filepath.Join(root, "local-model.json"), worker); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(legacy, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if !config.Enabled || config.APIKey != "synthetic-key" || len(config.GroupRules) != 1 || config.GroupRules[0].ID != "test@chatroom" || config.GroupRules[0].Mode != "all" {
		t.Fatalf("unexpected mapped legacy config: %+v", config)
	}
	if got, err := os.ReadFile(filepath.Join(legacy, "companion", "memory.sqlite")); err != nil || string(got) != "synthetic-sqlite" {
		t.Fatalf("memory was not copied: %v %q", err, got)
	}
	if err := PrepareStationLegacyData(legacy, filepath.Join(root, "local-model.json"), worker); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareStationLegacyDataBootstrapsWithoutPreviousInstallation(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "legacy")
	model := filepath.Join(root, "authority", "local-model.json")
	worker := filepath.Join(root, "workers", "legacy")
	if err := PrepareStationLegacyData(legacy, model, worker); err != nil {
		t.Fatal(err)
	}
	if config, err := loadConfig(legacy); err != nil || config.Enabled || config.APIKey != "" {
		t.Fatalf("fresh console must use disabled defaults, error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(legacy, "config.json")); !os.IsNotExist(err) {
		t.Fatal("bootstrap must leave later migration possible")
	}
	if err := os.MkdirAll(filepath.Dir(model), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(model, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareStationLegacyData(legacy, model, worker); err == nil {
		t.Fatal("corrupt existing input must remain an error")
	}
}
