package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOldCooldownFieldsLoadButAreNoLongerConfiguration(t *testing.T) {
	dir := t.TempDir()
	raw, _ := json.Marshal(defaults())
	var old map[string]any
	_ = json.Unmarshal(raw, &old)
	old["cooldown_seconds"] = -500
	old["companion"].(map[string]any)["decision_interval_seconds"] = 900000
	raw, _ = json.Marshal(old)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	current, err := loadConfig(dir)
	if err != nil {
		t.Fatal("legacy settings prevented startup", err)
	}
	raw, _ = json.Marshal(current)
	if strings.Contains(string(raw), "cooldown_seconds") || strings.Contains(string(raw), "decision_interval_seconds") {
		t.Fatal("obsolete UI/API controls remain")
	}
}
