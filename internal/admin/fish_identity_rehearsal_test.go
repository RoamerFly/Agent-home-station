package admin

import (
	"reflect"
	"testing"
	"time"

	"wechat-bot/internal/companion"
)

// fishMigrationSnapshot is intentionally built from the public in-memory scope
// representation rather than files.  The rehearsal therefore checks the data
// the normal chat and memory routes will actually read after a restart.
type fishMigrationSnapshot struct {
	Account    string
	ScopeID    string
	Messages   []fishMigrationMessage
	Notes      []string
	Summary    string
	Compressed int
}

type fishMigrationMessage struct {
	TraceID       string
	Text          string
	Sender        string
	SourceEvent   string
	SourceMessage string
}

func snapshotFishMigration(t *testing.T, manager *Manager, thread string) fishMigrationSnapshot {
	t.Helper()
	scopeID := companion.ScopeID("wechat", manager.native.Account(), thread)
	scope, err := manager.memory.Read(scopeID, manager.Config().Companion, time.Now())
	if err != nil {
		t.Fatalf("read synthetic memory for %s: %v", manager.native.Account(), err)
	}
	got := fishMigrationSnapshot{
		Account: manager.native.Account(), ScopeID: scope.ID,
		Notes: append([]string(nil), scope.Notes...), Summary: scope.Summary,
		Compressed: scope.CompressedCount,
	}
	for _, turn := range scope.Messages {
		row := fishMigrationMessage{TraceID: turn.TraceID, Text: turn.Text, Sender: turn.Sender}
		if turn.Source != nil {
			row.SourceEvent = turn.Source.EventID
			row.SourceMessage = turn.Source.MessageID
		}
		got.Messages = append(got.Messages, row)
	}
	return got
}

func observeFishMigrationHistory(t *testing.T, manager *Manager, thread, sender, prefix string, at time.Time) fishMigrationSnapshot {
	t.Helper()
	for i, text := range []string{
		prefix + " 第一条已有经历",
		prefix + " 第二条来源记录",
	} {
		id := prefix + "-source-" + string(rune('a'+i))
		in := companion.Input{
			Channel: "wechat", Account: manager.native.Account(), Thread: thread,
			Sender: sender, Text: text, TraceID: id, MessageTime: at.Add(time.Duration(i) * time.Second).Format(time.RFC3339),
			Source: &companion.SourceRef{
				InstanceID: "synthetic-migration-dataset", EventID: id + "-event", MessageID: id,
				Account: manager.native.Account(), Thread: thread, Sender: sender,
				OccurredAt: at.Add(time.Duration(i) * time.Second).UTC().Format(time.RFC3339Nano),
			},
		}
		if err := manager.memory.Observe(in, manager.Config().Companion, at.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("observe %s %s: %v", manager.native.Account(), id, err)
		}
	}
	scopeID := companion.ScopeID("wechat", manager.native.Account(), thread)
	if err := manager.memory.Note(scopeID, prefix+" 已有备注"); err != nil {
		t.Fatalf("note %s: %v", manager.native.Account(), err)
	}
	return snapshotFishMigration(t, manager, thread)
}

// TestFishIdentityMigrationRehearsal exercises the exact recovery order used
// by the eventual local migration: pre-existing independent data, first bind,
// interruption before a second account is processed, restart, repeat, then a
// temporarily removed account restored from its preserved storage.  It uses
// only fixture-local SQLite and account directories.
func TestFishIdentityMigrationRehearsalKeepsExistingAccountData(t *testing.T) {
	f, registry := setupAccounts(t)
	second := registry.lookup("second")
	if second == nil || second.manager == nil {
		t.Fatal("synthetic second account was not available")
	}
	thread := "synthetic-migration@chatroom"
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	beforeTest := observeFishMigrationHistory(t, f.manager, thread, "wxid_owner", "test", at)
	beforeSecond := observeFishMigrationHistory(t, second.manager, thread, "wxid_owner", "second", at)
	if reflect.DeepEqual(beforeTest, beforeSecond) {
		t.Fatal("fixture did not establish independent account histories")
	}

	// First persisted operation succeeds, then the process stops before the
	// next account can be bound.  This is the migration interruption boundary.
	fishID := fishManage(t, f, "test", "bind_fish", 200)
	if fishID == "" {
		t.Fatal("first binding returned empty fish identity")
	}
	if rows := fishAccountRows(t, f); rows["test"] != fishID || rows["second"] != "" {
		t.Fatalf("partial binding view: %#v", rows)
	}
	f = restartFishFixture(t, f)
	registry = f.manager.accounts
	second = registry.lookup("second")
	if second == nil || second.manager == nil {
		t.Fatal("interrupted secondary account did not reopen")
	}
	if got := snapshotFishMigration(t, f.manager, thread); !reflect.DeepEqual(got, beforeTest) {
		t.Fatalf("primary data changed across interrupted migration\n got: %#v\nwant: %#v", got, beforeTest)
	}
	if got := snapshotFishMigration(t, second.manager, thread); !reflect.DeepEqual(got, beforeSecond) {
		t.Fatalf("secondary data changed before binding\n got: %#v\nwant: %#v", got, beforeSecond)
	}

	// Continue from the durable first operation. Repeating the second bind must
	// neither mint another fish nor duplicate existing source events.
	if got := fishManage(t, f, "second", "bind_fish", 200); got != fishID {
		t.Fatalf("continued binding joined %q, want %q", got, fishID)
	}
	if got := fishManage(t, f, "second", "bind_fish", 200); got != fishID {
		t.Fatalf("replayed binding changed fish identity: %q", got)
	}
	f = restartFishFixture(t, f)
	second = f.manager.accounts.lookup("second")
	if second == nil || second.manager == nil {
		t.Fatal("bound secondary account did not reopen")
	}
	if rows := fishAccountRows(t, f); rows["test"] != fishID || rows["second"] != fishID {
		t.Fatalf("restart lost completed binding: %#v", rows)
	}
	if got := snapshotFishMigration(t, f.manager, thread); !reflect.DeepEqual(got, beforeTest) {
		t.Fatalf("primary data changed after completed migration\n got: %#v\nwant: %#v", got, beforeTest)
	}
	if got := snapshotFishMigration(t, second.manager, thread); !reflect.DeepEqual(got, beforeSecond) {
		t.Fatalf("secondary data changed after completed migration\n got: %#v\nwant: %#v", got, beforeSecond)
	}

	// A soft-removed account is deliberately absent from active identity views,
	// but its pre-migration data and binding must return when it is restored.
	fishManage(t, f, "second", "remove", 200)
	if rows := fishAccountRows(t, f); rows["second"] != "" || rows["test"] != fishID {
		t.Fatalf("removed account leaked active identity: %#v", rows)
	}
	f = restartFishFixture(t, f)
	fishManage(t, f, "second", "restore", 200)
	second = f.manager.accounts.lookup("second")
	if second == nil || second.manager == nil {
		t.Fatal("restored account did not reopen")
	}
	if rows := fishAccountRows(t, f); rows["second"] != fishID || rows["test"] != fishID {
		t.Fatalf("restored account did not recover binding: %#v", rows)
	}
	if got := snapshotFishMigration(t, second.manager, thread); !reflect.DeepEqual(got, beforeSecond) {
		t.Fatalf("restored account data changed\n got: %#v\nwant: %#v", got, beforeSecond)
	}
}
