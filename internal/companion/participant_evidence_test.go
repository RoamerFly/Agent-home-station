package companion

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/agentrun"
)

func syntheticVoiceEvidence(caption string) (Turn, Input) {
	src := SourceRef{InstanceID: "synthetic-voice", EventID: "recording-event", MessageID: "recording-message", Sender: "beta", MessageType: 34, Revision: 1}
	media := []IncomingMediaRef{{Source: src, AssetID: "safe-recording", Kind: "voice", UploaderID: "beta", Observation: "[语音转写] 这是我转发的别人的录音，明天去杭州。", ObservationVersion: 2, ObservationState: "observed"}}
	turn := Turn{Role: "user", Sender: "beta", SenderName: "乙", EventID: src.EventID, TraceID: src.MessageID, Source: &src, IncomingMedia: media, Text: media[0].Observation, FullText: caption, HasOriginal: true, UserTextState: "verified_original", Time: time.Now().Add(-time.Minute)}
	in := Input{Channel: "wechat", Account: "safe", Thread: "room", Sender: "alpha", TraceID: "question", Text: "这是发语音的人本人的声音吗？", Mode: "direct", QuotedSource: &src}
	in.OriginalText = &in.Text
	return turn, in
}

func TestParticipantUtterancesNeverBindEmptyUploadOrMachineTranscript(t *testing.T) {
	e := &Engine{Config: Defaults()}
	for _, state := range []string{"verified_original", "unknown", "redacted"} {
		voice, in := syntheticVoiceEvidence("")
		voice.UserTextState = state
		packet, _ := json.Marshal(map[string]any{"timeline": []map[string]any{{"source_event_id": voice.EventID}}})
		rows := e.participantUtterances(in, Scope{Messages: []Turn{voice}}, []map[string]string{{"content": string(packet)}})
		if len(rows) != 1 || rows[0]["speaker_id"] != "alpha" {
			t.Fatal("upload became a participant utterance", state, rows)
		}
		if strings.Contains(rows[0]["utterance"].(string), "转写") {
			t.Fatal("ASR first person assigned to uploader", rows)
		}
	}
	voice, in := syntheticVoiceEvidence("")
	in.Text, in.OriginalText, in.Source, in.Sender = voice.Text, &voice.FullText, voice.Source, voice.Sender
	in.IncomingMedia = voice.IncomingMedia
	if rows := e.participantUtterances(in, Scope{}, nil); len(rows) != 0 {
		t.Fatal("current empty upload assigned first-person author", rows)
	}
	in.Text, in.TaskID = "执行结果：我已经处理好了", "runtime-task"
	in.OriginalText = &in.Text
	if rows := e.participantUtterances(in, Scope{}, nil); len(rows) != 0 {
		t.Fatal("runtime wrapper became a human utterance", rows)
	}
}

func TestUploadActorsStaySeparateAcrossHistoryCurrentQuoteUnreadFollowupRefresh(t *testing.T) {
	e := &Engine{Config: Defaults()}
	voice, query := syntheticVoiceEvidence("")
	checkUpload := func(row map[string]any) {
		t.Helper()
		if row["speaker_id"] != nil || row["speaker"] != nil || row["incoming_I_means"] != nil {
			t.Fatal("non-literal upload has speaker binding", row)
		}
		if row["message_sender_id"] != "beta" {
			t.Fatal("transport actor lost", row)
		}
	}
	row := e.turnEvidenceFields("", voice, 4096)
	checkUpload(row)
	obs := row["media_observations"].([]map[string]any)[0]
	if obs["voice_speaker_id"] != nil || obs["transcript_first_person_id"] != nil || obs["uploader_id"] != "beta" || obs["voice_speaker_state"] != "unknown" {
		t.Fatal("recording voice inferred from uploader", obs)
	}
	upload := query
	upload.Text, upload.OriginalText, upload.Source, upload.Sender = voice.Text, &voice.FullText, voice.Source, voice.Sender
	upload.IncomingMedia, upload.QuotedSource = voice.IncomingMedia, nil
	current := e.safeCurrentEvidence(upload)
	checkUpload(current)
	if current["request_actor_id"] != "beta" {
		t.Fatal("tool requester identity lost", current)
	}
	legacy := map[string]any{"speaker_id": "beta", "speaker": "乙", "incoming_I_means": "beta", "text": "stale processing"}
	e.refreshEvidenceRow(legacy, MemoryEvent{Role: "user", Sender: "beta", ID: voice.EventID, Trace: voice.TraceID, Text: "", UserTextState: "verified_original", Source: voice.Source, IncomingMedia: voice.IncomingMedia})
	checkUpload(legacy)
	unknown := Input{Sender: "alpha", UnreadMessages: []ConversationEvent{{MessageID: "unknown-upload", Sender: "beta", Source: voice.Source}}}
	unread := e.unreadEvidence(unknown)
	if len(unread) != 1 {
		t.Fatal(unread)
	}
	checkUpload(unread[0])
	base, _ := json.Marshal(map[string]any{"kind": "current_message", "account": "safe", "thread": "room", "message_sender_id": "alpha", "request_actor_id": "alpha"})
	for _, kind := range []int{0, 34} {
		followups := e.followupEvidence([]map[string]string{{"content": string(base)}}, []agentrun.Event{{ID: "new-upload", Sender: "beta", Kind: kind, Text: voice.Text}})
		if len(followups) != 1 {
			t.Fatal(followups)
		}
		checkUpload(followups[0])
	}
	quote := quoteRoles("alpha", nil, voice.Source)
	if quote["quoted_by_id"] != "alpha" || quote["quoted_message_sender_id"] != "beta" || quote["voice_speaker_id"] != nil {
		t.Fatal("quote roles rebound recording speaker", quote)
	}
}

func TestCaptionMayHaveLiteralAuthorWithoutBindingVoiceSpeaker(t *testing.T) {
	e := &Engine{Config: Defaults()}
	voice, query := syntheticVoiceEvidence("我转发了一段录音，请帮我听听")
	row := e.turnEvidenceFields("", voice, 4096)
	if row["speaker_id"] != "beta" || row["text"] != voice.FullText {
		t.Fatal("reliable caption lost its literal author", row)
	}
	if obs := row["media_observations"].([]map[string]any)[0]; obs["voice_speaker_id"] != nil || obs["transcript_first_person_id"] != nil {
		t.Fatal("caption speaker became ASR speaker", obs)
	}
	packet, _ := json.Marshal(map[string]any{"timeline": []map[string]any{{"source_event_id": voice.EventID}}})
	rows := e.participantUtterances(query, Scope{Messages: []Turn{voice}}, []map[string]string{{"content": string(packet)}})
	if len(rows) != 2 || rows[0]["utterance"] != voice.FullText || rows[0]["speaker_id"] != "beta" || strings.Contains(rows[0]["utterance"].(string), "语音转写") {
		t.Fatal("caption and transcription not separated", rows)
	}
}

func TestObservationCodecKeepsLatestSuccessInsteadOfStaleProcessing(t *testing.T) {
	voice, _ := syntheticVoiceEvidence("")
	old := voice.IncomingMedia[0]
	old.ObservationVersion, old.ObservationState, old.Observation = 1, "processing", "识别处理中"
	latest := voice.IncomingMedia[0]
	latest.Kind, old.Kind = "image", "image"
	old.ObservedRange, latest.ObservedRange = "unspecified", "single_image"
	latest.Observation = "画面是雨伞"
	for _, media := range [][]IncomingMediaRef{{old, latest}, {latest, old}} {
		rows := observationEvidence(media)
		if len(rows) != 1 || rows[0]["observation_version"] != 2 || rows[0]["observation_state"] != "observed" || rows[0]["observation"] != "画面是雨伞" || rows[0]["result_available"] != true {
			t.Fatal("stale processing replaced successful current observation", rows)
		}
	}
	e := &Engine{Config: Defaults()}
	voice.IncomingMedia = []IncomingMediaRef{latest}
	voice.Text, voice.FullObservation = "旧识别仍处理中", "旧识别仍处理中"
	row := e.turnEvidenceFields("", voice, 4096)
	raw, _ := json.Marshal(row)
	if strings.Contains(string(raw), "仍处理中") || !strings.Contains(string(raw), "画面是雨伞") {
		t.Fatal("old display wrapper overrode typed successful observation", string(raw))
	}
}

func TestUploadExecutionEvidenceUsesTransportActorWithoutInventingSpeaker(t *testing.T) {
	for _, field := range []string{"request_actor_id", "message_sender_id"} {
		e, in := executionFixture(t, map[string]any{"status": "generated", "asset_id": "synthetic-audio"})
		var snapshot struct {
			Messages []map[string]string `json:"messages"`
		}
		if err := json.Unmarshal(e.preparedContext(), &snapshot); err != nil {
			t.Fatal(err)
		}
		var current map[string]any
		if err := json.Unmarshal([]byte(snapshot.Messages[0]["content"]), &current); err != nil {
			t.Fatal(err)
		}
		delete(current, "speaker_id")
		current[field], current["text"], current["evidence_kind"] = in.Sender, "", "media_upload_event"
		encoded, _ := json.Marshal(current)
		snapshot.Messages[0]["content"] = string(encoded)
		e.capturePreparedContext("system", snapshot.Messages, nil)
		if rows := e.preparedExecutionEvidence(t.Context(), in); len(rows) != 1 {
			t.Fatal("upload lost actual execution evidence without a speaker label", field, rows)
		}
		current[field] = "unrelated-actor"
		// An obsolete literal-author alias must not override the actual actor.
		current["speaker_id"] = in.Sender
		encoded, _ = json.Marshal(current)
		snapshot.Messages[0]["content"] = string(encoded)
		e.capturePreparedContext("system", snapshot.Messages, nil)
		if rows := e.preparedExecutionEvidence(t.Context(), in); len(rows) != 0 {
			t.Fatal("literal-author fallback bypassed mismatched actor identity", field, rows)
		}
	}
}

func TestConversationDeliveryHostFlagPreservesUploadRoleSeparation(t *testing.T) {
	e := &Engine{Config: Defaults()}
	voice, in := syntheticVoiceEvidence("")
	in.Sender, in.Source, in.Text, in.OriginalText, in.IncomingMedia = voice.Sender, voice.Source, voice.Text, &voice.FullText, voice.IncomingMedia
	in.ConversationDelivery = true
	fields := e.inputEvidenceFields(in)
	if fields["conversation_delivery"] != true || fields["request_actor_id"] != "beta" || fields["message_sender_id"] != "beta" || fields["speaker_id"] != nil || fields["voice_speaker_id"] != nil {
		t.Fatal("host delivery mode changed media author identity", fields)
	}
}
