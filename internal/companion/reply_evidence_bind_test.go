package companion

import (
	"encoding/json"
	"testing"
)

func TestPrivateReplyAndMediaBindUseStructuralProofWithoutSemanticCall(t *testing.T) {
	s, c, _ := memoryFixture(t)
	in := groundedInput(t, s, c, "alpha", "private-binding", "请按原请求继续处理")
	in.IsGroup = false
	model := &groundingSequenceModel{}
	access := &memoryAuthorizationFixture{stamp: "private-grant"}
	e := &Engine{Store: s, Config: c, Tools: access, model: model}
	d, err := e.DiagnoseGroundedReply(t.Context(), in, Decision{Action: "reply", Messages: []string{"收到，我会按指定声线处理。"}})
	if err != nil || len(d.ReplyContexts) != 1 || model.calls != 0 {
		t.Fatal("private reply did not obtain structural proof without review", d, err, model.calls)
	}
	r := d.ReplyContexts[0]
	if r.EvidenceEpoch == "" || r.EvidenceAuthorization != access.stamp || len(r.SourceVersions) != 1 {
		t.Fatal("private proof lost source or evidence version", r)
	}
	media, err := e.BindReplyEvidence(WithDecisionGenerationEvidence(t.Context(), d), in)
	if err != nil || media.EvidenceEpoch != r.EvidenceEpoch || model.calls != 0 {
		t.Fatal("media creation changed evidence or used a semantic model call", media, err)
	}
	in.ReplyContext = &media
	before, _ := json.Marshal(media)
	access.stamp = "revoked-private-grant"
	if _, err = e.BindReplyEvidence(t.Context(), in); err == nil {
		t.Fatal("existing private media evidence was re-signed after revocation")
	}
	after, _ := json.Marshal(media)
	if string(before) != string(after) {
		t.Fatal("validation mutated persisted media evidence")
	}
}

func TestBindingGeneratedDraftNeverMintsCurrentStampAfterInvalidation(t *testing.T) {
	s, c, _ := memoryFixture(t)
	in := groundedInput(t, s, c, "alpha", "generated-binding", "你好")
	in.IsGroup = false
	model := &groundingSequenceModel{}
	e := &Engine{Store: s, Config: c, model: model}
	original, err := e.captureGenerationEvidence(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	d := Decision{Action: "reply", Messages: []string{"你好"}, generationEvidence: &original}
	if _, err = s.db.Exec("INSERT INTO meta(key,value) VALUES('recall_epoch','1') ON CONFLICT(key) DO UPDATE SET value=CAST(value AS INTEGER)+1"); err != nil {
		t.Fatal(err)
	}
	if _, err = e.DiagnoseGroundedReply(t.Context(), in, d); err == nil {
		t.Fatal("private generated draft was signed against a new epoch")
	}
	if _, err = e.BindReplyEvidence(WithDecisionGenerationEvidence(t.Context(), d), in); err == nil {
		t.Fatal("generated media was signed against a new epoch")
	}
	if model.calls != 0 {
		t.Fatal("invalid evidence reached semantic model", model.calls)
	}
}

func TestDedicatedGroundingResultRejectsPermissionChangesDuringCall(t *testing.T) {
	s, c, _ := memoryFixture(t)
	access := &memoryAuthorizationFixture{stamp: "grant-before"}
	model := mutatingVerdictModel{mutate: func() { access.stamp = "grant-after" }}
	e := &Engine{Store: s, Config: c, Tools: access, model: model}
	if _, err := e.groundingChat(t.Context(), groundingCheckPolicy, []map[string]string{{"role": "user", "content": `{}`}}); err == nil || err.Error() != "generation_evidence_changed" {
		t.Fatal("review result survived a changed authorization", err)
	}
}
