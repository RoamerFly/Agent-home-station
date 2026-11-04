package admin

import (
	"encoding/json"
	"testing"
	"time"

	"wechat-bot/internal/companion"
)

// Synthetic material shared by the authenticated HTTP and browser replays.
func seedVideoReflection(t *testing.T, m *Manager, in companion.Input) companion.ExperienceReflection {
	t.Helper()
	at := time.Now().UTC()
	in.Text, in.TraceID = "这是我拍的海浪配色视频。", "synthetic-wave-video"
	in.OriginalText = &in.Text
	in.Source = &companion.SourceRef{InstanceID: "synthetic-video-reader", MessageID: in.TraceID, Account: in.Account, Thread: in.Thread, Sender: in.Sender, SenderName: in.SenderName, OccurredAt: at.Format(time.RFC3339Nano), Revision: 1}
	excerpt := "海浪由深蓝过渡到浅紫"
	in.IncomingMedia = []companion.IncomingMediaRef{{Source: *in.Source, Kind: "video", AssetID: "synthetic-wave", UploaderID: in.Sender, ObservationState: "verified", ObservationVersion: 1, Observation: excerpt, ObservationExcerpt: excerpt, ObservedRange: "12 秒视频，抽取 3 帧"}}
	cfg := m.Config().Companion
	cfg.Permissions.Remember = true
	if err := m.memory.Observe(in, cfg, at); err != nil {
		t.Fatal(err)
	}
	scope := companion.ScopeID(in.Channel, in.Account, in.Thread)
	candidates, err := m.memory.ListObservedExperienceReflectionCandidates(t.Context(), scope, 3)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("synthetic observed candidates: %v, %v", candidates, err)
	}
	c := candidates[0]
	item, err := m.memory.SaveExperienceReflection(t.Context(), companion.ExperienceReflectionInput{Scope: scope, SourceAssetID: c.SourceAssetID, SourceEventID: c.SourceEventID, SourceEventRevision: c.SourceEventRevision, SourceObservationVersion: c.SourceObservationVersion, SourceObservationHash: c.SourceObservationHash, Note: "下次想继续看海边明信片的配色灵感", WantsToRevisit: true})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func TestMemoryReflectionManagement(t *testing.T) {
	f := setup(t)
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "waves@chatroom", IsGroup: true, Sender: "alice", SenderName: "小林"}
	item := seedVideoReflection(t, f.manager, in)
	path := "/api/companion/memory/" + item.Scope + "/reflections"
	if code, _, _ := f.request("GET", path, nil, false, false); code != 401 {
		t.Fatalf("anonymous read: %d", code)
	}
	f.login()
	code, data, _ := f.request("GET", path+"?limit=1", nil, false, false)
	var page companion.ExperienceReflectionPage
	if code != 200 || json.Unmarshal(data, &page) != nil || len(page.Items) != 1 || page.Items[0].ID != item.ID {
		t.Fatalf("list: %d %s", code, data)
	}
	edit := map[string]any{"action": "correct", "expected_revision": item.Revision, "note": "我更想再看潮水的变化。"}
	if code, _, _ := f.request("POST", path+"/"+item.ID, edit, true, false); code != 403 {
		t.Fatalf("missing CSRF: %d", code)
	}
	in.Account = "another-account"
	other := seedVideoReflection(t, f.manager, in)
	if code, _, _ := f.request("GET", "/api/companion/memory/"+other.Scope+"/reflections", nil, false, false); code != 404 {
		t.Fatalf("cross-account read: %d", code)
	}
	if code, _, _ := f.request("POST", path+"/"+other.ID, edit, true, true); code != 404 {
		t.Fatalf("cross-account edit: %d", code)
	}
	if code, data, _ := f.request("POST", path+"/"+item.ID, edit, true, true); code != 200 {
		t.Fatalf("correct: %d %s", code, data)
	}
	if code, _, _ := f.request("POST", path+"/"+item.ID, edit, true, true); code != 409 {
		t.Fatalf("stale edit: %d", code)
	}
}
