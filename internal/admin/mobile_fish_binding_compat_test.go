package admin

import (
	"encoding/json"
	"testing"
	"time"

	mobile "wechat-bot/internal/devicespec"
)

// A package from before 0.6.2 knows none of the optional identity keys.  Its
// start response must therefore be rejected before any device effect can be
// scheduled; accepting it would silently turn an explicit fish binding into a
// legacy owner task.
func TestMobileFishBindingPolicyRejectsLegacyRuntimeFieldOmission(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	expected := mobile.Task{ID: "fish-runtime-check", RequestHash: "fish-runtime-check", DeviceID: "synthetic", FishID: "fish-0123456789abcdef0123456789abcdef0123456789abcdef", DeviceBindingID: "fish-device-0123456789abcdef0123456789abcdef0123456789abcdef", Goal: "查看", Source: mobile.Source{Admin: true, MessageID: "fish-runtime-check"}, State: "queued", Created: now, Updated: now}
	input := mustJSON(expected)
	if err := json.Unmarshal(input, &expected); err != nil {
		t.Fatal(err)
	}
	policy := &mobileWorkflowPolicy{op: "start", input: input, snapshot: mobileWorkflowSnapshot{tasks: map[string]mobile.Task{}}, now: expected.Created}
	if err := policy.prove(mobileWorkflowPatch{Tasks: map[string]mobile.Task{expected.ID: expected}, Workflows: map[string]json.RawMessage{}, Result: mustJSON(expected)}); err != nil {
		t.Fatalf("correct fish-bound runtime result rejected: %v", err)
	}
	legacy := expected
	legacy.FishID, legacy.DeviceBindingID = "", ""
	if err := policy.prove(mobileWorkflowPatch{Tasks: map[string]mobile.Task{legacy.ID: legacy}, Workflows: map[string]json.RawMessage{}, Result: mustJSON(legacy)}); err == nil {
		t.Fatal("legacy runtime omitted fish binding fields but start proof accepted it")
	}
}

// The pre-fish owner flow deliberately retains its exact idempotency input;
// upgrading must not cause the same request to submit a second device task.
func TestMobileLegacyOwnerStartHashRemainsCompatible(t *testing.T) {
	x := newWorkflowFixture(t)
	source := mobile.Source{Admin: true, MessageID: "legacy-hash"}
	task, err := x.service.Start(source, "synthetic", "查看")
	if err != nil {
		t.Fatal(err)
	}
	want := namedSpeechSHA(mustJSON([]any{source, "synthetic", "查看"}))
	if task.RequestHash != want || task.FishID != "" || task.DeviceBindingID != "" {
		t.Fatalf("legacy task changed hash or gained identity task=%#v want=%s", task, want)
	}
	again, err := x.service.Start(source, "synthetic", "查看")
	if err != nil || again.ID != task.ID {
		t.Fatalf("same legacy request created another task: first=%s second=%s err=%v", task.ID, again.ID, err)
	}
}
