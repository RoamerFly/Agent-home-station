package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
)

type ownerReceiptNative struct {
	*fakeTools
	accepted bool
}

func (n *ownerReceiptNative) LookupSend(context.Context, string) (linuxnative.ToolStatus, error) {
	state := "unknown"
	if n.accepted {
		state = "accepted"
	}
	return linuxnative.ToolStatus{ID: "receipt", ThreadID: "filehelper", State: state, ServerID: "123"}, nil
}
func TestOwnerSurfReceiptImportRequiresNativeAcceptanceAndDoesNotReplay(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "accepted"}[accepted], func(t *testing.T) {
			f := setup(t)
			n := &ownerReceiptNative{fakeTools: &fakeTools{fakeNative: f.native}, accepted: accepted}
			f.manager.native = n
			f.manager.config.Companion.Permissions.Remember = true
			session, thread, candidate := "surf-owner", "filehelper", "one"
			x := mcptools.DiscoveryItem{ID: candidate, Title: "猫咪看雪", URL: "https://example.org/cat", Kind: "video"}
			f.manager.surfHistory.data.Sessions = []surfSession{{ID: session, Thread: thread, Candidates: []mcptools.DiscoveryItem{x}, State: "not_shared", At: time.Now()}}
			f.manager.surfHistory.data.Items = []surfSeen{{Item: x, Shared: map[string]time.Time{}}}
			digest := sha256.Sum256([]byte("manual-surf-v1\x00" + session + "\x00" + candidate + "\x00" + thread))
			key := "manual-surf-" + hex.EncodeToString(digest[:])[:40]
			dir := filepath.Join(f.dir, "manual-surf-claims")
			os.MkdirAll(dir, 0700)
			// A locally claimed success alone is never evidence of acceptance.
			b, _ := json.Marshal(map[string]any{"session_id": session, "candidate_id": candidate, "thread": thread, "key": key, "status": map[string]string{"state": "accepted"}})
			os.WriteFile(filepath.Join(dir, key+".json"), b, 0600)
			f.manager.importOwnerSurfReceipts(context.Background())
			f.manager.importOwnerSurfReceipts(context.Background())
			got := f.manager.surfHistory.snapshot().Sessions[0]
			if (got.State == "shared") != accepted {
				t.Fatal(got)
			}
			count := 0
			for _, row := range f.manager.Logs() {
				if row.Event == "message_sent" {
					count++
				}
			}
			if accepted && count != 1 || !accepted && count != 0 || n.submits.Load() != 0 {
				t.Fatal("replayed or invented send", count, n.submits.Load())
			}
		})
	}
}
