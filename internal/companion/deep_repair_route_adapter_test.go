package companion

// Offline consumer checks only. The frozen live five-round candidate and all
// 44/240/core stimuli, semantic gold and thresholds are unchanged.
import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/deepseek"
)

type deepRouteAdapterProbe struct {
	deepStructuredFake
	Target, Text, Selected string
	InvalidRoute           bool
	EditorPacket           map[string]json.RawMessage
	Checks, Edits          int
}

func (m *deepRouteAdapterProbe) ChatGrounding(_ context.Context, _ string, turns []map[string]string, _ deepseek.GroundingOptions) (string, error) {
	if len(turns) != 1 {
		return "", fmt.Errorf("expected one actual synthetic packet")
	}
	var packet map[string]json.RawMessage
	if err := json.Unmarshal([]byte(turns[0]["content"]), &packet); err != nil {
		return "", err
	}
	if raw, ok := packet["reply_routes"]; ok {
		m.Edits++
		m.EditorPacket = packet
		var routes []replyRoute
		if err := json.Unmarshal(raw, &routes); err != nil {
			return "", err
		}
		for _, route := range routes {
			if reflect.DeepEqual(route.TargetIDs, []string{m.Target}) {
				m.Selected = route.ID
				break
			}
		}
		if m.Selected == "" {
			return "", fmt.Errorf("host did not offer the real current target")
		}
		if m.InvalidRoute {
			m.Selected = "K1" // A fact reference must never become a route.
		}
		out, err := json.Marshal(routeEditedReply{Action: "reply", Bubbles: []editedReplyBubble{{Text: m.Text, RouteID: m.Selected}}})
		return string(out), err
	}
	m.Checks++
	if m.Checks > 1 {
		return syntheticGroundingVerdict(turns, "allow"), nil
	}
	// Deliberately force one repair for a wire-adapter test, not a semantic vote.
	return syntheticGroundingVerdict(turns, "reject"), nil
}

func TestDeepRepairRouteAdapterRepairsFrozenGeneratedCandidateWithoutFactIDRouting(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprint(invalid), func(t *testing.T) {
			sample := deepGeneratedControlSample(t)
			engine, in := deepToolVerifierFixture(t, sample)
			probe := &deepRouteAdapterProbe{Target: in.Sender, Text: sample.Draft, InvalidRoute: invalid}
			recording := &deepRepairRecordingModel{client: probe, chatModel: "deepseek-flash", captureGroundingRequests: true}
			engine.model = recording
			proof, ok := engine.Store.SourceForInput(in)
			if !ok {
				t.Fatal("actual user source missing")
			}
			draft, err := engine.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{sample.Draft}, ReplyContexts: []ReplyContext{{TargetIDs: []string{in.Sender}, SourceEventIDs: []string{proof.EventID}}}})
			if err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(draft)
			out, err := engine.DiagnoseGroundedReply(t.Context(), in, draft)
			after, _ := json.Marshal(draft)
			if string(before) != string(after) || probe.Edits != 1 || len(probe.EditorPacket) == 0 || engine.Tools != nil {
				t.Fatal("consumer changed original candidate, skipped editor or installed tools", err)
			}
			wire := string(probe.EditorPacket["reply_routes"])
			for _, forbidden := range []string{"source_event_ids", "source_versions", "evidence_epoch", "context_version"} {
				if strings.Contains(wire, forbidden) {
					t.Fatal("route wire exposed editable proof", wire)
				}
			}
			if invalid {
				if out.Action == "reply" || probe.Checks != 1 || deepReviewE2EAuditState(t, engine, in) != "repair_binding_invalid" {
					t.Fatal("invalid fact reference was treated as a valid route", out, err)
				}
				return
			}
			if err != nil || out.Action != "reply" || !reflect.DeepEqual(out.Messages, []string{sample.Draft}) || probe.Checks != 2 || deepReviewE2EAuditState(t, engine, in) != "repaired_and_checked" {
				t.Fatal("actual one-editor plus final-check branch failed", out, err, probe.Checks)
			}
			if !reflect.DeepEqual(out.ReplyContexts[0].TargetIDs, []string{in.Sender}) || out.ReplyContexts[0].AnchorMessageID != in.TraceID || engine.ValidateReplyContext(t.Context(), in, out.ReplyContexts[0]) != nil {
				t.Fatal("host route lost actual target or source proof", out.ReplyContexts)
			}
			for _, id := range out.ReplyContexts[0].SourceEventIDs {
				if id != proof.EventID {
					t.Fatal("execution or knowledge reference entered DB source proof", id)
				}
			}
		})
	}
}

func TestDeepRepairVoiceUploadRecorderPreservesActorWithoutInventingASRSpeaker(t *testing.T) {
	store, c, _ := memoryFixture(t)
	c.Enabled, c.Permissions.ReplyText = true, true
	var tc deepRepairCase
	for _, candidate := range deepRepairCatalogue(t) {
		if candidate.ID == "media-07-0" {
			tc = candidate
			break
		}
	}
	if tc.ID == "" {
		t.Fatal("original frozen voice fixture missing")
	}
	in, _, _, _, _ := deepRepairLiveScene(t, store, c, tc, time.Now().Add(-time.Minute))
	probe := &deepAliasAwareProbe{}
	recording := &deepRepairRecordingModel{client: probe, captureGroundingRequests: true, chatModel: "deepseek-flash"}
	engine, err := New(c, store, recording, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if in.QuotedSource == nil {
		t.Fatal("original quoted voice source missing")
	}
	if err = store.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	quoted, found := engine.replyReferenceEvent(t.Context(), in, in.QuotedSource)
	if !found {
		t.Fatal("actual quoted source not resolved")
	}
	draft, err := engine.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{"无法仅凭上传者确认录音中的说话人。"}, ReplyContexts: []ReplyContext{{TargetIDs: []string{in.Sender}, SourceEventIDs: []string{quoted.ID}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.checkReplyGrounding(t.Context(), in, draft); err != nil {
		t.Fatal(err)
	}
	var table []GroundingEvidenceRow
	if json.Unmarshal(probe.Packet["evidence_table"], &table) != nil {
		t.Fatal("actual typed evidence table missing")
	}
	for _, row := range table {
		if row.Kind == "user_utterance" && (row.Metadata["speaker_id"] == tc.Other || row.Metadata["incoming_I_means"] == tc.Other) {
			t.Fatal("pure upload invented a literal or ASR first-person speaker", row)
		}
	}
	var packet any
	_ = json.Unmarshal([]byte(probe.Wire), &packet)
	uploads, observations := 0, 0
	var visit func(any)
	visit = func(value any) {
		switch row := value.(type) {
		case map[string]any:
			if row["evidence_kind"] == "media_upload_event" && row["message_sender_id"] == tc.Other {
				uploads++
				if row["speaker_id"] != nil || row["incoming_I_means"] != nil {
					t.Fatal("actual upload packet still binds recording speaker", row)
				}
			}
			if row["uploader_id"] == tc.Other && row["voice_speaker_state"] == "unknown" {
				observations++
				if row["voice_speaker_id"] != nil || row["transcript_first_person_id"] != nil {
					t.Fatal("recorded ASR first person was assigned to uploader", row)
				}
			}
			for _, child := range row {
				visit(child)
			}
		case []any:
			for _, child := range row {
				visit(child)
			}
		}
	}
	visit(packet)
	if uploads == 0 || observations == 0 || len(recording.responses) != 1 || recording.responses[0]["synthetic_request"] == "" {
		t.Fatal("actual wire omitted upload identity, unknown recording speaker or recorder evidence", uploads, observations)
	}
}
