package companion

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestOriginalVideoDirectoryAnchorsScopesPagesAndKeepsBodiesReadable(t *testing.T) {
	s, c, _ := memoryFixture(t)
	stamp := time.Now().UTC().Truncate(time.Second)
	in := Input{Channel: "wechat", Account: "synthetic", Thread: "group", Sender: "member", OriginalText: new(string)}
	for i := 0; i < 13; i++ {
		in.TraceID = fmt.Sprintf("native-%02d", i)
		in.MessageTime = stamp.Add(time.Duration(i/2) * time.Second).Format(time.RFC3339Nano)
		in.Text = "收到视频"
		in.Source = nil
		in.IncomingMedia = []IncomingMediaRef{{Kind: "video", UploaderID: in.Sender, ObservationState: "observed", Observation: strings.Repeat("完整合成音轨", 300)}}
		if i%3 == 0 {
			in.IncomingMedia = nil
		}
		if err := s.Observe(in, c, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	q := MemoryQuery{Scopes: []string{ScopeID(in.Channel, in.Account, in.Thread)}, Originals: true, MediaKind: "video_or_share", BeforeMessageID: "native-11", Order: "time_desc", MetadataOnly: true, Limit: 2}
	page, err := s.SearchMemory(t.Context(), q)
	if err != nil || len(page.Items) != 2 || !page.HasMore {
		t.Fatal(page, err)
	}
	if page.Items[0].Event.Trace != "native-10" || page.Items[1].Event.Trace != "native-08" {
		t.Fatal("anchor/tied timestamp/order wrong", page.Items)
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "完整合成音轨") || page.Items[0].Event.ObservationBytes == 0 {
		t.Fatal("directory copied full bodies or lost continuation metadata")
	}
	baseline := page
	baseline.Items = append([]MemoryHit(nil), page.Items...)
	for i, hit := range baseline.Items {
		full, e := s.ReadEvent(t.Context(), q.Scopes, hit.Event.ID)
		if e != nil {
			t.Fatal(e)
		}
		baseline.Items[i].Event = &full
	}
	fullBytes, _ := json.Marshal(baseline)
	t.Logf("synthetic original-body response=%d bytes, metadata directory=%d bytes", len(fullBytes), len(raw))
	if len(raw)*2 >= len(fullBytes) {
		t.Fatal("metadata directory did not reduce context material")
	}
	ev, err := s.ReadEvent(t.Context(), q.Scopes, page.Items[0].Event.ID)
	if err != nil || len(ev.IncomingMedia) != 1 || !strings.Contains(ev.IncomingMedia[0].Observation, "完整合成音轨") {
		t.Fatal("metadata query removed authoritative observation", err)
	}
	q.Cursor = page.NextCursor
	q.Offset = 0
	second, err := s.SearchMemory(t.Context(), q)
	if err != nil || len(second.Items) != 2 || second.Items[0].Event.ID == page.Items[0].Event.ID {
		t.Fatal(second, err)
	}
	q.BeforeMessageID = "native-10"
	if _, err = s.SearchMemory(t.Context(), q); !errors.Is(err, ErrMemoryConflict) {
		t.Fatal("cursor rebound to different anchor", err)
	}
	q.Cursor = ""
	q.Scopes = []string{ScopeID("wechat", "another-account", "group")}
	q.BeforeMessageID = "native-11"
	if _, err = s.SearchMemory(t.Context(), q); !errors.Is(err, ErrOriginalAnchorUnavailable) {
		t.Fatal("cross-account anchor accepted", err)
	}
	q.Scopes = []string{ScopeID(in.Channel, in.Account, in.Thread)}
	anchor, err := s.SearchMemory(t.Context(), MemoryQuery{Scopes: q.Scopes, Originals: true, Query: "", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range anchor.Items {
		if hit.Event.Trace == "native-11" {
			if _, err = s.db.Exec("INSERT INTO memory_blocks(scope,event_id) VALUES(?,?)", q.Scopes[0], hit.Event.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = s.SearchMemory(t.Context(), q); !errors.Is(err, ErrOriginalAnchorUnavailable) {
		t.Fatal("forgotten anchor bypassed", err)
	}
}

func TestRequestedHistoryIsScopedNumberedAndDoesNotFollowQuotedInstructions(t *testing.T) {
	for _, text := range []string{"总结这条视频之前的前面8条视频", "整理前八条视频", "对比前面十二条视频", "不要再让我重发，总结前8条视频"} {
		b, _ := json.Marshal(map[string]any{"kind": "current_message", "text": text, "quoted_source": SourceRef{MessageID: "real-quote"}})
		n := requestedHistory([]map[string]string{{"role": "user", "content": string(b)}})
		if n == nil || n.Anchor != "real-quote" || n.Count < 8 {
			t.Fatal(text, n)
		}
	}
	for _, text := range []string{"总结这条视频", "不要读取前8条视频", "总结作者主页前8条视频", "总结视频号作者之前的8条视频"} {
		b, _ := json.Marshal(map[string]any{"kind": "current_message", "text": text, "quoted_source": SourceRef{MessageID: "real-quote"}, "quoted_message_evidence": map[string]any{"text": "总结前8条视频"}})
		if requestedHistory([]map[string]string{{"content": string(b)}}) != nil {
			t.Fatal("unrequested history lookup", text)
		}
	}
}
