package companion

import (
	"testing"
	"time"
)

func TestReconcileOnlyFalseLegacyNamespaceCollision(t *testing.T) {
	for _, blocked := range []string{"restored", "manual", "forgotten", "current_collision"} {
		t.Run(blocked, func(t *testing.T) {
			s, c, in := memoryFixture(t)
			now := time.Now().UTC().Truncate(time.Second)
			in.TraceID = "reused-local"
			in.Text = "legacy original"
			in.MessageTime = now.Add(-24 * time.Hour).Format(time.RFC3339)
			if err := s.Observe(in, c, now); err != nil {
				t.Fatal(err)
			}
			in.Sender = "current-member"
			in.Text = "current original"
			in.MessageTime = now.Format(time.RFC3339)
			in.Source = &SourceRef{InstanceID: s.SourceInstanceID(), MessageID: in.TraceID, ServerID: "actual-server", MessageType: 49}
			if err := s.Observe(in, c, now); err != nil {
				t.Fatal(err)
			}
			ref, ok := s.SourceForInput(in)
			if !ok {
				t.Fatal("source not created")
			}
			id := ScopeID(in.Channel, in.Account, in.Thread)
			before := s.Context(in, c, now).LastInteraction
			if err := s.scopeTransaction(id, func(data *persisted) error {
				sc := data.Scopes[id]
				for i := range sc.Messages {
					if sc.Messages[i].EventID == ref.EventID {
						markSourceConflict(&sc.Messages[i])
					}
				}
				data.Scopes[id] = sc
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			switch blocked {
			case "manual":
				if err := s.QuarantineSources(t.Context(), []SourceAudit{{EventID: ref.EventID, Scope: id, Revision: ref.Revision, Hash: ref.BodyHash}}); err != nil {
					t.Fatal(err)
				}
			case "forgotten":
				if _, err := s.db.Exec("INSERT INTO memory_blocks(scope,event_id) VALUES(?,?)", id, ref.EventID); err != nil {
					t.Fatal(err)
				}
			case "current_collision":
				other := in
				other.Sender = "different-current-member"
				other.Source = cloneSource(in.Source)
				other.Source.ServerID = "other-server"
				if err := s.Observe(other, c, now); err != nil {
					t.Fatal(err)
				}
			}
			got, restored, err := s.ReconcileLegacySource(t.Context(), in)
			if err != nil {
				t.Fatal(err)
			}
			if blocked != "restored" {
				if restored {
					t.Fatal("explicit block/current collision bypassed", blocked)
				}
				return
			}
			if !restored || got.EventID != ref.EventID || got.Revision != ref.Revision+1 || got.Integrity != "verified_import" {
				t.Fatal(got, restored)
			}
			event, err := s.ReadEvent(t.Context(), []string{id}, got.EventID)
			if err != nil || event.Text != "current original" || event.Hash != ref.BodyHash {
				t.Fatal("original changed", event, err)
			}
			var count int
			s.db.QueryRow("SELECT COUNT(*) FROM events WHERE trace=?", in.TraceID).Scan(&count)
			if count != 2 || !s.Context(in, c, now).LastInteraction.Equal(before) {
				t.Fatal("lost history or refreshed interaction", count)
			}
			if again, ok := s.SourceForInput(in); !ok || again.Revision != got.Revision {
				t.Fatal("restored source not resolvable", again, ok)
			}
			if _, changed, err := s.ReconcileLegacySource(t.Context(), in); err != nil || changed {
				t.Fatal("repair not idempotent", changed, err)
			}
		})
	}
}

func TestUnboundLegacyImportRequiresExactNativeMetadataAndNoBlocks(t *testing.T) {
	s, c, in := memoryFixture(t)
	in.MessageTime = time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	in.Text = "legacy presentation"
	if err := s.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	in.Source = &SourceRef{InstanceID: s.SourceInstanceID(), MessageID: in.TraceID, ServerID: "native-server"}
	if ok, err := s.CanImportUnboundNative(t.Context(), in); err != nil || !ok {
		t.Fatal(ok, err)
	}
	other := in
	other.Sender = "different-member"
	if ok, _ := s.CanImportUnboundNative(t.Context(), other); ok {
		t.Fatal("different native identity adopted legacy record")
	}
	if err := s.Clear(ScopeID(in.Channel, in.Account, in.Thread)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.CanImportUnboundNative(t.Context(), in); ok {
		t.Fatal("clear boundary bypassed by legacy import")
	}
}
