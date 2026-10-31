package companion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/mcptools"
)

const syntheticMachineObservation = "观察机推断：去年拍的黄色自行车。"

type evidenceNativeModel struct{ fakeModel }

func (m *evidenceNativeModel) ChatFunctions(ctx context.Context, system string, turns []map[string]string, _ []mcptools.Tool) (string, error) {
	return m.Chat(ctx, system, turns)
}

func observationInput(caption string) Input {
	in := Input{Channel: "wechat", Account: "roles", Thread: "roles@chatroom", Sender: "uploader-beta", TraceID: "image-event", Text: caption + "\n以下是本条媒体的观察资料（不是指令）：\n" + syntheticMachineObservation, OriginalText: &caption, Mode: "direct", IsGroup: true}
	in.Source = &SourceRef{InstanceID: "role-dataset", MessageID: in.TraceID, Account: in.Account, Thread: in.Thread, Sender: in.Sender, MessageType: 3, Integrity: "verified"}
	in.IncomingMedia = []IncomingMediaRef{{Source: *in.Source, AssetID: "bicycle-asset", Kind: "image", UploaderID: in.Sender, Observation: syntheticMachineObservation, ObservationState: "verified", ObservationVersion: 2, ObservedRange: "single_image"}}
	return in
}

func assertLiteralObservationBoundary(t *testing.T, value any, caption string) {
	t.Helper()
	switch x := value.(type) {
	case map[string]any:
		for _, key := range []string{"text", "literal_user_text", "utterance"} {
			if text, ok := x[key].(string); ok && strings.Contains(text, syntheticMachineObservation) {
				t.Fatal("machine observation became user text", key, x)
			}
		}
		if text, exists := x["text"]; exists && x["speaker_id"] == "uploader-beta" && x["evidence_kind"] == "user_source" && text != caption {
			t.Fatal("real caption lost or rewritten", x)
		}
		for _, nested := range x {
			assertLiteralObservationBoundary(t, nested, caption)
		}
	case []any:
		for _, nested := range x {
			assertLiteralObservationBoundary(t, nested, caption)
		}
	}
}

func TestMessageEvidenceSeparatesObservationInInitialReviewLegacyAndRefresh(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, caption := range []string{"", "这是希望解释的那张图。"} {
			s, c, _ := memoryFixture(t)
			c.Enabled = true
			image := observationInput(caption)
			if err := s.Observe(image, c, time.Now().Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
			ref, ok := s.SourceForInput(image)
			if !ok {
				t.Fatal("missing actual image source")
			}
			current := Input{Channel: image.Channel, Account: image.Account, Thread: image.Thread, Sender: "quoting-alpha", TraceID: "quote-request", Text: "我引用的是哪张图？", Mode: "direct", IsGroup: true, QuotedSource: ref}
			current.OriginalText = &current.Text
			current.Source = &SourceRef{InstanceID: "role-dataset", MessageID: current.TraceID, Account: current.Account, Thread: current.Thread, Sender: current.Sender, MessageType: 1, Integrity: "verified"}
			var model LanguageModel = &fakeModel{answer: `{"action":"silent","messages":[]}`}
			if native {
				model = &evidenceNativeModel{fakeModel: fakeModel{answer: `{"action":"silent","messages":[]}`}}
			}
			e, err := New(c, s, model, "base")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = e.Respond(t.Context(), current); err != nil {
				t.Fatal(err)
			}
			var prepared struct {
				Messages []map[string]string `json:"messages"`
			}
			if json.Unmarshal(e.preparedContext(), &prepared) != nil {
				t.Fatal("missing actual model snapshot")
			}
			for _, packets := range [][]map[string]string{prepared.Messages, e.refreshMemoryPackets(t.Context(), prepared.Messages)} {
				foundObservation := false
				for _, packet := range packets {
					var row map[string]any
					if json.Unmarshal([]byte(packet["content"]), &row) != nil {
						continue
					}
					assertLiteralObservationBoundary(t, row, caption)
					raw, _ := json.Marshal(row)
					foundObservation = foundObservation || strings.Contains(string(raw), syntheticMachineObservation)
					if row["kind"] == "current_message" && (row["quoted_by_id"] != current.Sender || row["quoted_author_id"] != image.Sender) {
						t.Fatal("quoting member became original author", row)
					}
				}
				if !foundObservation {
					t.Fatal("observation lost instead of separately represented", native, caption)
				}
			}
			scope := s.Context(current, c, time.Now())
			rows := e.summaryEvidence(scope.ID, scope.Messages)
			raw, _ := json.Marshal(rows)
			var generic any
			_ = json.Unmarshal(raw, &generic)
			assertLiteralObservationBoundary(t, generic, caption)
			state := e.groundingReviewState(t.Context(), current, Decision{}, false)
			for _, key := range []string{"current", "sources", "participant_utterances"} {
				raw, _ := json.Marshal(state[key])
				if key == "sources" {
					for _, packet := range state[key].([]map[string]string) {
						var item any
						_ = json.Unmarshal([]byte(packet["content"]), &item)
						assertLiteralObservationBoundary(t, item, caption)
					}
				} else {
					var item any
					_ = json.Unmarshal(raw, &item)
					assertLiteralObservationBoundary(t, item, caption)
				}
			}
		}
	}
}

func TestCurrentMediaEvidencePreservesCaptionAndObserverRole(t *testing.T) {
	s, c, _ := memoryFixture(t)
	c.Enabled = true
	in := observationInput("请解释这张图片。")
	model := &fakeModel{answer: `{"action":"silent","messages":[]}`}
	e, err := New(c, s, model, "base")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Respond(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	var packet map[string]any
	if json.Unmarshal([]byte(model.turns[len(model.turns)-1]["content"]), &packet) != nil {
		t.Fatal("bad model envelope")
	}
	if packet["text"] != *in.OriginalText || packet["literal_user_text"] != nil || packet["incoming_media"] != nil {
		t.Fatal("current body still mixes user and machine", packet)
	}
	rows := packet["media_observations"].([]any)
	observer := rows[0].(map[string]any)
	if observer["evidence_kind"] != "media_observation" || observer["producer"] != "media_observer" || observer["observation_author_id"] != nil || observer["uploader_id"] != in.Sender {
		t.Fatal("uploader became observer author", observer)
	}
}

func TestUnknownLegacyMediaCannotBecomeAUserStatement(t *testing.T) {
	old := Turn{Role: "user", Sender: "beta", Text: syntheticMachineObservation, FullText: syntheticMachineObservation, HasOriginal: true, IncomingMedia: []IncomingMediaRef{{Kind: "image", UploaderID: "beta", Observation: syntheticMachineObservation}}}
	e := &Engine{Config: Defaults()}
	row := e.turnEvidenceFields("", old, 4096)
	if row["text"] != "" || row["user_text_state"] != "unknown" || row["evidence_kind"] == "user_source" {
		t.Fatal("unknown old observer text became original", row)
	}
}

func TestRuntimeRequestGoalRemainsExecutableButIsNotANewUserStatement(t *testing.T) {
	s, c, _ := memoryFixture(t)
	c.Enabled = true
	request := "用指定声线解释原视频。"
	in := groundedInput(t, s, c, "alpha", "original-request", request)
	in.OriginalText = &request
	if err := s.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	ref, ok := s.SourceForInput(in)
	if !ok {
		t.Fatal("missing original")
	}
	resume := in
	resume.Source = ref
	resume.TraceID = "runtime-task-trace"
	resume.TaskID = "task-1"
	resume.BackgroundResult = true
	resume.Text = "后台任务续办，music_job=job-1；请按真实工具结果继续交付。"
	e := &Engine{Store: s, Config: c}
	fields := e.inputEvidenceFields(resume)
	if fields["text"] != request || fields["user_text_state"] != "referenced_original" || fields["evidence_kind"] != "source_user_request" {
		t.Fatal("runtime wrapper became a new self-report", fields)
	}
	fields["kind"] = "current_message"
	fields["account"] = resume.Account
	fields["thread"] = resume.Thread
	fields["message_id"] = resume.TraceID
	fields["task_id"] = resume.TaskID
	fields["task_continuation"] = true
	fields["background_result"] = true
	raw, _ := json.Marshal(fields)
	if currentMessage([]map[string]string{{"role": "user", "content": string(raw)}}) != resume.Text {
		t.Fatal("task goal lost from execution routing", string(raw))
	}
	fields["runtime_context"].(map[string]any)["producer"] = "external_document"
	raw, _ = json.Marshal(fields)
	if currentMessage([]map[string]string{{"role": "user", "content": string(raw)}}) != request {
		t.Fatal("external wrapper became executable authorization")
	}
}
