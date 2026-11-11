package adapter

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManagementRoutesKeepArchiveReadOnly(t *testing.T) {
	called := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Fixture-Connection", r.PathValue("connection"))
		w.WriteHeader(http.StatusNoContent)
	}
	protect := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Fixture-Owner") != "authorized" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}
	active, archive, unprotected := http.NewServeMux(), http.NewServeMux(), http.NewServeMux()
	evidence := ArchiveManagementHandlers{
		ListLegacyReceipts: called, ReadLegacyReceipt: called, ReadRelocationReceipt: called,
		ReadLegacySurf: called,
	}
	handlers := ManagementHandlers{
		Contacts: called, SubmitRecall: called, CancelDelivery: called, ResumeDelivery: called,
		Activation: called, RelocationReadiness: called, MediaSettings: called, Archive: evidence,
	}
	handlers.Mount(active, protect)
	evidence.Mount(archive, protect)
	handlers.Mount(unprotected, nil)
	const base = "/api/apps/wechat/connections/fixture"
	for _, tc := range []struct {
		method, path string
		archiveCode  int
	}{
		{http.MethodGet, "/legacy-import/receipts", http.StatusNoContent},
		{http.MethodGet, "/legacy-import/receipts/original", http.StatusNoContent},
		{http.MethodGet, "/legacy-import/relocation/receipts/original", http.StatusNoContent},
		{http.MethodGet, "/legacy-import/surf", http.StatusNoContent},
		{http.MethodGet, "/contacts", http.StatusNotFound},
		{http.MethodPost, "/receipts/original/recall", http.StatusNotFound},
		{http.MethodPost, "/receipts/original/cancel", http.StatusNotFound},
		{http.MethodPost, "/receipts/original/resume", http.StatusNotFound},
		{http.MethodPost, "/legacy-import/activation", http.StatusNotFound},
		{http.MethodPost, "/legacy-import/relocation/readiness", http.StatusNotFound},
		{http.MethodPut, "/media-understanding", http.StatusNotFound},
	} {
		r := httptest.NewRequest(tc.method, base+tc.path, nil)
		rejected := httptest.NewRecorder()
		active.ServeHTTP(rejected, r)
		if rejected.Code != http.StatusUnauthorized || rejected.Header().Get("Fixture-Connection") != "" {
			t.Errorf("%s %s: active route bypassed protection: %d", tc.method, tc.path, rejected.Code)
		}
		r.Header.Set("Fixture-Owner", "authorized")
		accepted := httptest.NewRecorder()
		active.ServeHTTP(accepted, r)
		if accepted.Code != http.StatusNoContent || accepted.Header().Get("Fixture-Connection") != "fixture" {
			t.Errorf("%s %s: active route lost its handler or connection: %d", tc.method, tc.path, accepted.Code)
		}
		archived := httptest.NewRecorder()
		archive.ServeHTTP(archived, r)
		if archived.Code != tc.archiveCode {
			t.Errorf("%s %s: archive route got %d, want %d", tc.method, tc.path, archived.Code, tc.archiveCode)
		}
		disabled := httptest.NewRecorder()
		unprotected.ServeHTTP(disabled, r)
		if disabled.Code != http.StatusNotFound {
			t.Errorf("%s %s: route mounted without protection: %d", tc.method, tc.path, disabled.Code)
		}
	}
	r := httptest.NewRequest(http.MethodPost, base+"/legacy-import/receipts/original", nil)
	denied := httptest.NewRecorder()
	archive.ServeHTTP(denied, r)
	if denied.Code != http.StatusMethodNotAllowed {
		t.Fatal("archive receipt accepted a write method", denied.Code)
	}
}
