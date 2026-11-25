package admin

import (
	"reflect"
	"testing"
)

func TestMakeModelKeepsConfiguredToolOutputBudget(t *testing.T) {
	m := &Manager{dir: t.TempDir()}
	c := defaults()
	c.APIKey = "synthetic-model-budget-key"
	c.BaseURL = "https://api.example.invalid"
	c.MaxTokens = 32768
	c.ThinkingEnabled = false
	c.Companion.Enabled = true

	client, err := m.makeModel(c)
	if err != nil {
		t.Fatalf("makeModel failed: %v", err)
	}
	got := reflect.ValueOf(client).Elem().FieldByName("toolMaxTokens").Int()
	if got != int64(c.MaxTokens) {
		t.Fatalf("tool output budget changed: got %d, want %d", got, c.MaxTokens)
	}
}
