package station

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/documentmemory"
)

func TestLocalDocumentMemoryPersistsAcrossRestart(t *testing.T) {
	f := localChatFixture(t)
	access := &localWorkspaceAccess{home: f.handler.home}
	grant, err := access.resolve()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(grant.Directory, "works", "note.txt")
	os.WriteFile(path, []byte("synthetic document"), 0600)
	store := f.handler.home.artifacts
	a, err := store.publishFile(t.Context(), localArtifactInput{Path: "works/note.txt", Key: "note-test"}, nil, access, grant, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := &localToolSet{access: access, run: companion.NewGuardedToolRun(func(context.Context) error { return nil }), traceID: "synthetic-request"}
	raw, _ := json.Marshal(documentmemory.Input{Action: "save", Asset: a.ID, Summary: "标题调整为居中", Requirements: "保留正文"})
	if _, err = tools.documentMemory(t.Context(), raw); err != nil {
		t.Fatal(err)
	}
	next := localRestartChat(t, f)
	tools = &localToolSet{access: &localWorkspaceAccess{home: next.handler.home}, run: companion.NewGuardedToolRun(func(context.Context) error { return nil }), traceID: "next-request"}
	read, _ := json.Marshal(documentmemory.Input{Action: "read", Asset: a.ID})
	result, err := tools.documentMemory(t.Context(), read)
	if err != nil || !strings.Contains(result, "标题调整") {
		t.Fatal(result, err)
	}
	if _, err = tools.documentMemory(t.Context(), raw); err == nil {
		t.Fatal("stale note update accepted")
	}
	current, err := next.handler.home.artifacts.read(a.ID)
	if err != nil || current.SHA256 != a.SHA256 || current.ID != a.ID {
		t.Fatal("note changed file identity", err)
	}
}
