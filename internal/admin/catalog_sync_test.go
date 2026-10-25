package admin

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
	"wechat-bot/internal/linuxnative"
)

type syncCatalogNative struct {
	*fakeNative
	starts   atomic.Int32
	hydrated []string
	block    chan struct{}
	started  chan struct{}
}

func (n *syncCatalogNative) StartCatalogSync(context.Context, string) (linuxnative.CatalogSyncOperation, error) {
	n.starts.Add(1)
	if n.started != nil {
		close(n.started)
	}
	if n.block != nil {
		<-n.block
	}
	return linuxnative.CatalogSyncOperation{ID: "catalog-op", State: "succeeded"}, nil
}
func (n *syncCatalogNative) CatalogSyncStatus(context.Context, string) (linuxnative.CatalogSyncOperation, error) {
	return linuxnative.CatalogSyncOperation{State: "succeeded"}, nil
}
func (n *syncCatalogNative) RefreshGroupMetadata(_ context.Context, id string) (linuxnative.CatalogSyncOperation, error) {
	n.hydrated = append(n.hydrated, id)
	var op linuxnative.CatalogSyncOperation
	op.State = "succeeded"
	op.Result.RecordCount = 2
	return op, nil
}
func (n *syncCatalogNative) Catalog(context.Context, string, string, string, string) (linuxnative.CatalogPage, error) {
	return linuxnative.CatalogPage{Items: []map[string]any{{"peerId": "room@chatroom"}, {"peerId": "new-room@chatroom"}, {"peerId": "friend"}, {"peerId": "new-room@chatroom"}}}, nil
}

func TestCatalogRefreshHydratesOnlyUnknownReceivedGroupsAndKeepsRules(t *testing.T) {
	f := setup(t)
	n := &syncCatalogNative{fakeNative: f.native}
	f.manager.native = n
	before, _ := json.Marshal(f.manager.Config())
	if code, _, _ := f.request("POST", "/api/catalog/sync", map[string]string{"kind": "groups"}, true, true); code != 401 {
		t.Fatal(code)
	}
	f.login()
	if code, _, _ := f.request("POST", "/api/catalog/sync", map[string]string{"kind": "groups"}, true, false); code != 403 {
		t.Fatal(code)
	}
	if code, _, _ := f.request("POST", "/api/catalog/sync", map[string]string{"kind": "arbitrary"}, true, true); code != 400 {
		t.Fatal(code)
	}
	if code, _, _ := f.request("POST", "/api/catalog/sync", map[string]string{"kind": "groups"}, true, true); code != 202 {
		t.Fatal(code)
	}
	job := waitCatalogJob(t, f)
	if job.State != "succeeded" || job.Hydrated != 1 || job.Records != 2 || len(n.hydrated) != 1 || n.hydrated[0] != "new-room@chatroom" {
		t.Fatal("new conversation not hydrated", job, n.hydrated)
	}
	after, _ := json.Marshal(f.manager.Config())
	if string(before) != string(after) {
		t.Fatal("catalog sync changed reply permissions")
	}
}

func waitCatalogJob(t *testing.T, f *webFixture) catalogRefresh {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		code, b, _ := f.request("GET", "/api/catalog/sync", nil, true, true)
		var job catalogRefresh
		if code != 200 || json.Unmarshal(b, &job) != nil {
			t.Fatal(code, string(b))
		}
		if job.State != "running" {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("catalog job did not finish")
	return catalogRefresh{}
}
func TestRepeatedCatalogRefreshReusesActiveJob(t *testing.T) {
	f := setup(t)
	n := &syncCatalogNative{fakeNative: f.native, block: make(chan struct{}), started: make(chan struct{})}
	f.manager.native = n
	f.login()
	_, b, _ := f.request("POST", "/api/catalog/sync", map[string]string{"kind": "contacts"}, true, true)
	var first catalogRefresh
	json.Unmarshal(b, &first)
	<-n.started
	_, b, _ = f.request("POST", "/api/catalog/sync", map[string]string{"kind": "groups"}, true, true)
	var again catalogRefresh
	json.Unmarshal(b, &again)
	if again.ID != first.ID || n.starts.Load() != 1 {
		t.Fatal("duplicate sync submitted")
	}
	close(n.block)
	job := waitCatalogJob(t, f)
	if job.Kind != "contacts" || len(n.hydrated) != 0 {
		t.Fatal(job)
	}
}
