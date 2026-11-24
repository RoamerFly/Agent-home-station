package bot

import (
	"fmt"
	"testing"
	"time"
)

func TestFailedUnreadBatchRecoversOnceWithoutEffects(t *testing.T) {
	w := newTestWorker(t, &fakeSource{}, &fakeModel{}, &fakeSender{})
	opts := DefaultOptions()
	opts.RecoverBacklog = true
	w.config.Options = &opts
	w.runtime = &runtimeState{Version: 1, Server: "server-1", Account: "test"}
	for i := 0; i < 43; i++ {
		w.runtime.Inbox = append(w.runtime.Inbox, inboxItem{Event: fixtureEvent(fmt.Sprint(i), fmt.Sprintf("recent-%d", i), "room@chatroom", "incoming", "remote"), Backlog: true, BacklogParent: "recent-42", Phase: "failed", Updated: time.Now().Add(-time.Minute)})
	}
	w.runtime.Inbox = append(w.runtime.Inbox, inboxItem{Event: fixtureEvent("old", "old", "room@chatroom", "incoming", "remote"), Backlog: true, BacklogParent: "old", Phase: "failed", Updated: time.Now().Add(-48 * time.Hour)})
	if !w.recoverSafeFailedBacklogs() {
		t.Fatal("recent effect-free batch was not recovered")
	}
	for i := 0; i < 43; i++ {
		item := w.runtime.Inbox[i]
		if item.Phase != "received" || !item.BacklogRecoveryAttempted || item.BacklogParent != "" {
			t.Fatalf("recent item %d not restored safely", i)
		}
	}
	if w.runtime.Inbox[43].Phase != "failed" || w.recoverSafeFailedBacklogs() {
		t.Fatal("old batch or a repeated recovery was scheduled")
	}
}
