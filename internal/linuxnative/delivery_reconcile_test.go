package linuxnative

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type reconcileCatalog struct {
	page CatalogPage
	err  error
}

func (n reconcileCatalog) Catalog(_ context.Context, kind, thread, search, cursor string) (CatalogPage, error) {
	if kind != "messages" || thread != "room@chatroom" {
		panic("wrong scope")
	}
	return n.page, n.err
}
func TestReconcileRequiresRealOutgoingIdentityNotIntentOrDuration(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	status := ToolStatus{ThreadID: "room@chatroom", CreatedAt: now.Add(-time.Minute).Format(time.RFC3339), VoiceChunks: []VoiceChunkStatus{{Index: 0, State: "accepted", HasReceipt: true, ServerID: "123"}, {Index: 1, ClientID: "same-plan-part-1", EncodedMD5: strings.Repeat("a", 32), SubmittedAtMS: now.UnixMilli(), DurationMS: 42000}}}
	row := map[string]any{"threadId": "room@chatroom", "direction": "outgoing", "serverId": "456", "createdAt": now.Format(time.RFC3339), "voice_duration_ms": "42000", "body": "queued local memory"}
	n := reconcileCatalog{page: CatalogPage{Items: []map[string]any{row}}}
	p, e := ReconcileMedia(context.Background(), n, status)
	if e != nil || !p.Complete || len(p.Matched) != 0 {
		t.Fatal("duration or intent treated as delivered", p, e)
	}
	row["voice_client_id"] = "same-plan-part-1"
	p, e = ReconcileMedia(context.Background(), n, status)
	if e != nil || len(p.Matched) != 1 || p.Matched[0] != 1 {
		t.Fatal(p, e)
	}
	row["direction"] = "incoming"
	p, _ = ReconcileMedia(context.Background(), n, status)
	if len(p.Matched) != 0 {
		t.Fatal("another sender counted")
	}
	n.err = errors.New("offline")
	p, e = ReconcileMedia(context.Background(), n, status)
	if e == nil || p.Complete {
		t.Fatal("read failure counted as healthy absence")
	}
}
func TestReconcileMediaJobReadFailureNeverSubmits(t *testing.T) {
	writes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writes++
			t.Error("unexpected write")
		}
		if strings.Contains(r.URL.Path, "/sends/job") {
			w.Write([]byte(`{"id":"job","accountId":"test","state":"needs_review","threadId":"room@chatroom"}`))
			return
		}
		w.WriteHeader(503)
	}))
	defer srv.Close()
	_, err := testSender(t, srv.URL).ReconcileMediaJob(context.Background(), "job")
	if err == nil || writes != 0 {
		t.Fatal(err, writes)
	}
}
