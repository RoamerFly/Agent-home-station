package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/linuxnative"
)

func TestConnectedActionLostResponseCanOnlyBeInspected(t *testing.T) {
	const account, group, key = "fixture", "fixture-room@chatroom", "original-action"
	var writes, reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/accounts/"+account+"/groups/"+group:
			writes.Add(1)
			var request struct{ Name string }
			if r.Header.Get("Idempotency-Key") != key || json.NewDecoder(r.Body).Decode(&request) != nil || request.Name != "fixture name" {
				t.Error("reserved request changed before dispatch")
			}
			http.Error(w, "synthetic lost response", http.StatusServiceUnavailable)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/accounts/"+account+"/group-mutations/by-idempotency-key" && r.URL.Query().Get("key") == key:
			reads.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "native-original", "accountId": account, "state": "succeeded", "kind": "group_mutation",
				"result": map[string]string{"action": "rename", "targetId": group, "remoteOutcome": "accepted"},
			})
		default:
			t.Errorf("unexpected native request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("synthetic-action-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	native, err := linuxnative.New(linuxnative.Config{URL: server.URL, TokenFile: token, Account: account})
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := OpenActionLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	ref := apphost.ConnectionRef{AppID: ID, InstallationID: "local", ConnectionID: ConnectionID(account), AccountID: account}
	p := ActionProposal{
		ID: key, Account: account, State: "unknown", Request: linuxnative.AdminAction{Kind: "rename", ThreadID: group, Text: "fixture name"},
		AppAction: &apphost.ActionIntent{ConnectionRef: ref, Action: "rename", Target: group, IdempotencyKey: key},
	}
	submit, err := ledger.ReserveConnected(p, native)
	if err != nil {
		t.Fatal(err)
	}
	p.AppAction.IdempotencyKey = "redirected"
	if got, err := submit(t.Context()); err != nil || got.State != "submission_unknown" || got.PlatformOperationID != "" {
		t.Fatal("a lost response did not leave an inspectable reservation", got, err)
	}
	if _, err := submit(t.Context()); !errors.Is(err, apphost.ErrLifecycleConflict) {
		t.Fatal("the submit function could be used again", err)
	}
	restored, err := OpenActionLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	old, found := restored.FindConnected(ref.InstallationID, ref.ConnectionID, key, "", "")
	if !found {
		t.Fatal("the original key did not survive reopening")
	}
	if _, err := restored.ReserveConnected(old, native); !errors.Is(err, apphost.ErrLifecycleConflict) {
		t.Fatal("the reopened reservation admitted a second submission", err)
	}
	read := func(_ context.Context, connection string) (*linuxnative.Sender, error) {
		if connection != ref.ConnectionID {
			t.Fatal("lookup moved to another connection")
		}
		return native, nil
	}
	audit := ProjectRollbackActions(t.Context(), native, []ActionProposal{old})
	if len(audit) != 1 || audit[0].Lookup != "platform" || audit[0].State != "succeeded" {
		t.Fatal("rollback could not verify the original receipt", audit)
	}
	stored, _ := restored.Read(key, account)
	if stored.Receipt().State != "submission_unknown" {
		t.Fatal("rollback projection changed the local ledger")
	}
	got := restored.RefreshConnected(t.Context(), old, read)
	if got.Lookup != "platform" || got.State != "succeeded" || got.PlatformOperationID != "native-original" || writes.Load() != 1 || reads.Load() != 2 {
		t.Fatal("original lookup did not recover the receipt without another write", got, writes.Load(), reads.Load())
	}
}
