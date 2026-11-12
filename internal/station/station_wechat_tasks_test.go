package station

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"wechat-bot/internal/apphost"
)

type taskEvidenceNative struct {
	stationWechatNative
	connection apphost.Connection
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
