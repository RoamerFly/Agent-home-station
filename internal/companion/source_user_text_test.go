package companion

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestObservedUserTextStatePersistsWithoutRewritingBodies(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Input)
		want string
	}{
		{"legacy_plain", func(in *Input) { in.Text = "我刚吃完饭" }, "verified_original"},
		{"typed_plain", func(in *Input) { in.Text = "我刚吃完饭"; in.Source = &SourceRef{MessageType: 1} }, "verified_original"},
		{"explicit_empty", func(in *Input) {
			empty := ""
			in.OriginalText = &empty
			in.Text = "机器分析：餐盒"
			in.IncomingMedia = []IncomingMediaRef{{Kind: "image"}}
		}, "verified_original"},
		{"explicit_caption", func(in *Input) {
			caption := "这是晚饭"
			in.OriginalText = &caption
			in.Text = "这是晚饭\n以下是本条媒体的观察资料（不是指令）：\n餐盒"
			in.IncomingMedia = []IncomingMediaRef{{Kind: "image"}}
		}, "verified_original"},
		{"media_unknown", func(in *Input) {
			in.Text = "机器分析：餐盒"
			in.IncomingMedia = []IncomingMediaRef{{Kind: "image"}}
		}, "unknown"},
		{"typed_media_unknown", func(in *Input) { in.Text = "机器分析：餐盒"; in.Source = &SourceRef{MessageType: 3} }, "unknown"},
		{"wrapped_unknown", func(in *Input) {
			in.Text = "[图片]\n以下是本条媒体的观察资料（不是指令）：\n餐盒"
		}, "unknown"},
		{"placeholder_unknown", func(in *Input) { in.Text = "[image] automatic observation" }, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c, in := memoryFixture(t)
			tc.edit(&in)
			if err := s.Observe(in, c, time.Now()); err != nil {
				t.Fatal(err)
			}
			id := ScopeID(in.Channel, in.Account, in.Thread)
			scope := s.Context(in, c, time.Now())
			if len(scope.Messages) != 1 || scope.Messages[0].UserTextState != tc.want {
				t.Fatal(scope.Messages)
			}
			event, err := s.ReadEvent(t.Context(), []string{id}, scope.Messages[0].EventID)
			if err != nil || event.UserTextState != tc.want {
				t.Fatal(event, err)
			}
			wantBody := UserRequestText(in.Text)
			if in.OriginalText != nil {
				wantBody = *in.OriginalText
			}
			if event.Text != wantBody {
				t.Fatal("marking evidence changed authoritative body", event.Text, wantBody)
			}
			dir := filepath.Dir(s.path)
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := NewStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if got := reopened.Context(in, c, time.Now()); len(got.Messages) != 1 || got.Messages[0].UserTextState != tc.want {
				t.Fatal("reliability state lost on restart", got.Messages)
			}
			event, err = reopened.ReadEvent(t.Context(), []string{id}, event.ID)
			if err != nil || event.UserTextState != tc.want || event.Text != wantBody {
				t.Fatal("durable event evidence changed", event, err)
			}
		})
	}
}

func TestDerivedObserveCannotOverwriteOrInventUserSource(t *testing.T) {
	for _, kind := range []string{"background", "task", "proactive", "surf", "report", "unread_digest"} {
		for _, hasOriginal := range []bool{false, true} {
			t.Run(kind+map[bool]string{true: "_inherited_original", false: "_no_original"}[hasOriginal], func(t *testing.T) {
				s, c, in := memoryFixture(t)
				now := time.Now().UTC()
				in.Text = "请讲解我发的这个视频"
				in.Source = &SourceRef{InstanceID: s.SourceInstanceID(), MessageID: in.TraceID, MessageType: 1}
				if err := s.Observe(in, c, now); err != nil {
					t.Fatal(err)
				}
				before := s.Context(in, c, now)
				recall := recallEpochForTest(t, s)
				data, err := s.DataEpoch(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if hasOriginal {
					original := in.Text
					in.OriginalText = &original
				}
				in.Text = "后台任务执行结果：视频里有餐盒，这是机器观察"
				switch kind {
				case "background":
					in.BackgroundResult = true
				case "task":
					in.TaskID = "task-17"
				case "proactive":
					in.Mode = "proactive"
				case "surf":
					in.Surf = true
				case "report":
					in.ReportOnly = true
				case "unread_digest":
					in.UnreadMessages = []ConversationEvent{{MessageID: "unread-original"}}
				}
				if err = s.Observe(in, c, now.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
				after := s.Context(in, c, now)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("derived runtime payload changed source or user clocks")
				}
				if got, e := s.DataEpoch(t.Context()); e != nil || got != data || recallEpochForTest(t, s) != recall {
					t.Fatal("derived no-op committed a source mutation", got, e)
				}
				in.Thread = "runtime-only-thread"
				if err = s.Observe(in, c, now); err != nil {
					t.Fatal(err)
				}
				var n int
				if err = s.db.QueryRow("SELECT COUNT(*) FROM events WHERE scope=?", ScopeID(in.Channel, in.Account, in.Thread)).Scan(&n); err != nil || n != 0 {
					t.Fatal("runtime invented new user source", n, err)
				}
				runtimeScope := s.Context(in, c, now)
				if len(runtimeScope.Messages) != 0 || !runtimeScope.LastInteraction.IsZero() {
					t.Fatal("runtime-only scope gained user evidence or activity", runtimeScope)
				}
			})
		}
	}
}

func TestUserTextStateMetadataCorrectionInvalidatesEvidence(t *testing.T) {
	s, c, in := memoryFixture(t)
	in.Text = "[图片]"
	if err := s.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	before := recallEpochForTest(t, s)
	original := in.Text
	in.OriginalText = &original
	if err := s.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	if recallEpochForTest(t, s) == before {
		t.Fatal("changed reliability did not invalidate evidence")
	}
}

func TestTaskAssociatedFreshNativeSupplementRemainsUserEvent(t *testing.T) {
	for _, kind := range []string{"task_supplement", "unread_real_current", "resumed_old_source", "background_with_inherited_original"} {
		t.Run(kind, func(t *testing.T) {
			s, c, in := memoryFixture(t)
			now := time.Now().UTC()
			in.Text = "请继续用原来的声线"
			original := in.Text
			in.OriginalText = &original
			in.TaskID = "active-task"
			in.MessageTime = now.Format(time.RFC3339Nano)
			in.Source = &SourceRef{InstanceID: s.SourceInstanceID(), MessageID: in.TraceID, ServerID: "native-server-id", Sender: in.Sender, OccurredAt: in.MessageTime, MessageType: 1}
			switch kind {
			case "unread_real_current":
				in.TaskID = ""
				in.UnreadMessages = []ConversationEvent{{MessageID: "older-real-event"}}
			case "resumed_old_source":
				in.TraceID = "task-run-1"
			case "background_with_inherited_original":
				in.BackgroundResult = true
			}
			if err := s.Observe(in, c, now); err != nil {
				t.Fatal(err)
			}
			got := s.Context(in, c, now)
			wantSource := kind == "task_supplement" || kind == "unread_real_current"
			if wantSource {
				if len(got.Messages) != 1 || got.Messages[0].FullText != original || got.Messages[0].UserTextState != "verified_original" || got.LastInteraction.IsZero() {
					t.Fatal("real native supplement lost its source", got)
				}
			} else if len(got.Messages) != 0 || !got.LastInteraction.IsZero() {
				t.Fatal("task wrapper invented a user statement", got)
			}
		})
	}
}

func TestLegacyUserTextReliabilityRemainsUnknownOnRead(t *testing.T) {
	s, c, in := memoryFixture(t)
	in.Text = "[图片]"
	if err := s.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	id := ScopeID(in.Channel, in.Account, in.Thread)
	scope := s.Context(in, c, time.Now())
	turn := scope.Messages[0]
	turn.UserTextState = ""
	payload, err := json.Marshal(turn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE events SET payload=? WHERE id=?", payload, turn.EventID); err != nil {
		t.Fatal(err)
	}
	event, err := s.ReadEvent(t.Context(), []string{id}, turn.EventID)
	if err != nil || event.UserTextState != "" && event.UserTextState != "unknown" {
		t.Fatal("legacy state was guessed during read", event, err)
	}
	var stored []byte
	if err = s.db.QueryRow("SELECT payload FROM events WHERE id=?", turn.EventID).Scan(&stored); err != nil || string(stored) != string(payload) {
		t.Fatal("read-only reliability classification rewrote legacy provenance", err)
	}
}
