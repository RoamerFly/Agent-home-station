package companion

import (
	"testing"
	"time"
)

func revisedResolutionFixture(t *testing.T) (*Store, Input, Input) {
	t.Helper()
	s, c, in := memoryFixture(t)
	now := time.Now().UTC()
	original := "first verified request"
	in.Text, in.OriginalText, in.MessageTime = original, &original, now.Format(time.RFC3339Nano)
	in.Source = &SourceRef{InstanceID: s.SourceInstanceID(), MessageID: in.TraceID, ServerID: "native-resolution-source", MessageType: 1, OccurredAt: in.MessageTime, Revision: 1}
	if err := s.Observe(in, c, now); err != nil {
		t.Fatal(err)
	}
	ref, ok := s.SourceForInput(in)
	if !ok {
		t.Fatal("initial canonical source missing")
	}
	old := in
	old.Source = cloneSource(ref)
	next := in
	next.Source = cloneSource(ref)
	next.Source.Revision++
	corrected := "corrected verified request"
	next.Text, next.OriginalText = corrected, &corrected
	if err := s.Observe(next, c, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	return s, old, next
}

func TestSourceResolutionCannotPromoteStaleRevisionToCurrentProof(t *testing.T) {
	s, old, current := revisedResolutionFixture(t)
	if ref, ok := s.SourceForInput(old); ok {
		t.Fatal("stale v1 input silently obtained v2 source proof", ref)
	}
	// A caller explicitly presenting the new revision also needs its current
	// canonical body identity, rather than the old persisted v1 hash.
	current.Source.BodyHash = sourceHash(*current.OriginalText)
	if ref, ok := s.SourceForInput(current); !ok || ref.Revision != 2 {
		t.Fatal("current verified correction failed to resolve", ref, ok)
	}
}

func TestSourceResolutionChecksEachProvidedCanonicalConstraint(t *testing.T) {
	s, old, current := revisedResolutionFixture(t)
	current.Source.BodyHash = sourceHash(*current.OriginalText)
	for _, field := range []string{"revision", "event_id", "body_hash", "message_id", "account", "thread", "sender", "source_time", "literal_body"} {
		t.Run(field, func(t *testing.T) {
			candidate := current
			candidate.Source = cloneSource(current.Source)
			switch field {
			case "revision":
				candidate.Source.Revision = old.Source.Revision
			case "event_id":
				candidate.Source.EventID = "unrelated-persisted-event"
			case "body_hash":
				candidate.Source.BodyHash = old.Source.BodyHash
			case "message_id":
				candidate.Source.MessageID = "different-native-message"
			case "account":
				candidate.Source.Account = "foreign-account"
			case "thread":
				candidate.Source.Thread = "foreign-room"
			case "sender":
				candidate.Source.Sender = "different-sender"
			case "source_time":
				candidate.Source.OccurredAt = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
			case "literal_body":
				candidate.OriginalText = old.OriginalText
			}
			if ref, ok := s.SourceForInput(candidate); ok {
				t.Fatal("trace equality bypassed provided canonical constraint", field, ref)
			}
		})
	}
	// Source identity survives a different execution trace, and equal instants
	// survive the native/canonical timezone representation change.
	current.TraceID = "new-execution-trace"
	stamp, err := time.Parse(time.RFC3339Nano, current.Source.OccurredAt)
	if err != nil {
		t.Fatal(err)
	}
	current.Source.OccurredAt = stamp.In(time.FixedZone("native-zone", 8*3600)).Format(time.RFC3339Nano)
	current.MessageTime = current.Source.OccurredAt
	if ref, ok := s.SourceForInput(current); !ok || ref.EventID != current.Source.EventID {
		t.Fatal("valid canonical identity rejected after trace/timezone change", ref, ok)
	}
}

func TestSourceResolutionReliableOriginalPreventsUnversionedStaleBody(t *testing.T) {
	s, old, current := revisedResolutionFixture(t)
	// First-time/native references may omit canonical version/hash metadata;
	// an explicitly retained complete original still binds the actual body.
	old.Source.Revision, old.Source.EventID, old.Source.BodyHash = 0, "", ""
	if ref, ok := s.SourceForInput(old); ok {
		t.Fatal("stale complete literal silently upgraded without version metadata", ref)
	}
	current.Source.Revision, current.Source.EventID, current.Source.BodyHash = 0, "", ""
	if ref, ok := s.SourceForInput(current); !ok || ref.Revision != 2 {
		t.Fatal("current unversioned complete literal failed to resolve", ref, ok)
	}
}

func TestSourceResolutionAllowsRawNativeMediaHashButNotCanonicalHashMismatch(t *testing.T) {
	s, c, in := memoryFixture(t)
	empty := ""
	now := time.Now().UTC()
	in.Text = "[语音转写] 未知说话人的录音正文"
	in.OriginalText = &empty
	in.MessageTime = now.In(time.FixedZone("native-zone", 8*3600)).Format(time.RFC3339Nano)
	in.Source = &SourceRef{InstanceID: s.SourceInstanceID(), MessageID: in.TraceID, ServerID: "native-voice", MessageType: 34, Revision: 1, OccurredAt: in.MessageTime, BodyHash: sourceHash("<msg><voicemsg synthetic='raw-native-body'/></msg>")}
	in.IncomingMedia = []IncomingMediaRef{{Source: *in.Source, AssetID: "safe-native-voice", Kind: "voice", UploaderID: in.Sender, Observation: in.Text, ObservationState: "observed", ObservationVersion: 1}}
	if err := s.Observe(in, c, now); err != nil {
		t.Fatal(err)
	}
	dataBefore, err := s.DataEpoch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	recallBefore := recallEpochForTest(t, s)
	canonical, ok := s.SourceForInput(in)
	if !ok || canonical.BodyHash != sourceHash("") || canonical.Revision != 1 {
		t.Fatal("first native raw-XML hash blocked a valid media original", canonical, ok)
	}
	prepared := in
	prepared.Text = "[ASR预览更新] 这不是原始用户正文"
	prepared.OriginalText = nil
	if ref, ok := s.SourceForInput(prepared); !ok || ref.BodyHash != sourceHash("") {
		t.Fatal("display/ASR wrapper was incorrectly used as an original-body constraint", ref, ok)
	}
	prepared.Source = cloneSource(canonical)
	prepared.Source.BodyHash = in.Source.BodyHash
	if ref, ok := s.SourceForInput(prepared); ok {
		t.Fatal("persisted canonical EventID ignored mismatched canonical hash", ref)
	}
	prepared.Source = cloneSource(canonical)
	prepared.OriginalText = &empty
	if ref, ok := s.SourceForInput(prepared); !ok || ref.EventID != canonical.EventID {
		t.Fatal("valid canonical empty-original media proof rejected", ref, ok)
	}
	if dataAfter, e := s.DataEpoch(t.Context()); e != nil || dataAfter != dataBefore || recallEpochForTest(t, s) != recallBefore {
		t.Fatal("source lookup re-signed or mutated evidence state", dataAfter, e)
	}
}
