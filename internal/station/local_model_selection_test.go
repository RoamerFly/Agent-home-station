package station

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestLocalModelSelectionIsPerTurnAndKeepsSavedSettings(t *testing.T) {
	store := newLocalModelStore(t.TempDir())
	before, err := store.View()
	if err != nil {
		t.Fatal(err)
	}
	before, err = store.Save(LocalModelInput{LocalModelSettings: before, APIKey: "local-test-key-000000000000000000"})
	if err != nil {
		t.Fatal(err)
	}
	thinkingOff := false
	selection := localChatModelSelection{Model: "deepseek-reasoner", ThinkingEnabled: &thinkingOff, ReasoningEffort: "low"}
	model, effective, err := store.ClientSnapshotForSelection(selection)
	if err != nil || model == nil {
		t.Fatalf("selected client: %v", err)
	}
	if effective.Model != selection.Model || effective.ThinkingEnabled || effective.ReasoningEffort != "low" || effective.TaskReasoning != "off" || effective.Provider != before.Provider || effective.BaseURL != before.BaseURL || effective.Revision != before.Revision {
		t.Fatalf("wrong per-turn settings: %+v", effective)
	}
	_, thinkingTurn, err := store.ClientSnapshotForSelection(localChatModelSelection{ReasoningEffort: "high"})
	if err != nil || !thinkingTurn.ThinkingEnabled || thinkingTurn.TaskReasoning != "high" {
		t.Fatalf("explicit effort was not applied to the turn: %+v %v", thinkingTurn, err)
	}
	after, err := store.View()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("selection changed saved settings: before=%+v after=%+v err=%v", before, after, err)
	}
	_, inherited, err := store.ClientSnapshot()
	if err != nil || inherited.Model != before.Model || inherited.ThinkingEnabled != before.ThinkingEnabled || inherited.ReasoningEffort != before.ReasoningEffort {
		t.Fatalf("next turn did not inherit saved settings: %+v %v", inherited, err)
	}
}

func TestLocalModelSelectionDoesNotPretendCompatibleModelsSupportThinking(t *testing.T) {
	store := newLocalModelStore(t.TempDir())
	view, err := store.View()
	if err != nil {
		t.Fatal(err)
	}
	view.Provider = "openai-compatible"
	view.ThinkingEnabled = false
	view.TaskReasoning = ""
	view, err = store.Save(LocalModelInput{LocalModelSettings: view, APIKey: "local-test-key-000000000000000000"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.ClientSnapshotForSelection(localChatModelSelection{ReasoningEffort: "high"}); err == nil {
		t.Fatal("compatible provider silently accepted unsupported thinking selection")
	}
	if _, effective, err := store.ClientSnapshotForSelection(localChatModelSelection{Model: "compatible-model"}); err != nil || effective.Model != "compatible-model" || effective.Provider != view.Provider {
		t.Fatalf("compatible model choice: %+v %v", effective, err)
	}
}

func TestLocalModelSelectionRejectsInvalidValues(t *testing.T) {
	store := newLocalModelStore(t.TempDir())
	before, err := store.View()
	if err != nil {
		t.Fatal(err)
	}
	for _, selection := range []localChatModelSelection{
		{Model: "model with spaces"},
		{Model: strings.Repeat("x", 129)},
		{ReasoningEffort: "medium"},
	} {
		if _, _, err := store.ClientSnapshotForSelection(selection); err == nil {
			t.Fatalf("accepted invalid selection: %+v", selection)
		}
	}
	after, err := store.View()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("invalid selection changed saved settings: %+v %v", after, err)
	}
}

func TestLocalModelCatalogFallsBackToConfiguredModelWithoutKey(t *testing.T) {
	store := newLocalModelStore(t.TempDir())
	models, err := store.Models(context.Background())
	if err == nil || len(models) != 1 || models[0] != "deepseek-flash" {
		t.Fatalf("catalog fallback: models=%v err=%v", models, err)
	}
}
