package bot

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTaskEvidencePagesKeepIdentityAndExcludeBodies(t *testing.T) {
	raw := `{"version":2,"server":"dataset","account":"one",
	"inbox":[{"event":{"data":{"messageId":"m1","threadId":"group-a"}},"phase":"generating","recovery_tools":[{"result":"private tool result"}]}],
	"utterance_plans":[{"id":"p1","trace":"m1","state":"waiting","input":{"account":"one","thread":"group-a","text":"private input"},"messages":["private response"]}],
	"media":[{"key":"f1","trace":"m2","kind":"file","state":"needs_review","input":{"account":"one","thread":"group-b"},"url":"C:/private/file.docx","title":"private title"}]}`
	first, err := ReadTaskEvidencePageFrom(strings.NewReader(raw), "one", "", "", 2)
	if err != nil || len(first.Items) != 2 || first.NextAfter == "" {
		t.Fatal(first, err)
	}
	second, err := ReadTaskEvidencePageFrom(strings.NewReader(raw), "one", "", first.NextAfter, 2)
	if err != nil || len(second.Items) != 1 || second.NextAfter != "" {
		t.Fatal(second, err)
	}
	filtered, err := ReadTaskEvidencePageFrom(strings.NewReader(raw), "one", "group-b", "", 1000)
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].Receipt != "f1" || filtered.Limit != 1000 {
		t.Fatal(filtered, err)
	}
	public, err := json.Marshal(first)
	if err != nil || strings.Contains(string(public), "private") {
		t.Fatal("private content entered projection", err)
	}
	for _, invalid := range []string{
		strings.Replace(raw, `"account":"one"`, `"account":"other"`, 1),
		strings.ReplaceAll(raw, `"account":"one","thread":"group-a"`, `"account":"other","thread":"group-a"`),
		strings.Replace(raw, `"id":"p1"`, `"id":""`, 1),
		strings.Replace(raw, `"version":2`, `"version":99`, 1),
		`{"version":2,"server":"dataset","account":"one","media":[null]}`,
		`{"version":2,"server":"dataset","account":"one","inbox":[{"event":{"data":{"messageId":"m","threadId":"t"}}},{"event":{"data":{"messageId":"m","threadId":"t"}}}]}`,
	} {
		if page, err := ReadTaskEvidencePageFrom(strings.NewReader(invalid), "one", "", "", 40); err == nil || len(page.Items) != 0 {
			t.Fatal("invalid checkpoint exposed tasks", page, err)
		}
	}
	followups := `{"version":1,"server":"dataset","account":"one",
	"deliveries":[{"key":"d1","trace":"m1","state":"accepted","input":{"account":"one","thread":"t"},"text":"private reply"}],
	"media_replies":[{"trace":"m2","state":"waiting","input":{"account":"one","thread":"t"},"messages":["private followup"]}],
	"conversation_wakes":[{"parent":"m3","state":"waiting","input":{"account":"one","thread":"t"},"purpose":"private purpose"}]}`
	continuations, err := ReadTaskEvidencePageFrom(strings.NewReader(followups), "one", "", "", 40)
	if err != nil || len(continuations.Items) != 3 {
		t.Fatal(continuations, err)
	}
	public, _ = json.Marshal(continuations)
	if strings.Contains(string(public), "private") {
		t.Fatal("continuation body entered task list")
	}
}
