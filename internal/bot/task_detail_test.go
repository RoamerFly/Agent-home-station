package bot

import (
	"strings"
	"testing"
)

func TestTaskDetailKeepsFileSourcesWithinTaskAccountAndConversation(t *testing.T) {
	raw := `{"version":2,"server":"dataset","account":"one","media":[{
	"key":"f1","trace":"m1","kind":"file","state":"needs_review",
	"input":{"account":"one","thread":"group-a","source":{"account":"one","thread":"group-a","message_id":"file-message","message_type":49,"body_hash":"saved-body-hash"},
	"quoted_source":{"account":"one","thread":"group-b","message_id":"foreign-message","message_type":49}}}]}`
	detail, err := ReadTaskDetailFrom(strings.NewReader(raw), "one", "media-f1")
	if err != nil || detail.Task.Message != "m1" || len(detail.Sources) != 1 ||
		detail.Sources[0].MessageID != "file-message" || detail.Sources[0].BodyHash != "saved-body-hash" {
		t.Fatal(detail, err)
	}
	if _, err := ReadTaskDetailFrom(strings.NewReader(raw), "two", "media-f1"); err == nil {
		t.Fatal("another account read the task")
	}
	if _, err := ReadTaskDetailFrom(strings.NewReader(raw), "one", "media-unknown"); err == nil {
		t.Fatal("unknown task resolved")
	}
	foreign := strings.Replace(raw, `"source":{"account":"one"`, `"source":{"account":"two"`, 1)
	detail, err = ReadTaskDetailFrom(strings.NewReader(foreign), "one", "media-f1")
	if err != nil || len(detail.Sources) != 0 {
		t.Fatal("foreign source leaked", detail, err)
	}
}
