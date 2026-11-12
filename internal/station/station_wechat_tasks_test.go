package station

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/apphost"
)

type taskEvidenceNative struct {
	stationWechatNative
	connection apphost.Connection
}

func TestWechatTaskDocumentsRequireCurrentAttachmentGrant(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "queue"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "queue", "messages-v2.json")
	raw := []byte(`{"version":2,"server":"dataset","account":"one","media":[{"key":"f1","trace":"m1","kind":"file","state":"prepared","input":{"account":"one","thread":"group-a","source":{"account":"one","thread":"group-a","message_id":"file-message","message_type":49}}}]}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	id := "wechat-synthetic"
	worker := &stationWechatWorker{dir: dir, policy: stationWechatPolicy{AttachmentGrants: map[string]time.Time{}}}
	s := &stationWechat{ctx: context.Background(), workers: map[string]*stationWechatWorker{id: worker},
		stationWechatNative: taskEvidenceNative{connection: apphost.Connection{ID: id, AccountID: "one"}}}
	request := func(method, message string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/", strings.NewReader(message))
		if method == "POST" {
			r.Header.Set("Content-Type", "application/json")
		}
		r.SetPathValue("connection", id)
		r.SetPathValue("task", "media-f1")
		w := httptest.NewRecorder()
		if method == "GET" {
			s.readTaskDetail(w, r)
		} else {
			s.acquireTaskAttachment(w, r)
		}
		return w
	}
	if w := request("GET", ""); w.Code != 200 || strings.Contains(w.Body.String(), "file-message") {
		t.Fatal("ungranted source exposed", w.Code, w.Body.String())
	}
	worker.policy.AttachmentGrants["group-a"] = time.Now()
	if w := request("GET", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "file-message") {
		t.Fatal("granted source missing", w.Code, w.Body.String())
	}
	if w := request("POST", `{"message_id":"foreign-message"}`); w.Code != 404 {
		t.Fatal("browser replaced task source", w.Code)
	}
	delete(worker.policy.AttachmentGrants, "group-a")
	if w := request("POST", `{"message_id":"file-message"}`); w.Code != 404 {
		t.Fatal("revoked attachment triggered native access", w.Code)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(raw) {
		t.Fatal("document read changed task", err)
	}
}

func (n taskEvidenceNative) Inspect(context.Context, string) (apphost.ConnectionStatus, error) {
	return apphost.ConnectionStatus{Connection: n.connection}, nil
}

func TestWechatTaskReadKeepsConnectionAccountAndFiles(t *testing.T) {
	dir := t.TempDir()
	queue := filepath.Join(dir, "queue")
	if err := os.Mkdir(queue, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(queue, "messages-v2.json")
	raw := []byte(`{"version":2,"server":"synthetic-dataset","account":"one","media":[{"key":"file-task","trace":"original-message","kind":"file","state":"needs_review","input":{"account":"one","thread":"group-a"},"url":"C:/private/synthetic.docx"}]}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	id := "wechat-synthetic"
	native := taskEvidenceNative{connection: apphost.Connection{ID: id, AccountID: "one"}}
	s := &stationWechat{ctx: context.Background(), stationWechatNative: native, workers: map[string]*stationWechatWorker{id: {dir: dir}}}
	read := func(query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/?"+query, nil)
		r.SetPathValue("connection", id)
		w := httptest.NewRecorder()
		s.readTasks(w, r)
		return w
	}
	if w := read("limit=1000"); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := read("limit=0"); w.Code != 400 {
		t.Fatal(w.Code)
	}
	s.stationWechatNative = taskEvidenceNative{connection: apphost.Connection{ID: id, AccountID: "two"}}
	if w := read(""); w.Code != 503 {
		t.Fatal("foreign account could read tasks", w.Code)
	}
	s.stationWechatNative = taskEvidenceNative{connection: apphost.Connection{ID: "another", AccountID: "one"}}
	if w := read(""); w.Code != 404 {
		t.Fatal("foreign connection could read tasks", w.Code)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(raw) {
		t.Fatal("read altered checkpoint", err)
	}
}
