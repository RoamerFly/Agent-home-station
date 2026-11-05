package admin

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

func markSurfBodyRead(session *surfSession, item mcptools.DiscoveryItem) {
	session.addObservation(surfObservation{CandidateID: item.ID, URL: item.URL, Tool: "web_fetch", State: "body_read", JobID: "job-" + item.ID, ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ReturnedCharacters: 12, TotalCharacters: 12, Complete: true, At: time.Now().UTC()})
}

func TestRelatedExplorationStartsOnlyFromThisAuthorizedInterest(t *testing.T) {
	const video = "https://www.bilibili.com/video/BV1425hzpEtn/"
	candidate := mcptools.DiscoveryItem{ID: "music", URL: video}
	bookmark := surfSession{Thread: "group", ScopeKey: "current", InterestKey: "music", State: "bookmarked", Selected: candidate.ID, Candidates: []mcptools.DiscoveryItem{candidate}}
	seed := func(s surfSession) string {
		return recentRelatedSurfURL([]surfSession{s}, "group", "current", "music")
	}
	if seed(bookmark) != video {
		t.Fatal("missing bookmark from the same authorized interest")
	}
	read := bookmark
	read.State, read.Selected = "not_shared", ""
	read.Visited = []string{canonicalSurfURL(video)}
	if seed(read) != video {
		t.Fatal("missing previously visited video")
	}
	for _, change := range []func(*surfSession){
		func(s *surfSession) { s.Thread = "other" },
		func(s *surfSession) { s.ScopeKey = "before-forgetting" },
		func(s *surfSession) { s.ScopeKey = "" },
		func(s *surfSession) { s.InterestKey = "food" },
		func(s *surfSession) { s.InterestKey = "" },
		func(s *surfSession) { s.Reason = "surf_not_interested" },
		func(s *surfSession) { s.Selected = "" },
		func(s *surfSession) {
			s.Candidates = []mcptools.DiscoveryItem{{ID: "music", URL: "https://b23.tv/abc"}}
		},
	} {
		s := bookmark
		change(&s)
		if got := seed(s); got != "" {
			t.Fatalf("ineligible session supplied a related link: %+v", s)
		}
	}
}

func TestWebDiscoveryAdapterCannotContinueAnotherConversationBookmark(t *testing.T) {
	f, p := trustedAgent(t)
	installPublicWebFixture(t, f)
	configureSyntheticSurf(t, f, p)
	f.manager.config.Companion.Permissions.Remember = true
	p.trace.SurfTopic = "潮汐"
	p.trace.Text = "合成的浏览回合"
	if err := f.manager.memory.Observe(p.trace, f.manager.Config().Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	source := mcptools.DiscoverySource{ID: "public", Name: "公开目录", Type: "bilibili", Kind: "video", URL: "https://api.bilibili.com/x/web-interface/popular", Enabled: true}
	f.manager.surf.Settings.Discovery.Sources = []mcptools.DiscoverySource{source}
	video := mcptools.DiscoveryItem{ID: "one", URL: "https://www.bilibili.com/video/BV1425hzpEtn/"}
	args, _ := json.Marshal(webDiscoveryArguments{Source: source, Related: video.URL})
	_, _, scope, interest, err := p.surfExplorationIdentity(t.Context(), p.trace.SurfTopic)
	if err != nil {
		t.Fatal(err)
	}
	history := &f.manager.surfHistory.data
	history.Items = []surfSeen{{Item: video, Bookmarked: true}}
	history.Sessions = []surfSession{{ID: "previous", Thread: "another-room", ScopeKey: scope, InterestKey: interest, State: "bookmarked", Selected: video.ID, Candidates: []mcptools.DiscoveryItem{video}}}
	if _, _, err := p.webDiscoveryRequest(args); err == nil {
		t.Fatal("generic adapter authorized a global bookmark")
	}
	history.Sessions[0].Thread = p.trace.Thread
	if _, got, err := p.webDiscoveryRequest(args); err != nil || got != "https://api.bilibili.com/x/web-interface/archive/related?bvid=BV1425hzpEtn" {
		t.Fatal("same authorized interest could not continue", got, err)
	}
	if err := f.manager.memory.Clear(companion.ScopeID(p.trace.Channel, p.trace.Account, p.trace.Thread)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.webDiscoveryRequest(args); err == nil {
		t.Fatal("forgotten bookmark was still authorized")
	}
}

func TestDiscoveryPersistsDiverseCandidatesAndContinuesAfterOneSourceFails(t *testing.T) {
	f := setup(t)
	installPublicWebFixture(t, f)
	f.manager.config.Companion.Enabled = true
	f.manager.config.Companion.Permissions.Proactive = true
	f.manager.config.Companion.QuietStart = "00:00"
	f.manager.config.Companion.QuietEnd = "00:00"
	f.manager.surf.Settings.Enabled = true
	f.manager.surf.Settings.Threads = []string{"filehelper"}
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "surf-one", Surf: true})
	p.discoveryReader = func(_ context.Context, s mcptools.DiscoverySource, _ string) ([]mcptools.DiscoveryItem, error) {
		if s.Kind == "voice" {
			return nil, &mcptools.WebError{Code: "network_unavailable"}
		}
		return []mcptools.DiscoveryItem{{Title: s.Name + "猫咪玩耍", URL: "https://example.org/" + s.ID, Kind: s.Kind, Source: s.Name}}, nil
	}
	f.manager.surf.Settings.Discovery.MaxSources = 4
	result, err := p.Call(context.Background(), "web_discover", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Candidates []mcptools.DiscoveryItem `json:"candidates"`
		Failures   []string                 `json:"source_failures"`
	}
	if json.Unmarshal([]byte(result), &data) != nil || len(data.Candidates) < 1 || len(data.Failures) < 1 {
		t.Fatal(result)
	}
	reloaded, err := newSurfHistory(f.dir)
	if err != nil || len(reloaded.data.Sessions) != 1 || len(reloaded.data.Items) == 0 {
		t.Fatal("history lost", err)
	}
	if got := reloaded.data.Sessions[0].Candidates; len(got) == 0 || got[0].Title == "" || got[0].URL == "" {
		t.Fatal("discovery candidates did not retain metadata-only candidate data", got)
	}
	// Same candidate identities are not reintroduced on a second page in the session.
	result, err = p.Call(context.Background(), "web_discover", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(result), &data)
	ids := map[string]bool{}
	for _, x := range reloaded.data.Items {
		ids[x.Item.ID] = true
	}
	for _, x := range data.Candidates {
		if ids[x.ID] {
			t.Fatal("seen candidate repeated", x)
		}
	}
	if _, err = p.discover(context.Background(), json.RawMessage(`{"related_url":"https://www.bilibili.com/video/BV1425hzpEtn/"}`)); err == nil {
		t.Fatal("unseen related source admitted")
	}
}
func TestSurfBookmarkAndUnverifiedLegacyRowsDoNotPrepareShare(t *testing.T) {
	f := setup(t)
	p, _, _ := f.manager.toolAccess()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "surf-a", Surf: true}
	p.SetTrace(in)
	x := mcptools.DiscoveryItem{ID: "one", Title: "三比特纠缠有三种", Summary: "三比特纠缠研究", URL: "https://a.example.org/one", Kind: "text"}
	s := f.manager.surfHistory
	s.data.Items = []surfSeen{{Item: x, Seen: time.Now(), Shared: map[string]time.Time{}}}
	s.data.Sessions = []surfSession{{ID: in.TraceID, Thread: in.Thread, Candidates: []mcptools.DiscoveryItem{x}}}
	markSurfBodyRead(&s.data.Sessions[0], x)
	d, err := p.ReviewSurf(context.Background(), companion.Decision{Action: "silent", SurfAction: "bookmark", SurfItemID: "one"})
	if err != nil || d.Action != "silent" || d.Reason != "surf_bookmarked" || !s.data.Items[0].Bookmarked || s.data.Sessions[0].Selected != x.ID || s.data.Sessions[0].Reason != d.Reason {
		t.Fatal(d, err)
	}
	d, err = p.ReviewSurf(context.Background(), companion.Decision{Action: "reply", SurfItemID: "one", Messages: []string{"这个新发现分享给你"}})
	if err != nil || d.Action != "silent" || d.Reason != "surf_candidate_not_verified" {
		t.Fatal(d, err)
	}
	// Unrelated nature content is not blocked simply because it is also science.
	if sameSurfTopic("猫咪第一次看见雪", "鲸鱼迁徙的季节到了") {
		t.Fatal("novel topic overfiltered")
	}
}
func TestDiscoveryValidationAndHistoryAuth(t *testing.T) {
	f := setup(t)
	if code, _, _ := f.request("GET", "/api/surf/history", nil, false, false); code != 401 {
		t.Fatal(code)
	}
	f.login()
	if code, _, _ := f.request("GET", "/api/surf/history", nil, true, false); code != 200 {
		t.Fatal(code)
	}
	d := defaultDiscovery()
	d.Weights = map[string]int{"recommendations": 3, "community": 1}
	if err := validateDiscovery(d); err != nil {
		t.Fatal("non-100 weights rejected", err)
	}
	d.Weights["community"] = -1
	if validateDiscovery(d) == nil {
		t.Fatal("negative weight admitted")
	}
	d.Weights = map[string]int{"search": 0}
	if validateDiscovery(d) == nil {
		t.Fatal("zero total admitted")
	}
	if _, err := newSurfHistory("\x00"); err == nil || errors.Is(err, context.Canceled) {
		t.Fatal("bad history path silently reset")
	}
}

func TestReportedQuantumCaptionsAreDuplicateWithoutBlockingNewTopics(t *testing.T) {
	a := "刚冲浪看到个挺好玩的东西：量子期刊 Quantum 上有篇论文讲“三比特纠缠有三种”，把以前缺的那块数学拼图补上了 🔬 群里有对量子感兴趣的可以聊聊～ https://quantum-journal.org/"
	b := "刚随手翻到一篇量子期刊上的论文，标题叫《三比特纠缠的三种》，说是补上了纠缠理论里缺的一块数学拼图，看着还挺有意思的 🔬 https://different.example.org/"
	if !sameSurfTopic(a, b) {
		t.Fatal("reported paraphrases not recognized")
	}
	if sameSurfTopic(a, "可爱小猫在雪地里撒欢的日常") {
		t.Fatal("unrelated new topic blocked")
	}
}

func TestImportOldAcceptedSurfSharesPreservesAgeAndDoesNotImportPending(t *testing.T) {
	s, err := newSurfHistory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-3 * time.Hour).UTC()
	logs := []LogEntry{{Event: "message_sent", Status: "accepted", ThreadID: "room@chatroom", MessageID: "surf-old", Time: stamp.Format(time.RFC3339Nano), Content: "三比特纠缠有三种 https://quantum-journal.org/"}, {Event: "message_sent", Status: "unknown", ThreadID: "room@chatroom", MessageID: "surf-unknown", Time: stamp.Format(time.RFC3339Nano), Content: "另一个 https://example.org/unknown"}}
	if err = s.importAccepted(logs); err != nil {
		t.Fatal(err)
	}
	if err = s.importAccepted(logs); err != nil {
		t.Fatal(err)
	}
	if len(s.data.Items) != 1 || !s.data.Items[0].Shared["room@chatroom"].Equal(stamp) || len(s.data.Sessions) != 0 {
		t.Fatal("migration duplicated, refreshed age, or created a new browsing task", s.data)
	}
}

func TestSurfNoCandidatesExplainsNetworkFailureAndSearchFallbackCanJoinSession(t *testing.T) {
	f := setup(t)
	p, _, _ := f.manager.toolAccess()
	in := companion.Input{Thread: "filehelper", TraceID: "surf-empty", Surf: true}
	p.SetTrace(in)
	s := f.manager.surfHistory
	s.data.Sessions = []surfSession{{ID: in.TraceID, Thread: in.Thread, Candidates: []mcptools.DiscoveryItem{}, Failures: []string{"网络不可用"}}}
	d, err := p.ReviewSurf(context.Background(), companion.Decision{Action: "silent"})
	if err != nil || d.Reason != "surf_sources_unavailable" || s.data.Sessions[0].State != "failed" {
		t.Fatal(d, err)
	}
	extra, err := p.rememberSearchCandidates(`{"job":{"state":"completed","result":{"ok":true,"untrusted_external_content":true,"items":[{"title":"公开猫咪视频","url":"https://www.bilibili.com/video/BV1425hzpEtn/","summary":"真实简介"}]}}}`)
	if err != nil || len(s.data.Sessions[0].Candidates) != 1 || extra == "" {
		t.Fatal("successful fallback search not retained", extra, err)
	}
}

func TestSurfReadEvidenceSeparatesDiscoveryBodyAndFailure(t *testing.T) {
	s, err := newSurfHistory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	in := companion.Input{Thread: "room@chatroom", TraceID: "surf-read", Surf: true}
	item := mcptools.DiscoveryItem{ID: "article", Title: "只看到标题", Summary: "发现阶段的简介", URL: "https://example.org/article", Kind: "text"}
	s.data.Sessions = []surfSession{{ID: in.TraceID, Thread: in.Thread, Candidates: []mcptools.DiscoveryItem{item}}}
	if err := s.saveLocked(); err != nil {
		t.Fatal(err)
	}
	metadata := `{"job":{"id":"legacy-read","state":"completed","plugin":"web-search","tool":"web.read","result":{"ok":true,"untrusted_external_content":true,"url":"https://example.org/article","title":"只看到标题"}}}`
	read, err := s.recordRead(in, "web_fetch", item.URL, metadata, nil)
	if err != nil || read {
		t.Fatal("title-only result became a read", read, err)
	}
	body := `{"job":{"id":"web-job-1","state":"completed","plugin":"web-search","tool":"web.read","result":{"ok":true,"untrusted_external_content":true,"url":"https://example.org/article","title":"完整正文","text":"actual body","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","offset":0,"returned_characters":11,"total_characters":11,"complete":true,"read_coverage":{"kind":"page_body","start_offset":0,"end_offset":11,"total_characters":11,"complete":true,"excerpt":"actual body"}}}}`
	read, err = s.recordRead(in, "web_fetch", item.URL, body, nil)
	if err != nil || !read {
		t.Fatal("body result was not retained as a read", read, err)
	}
	if _, err = s.recordRead(in, "web_fetch", item.URL, "", &mcptools.WebError{Code: "network_unavailable"}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newSurfHistory(filepath.Dir(s.path))
	if err != nil {
		t.Fatal(err)
	}
	rows := reloaded.snapshot().Sessions[0].Observations
	states := map[string]surfObservation{}
	for _, row := range rows {
		states[row.State] = row
	}
	if states["legacy_unproven"].CandidateID != item.ID || states["body_read"].JobID != "web-job-1" || states["body_read"].ContentHash == "" || !states["body_read"].Complete || states["read_failed"].Failure == "" {
		t.Fatal("read evidence lost its boundary", rows)
	}
}

func TestSurfOutcomePersistsAndDoesNotConfuseFailuresWithDisinterest(t *testing.T) {
	for _, tc := range []struct {
		name, reason, want, state string
		read, failed              bool
	}{
		{"candidates_only", "silent", "surf_candidates_only", "not_shared", false, false},
		{"read_only", "silent", "surf_candidates_only", "not_shared", true, false},
		{"disinterest", "surf_not_interested", "surf_not_interested", "not_shared", false, false},
		{"unsuitable", "surf_not_suitable", "surf_not_suitable", "not_shared", true, false},
		{"failed_tool", "surf_tool_failed", "surf_tool_failed", "failed", true, true},
		{"invented_failure", "surf_tool_failed", "surf_candidates_only", "not_shared", true, false},
		{"duplicate", "surf_duplicate_source", "surf_duplicate_source", "not_shared", true, false},
		{"budget", "tool_budget_exhausted", "tool_budget_exhausted", "not_shared", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			p, _, _ := f.manager.toolAccess()
			in := companion.Input{Thread: "filehelper", TraceID: "surf-outcome", Surf: true}
			p.SetTrace(in)
			x := mcptools.DiscoveryItem{ID: "cat", Title: "猫咪", URL: "https://example.org/cat"}
			s := f.manager.surfHistory
			s.data.Sessions = []surfSession{{ID: in.TraceID, Thread: in.Thread, Candidates: []mcptools.DiscoveryItem{x}}}
			if tc.read {
				markSurfBodyRead(&s.data.Sessions[0], x)
			}
			if tc.failed {
				f.manager.record(bot.Record{Event: "tool_call_failed", Status: "failed", MessageID: in.TraceID, ThreadID: in.Thread, Tool: "music_local_start", Reason: "backend unavailable"})
			}
			d, err := p.ReviewSurf(context.Background(), companion.Decision{Action: "silent", Reason: tc.reason, SurfItemID: x.ID})
			if err != nil || d.Reason != tc.want {
				t.Fatal(d, err)
			}
			// This is the worker event that used to erase the detailed outcome.
			f.manager.record(bot.Record{Event: "character_decision", Status: "silent", MessageID: in.TraceID, ThreadID: in.Thread, Reason: d.Reason})
			reloaded, err := newSurfHistory(f.dir)
			if err != nil {
				t.Fatal(err)
			}
			got := reloaded.snapshot().Sessions[0]
			if got.State != tc.state || got.Reason != tc.want || got.Selected != x.ID || (len(got.ToolFailures) > 0) != tc.failed {
				t.Fatal(got)
			}
		})
	}
}

func TestSurfVerificationRejectionIsRecordedWithoutClaimingDelivery(t *testing.T) {
	for _, selected := range []string{"", "cat"} {
		f := setup(t)
		p, _, _ := f.manager.toolAccess()
		in := companion.Input{Thread: "filehelper", TraceID: "surf-unverified", Surf: true}
		p.SetTrace(in)
		x := mcptools.DiscoveryItem{ID: "cat", Title: "猫咪", URL: "https://example.org/cat"}
		s := f.manager.surfHistory
		s.data.Sessions = []surfSession{{ID: in.TraceID, Thread: in.Thread, Candidates: []mcptools.DiscoveryItem{x}}}
		d, err := p.ReviewSurf(context.Background(), companion.Decision{Action: "reply", SurfItemID: selected, Messages: []string{"看完了分享给你"}})
		want := "surf_candidate_not_selected"
		if selected != "" {
			want = "surf_candidate_not_verified"
		}
		got := s.snapshot().Sessions[0]
		if err != nil || d.Action != "silent" || d.Reason != want || got.State != "not_shared" || got.Reason != want {
			t.Fatal(d, got, err)
		}
	}
}

func TestSurfReviewRejectsLegacyVisitedWithoutVerifiedBodyEvidence(t *testing.T) {
	f := setup(t)
	p, _, _ := f.manager.toolAccess()
	in := companion.Input{Thread: "filehelper", TraceID: "surf-legacy-visited", Surf: true}
	p.SetTrace(in)
	x := mcptools.DiscoveryItem{ID: "cat", Title: "猫咪", URL: "https://example.org/cat"}
	s := f.manager.surfHistory
	// Older histories may contain Visited entries, but those only prove a
	// previous implementation attempted a fetch; they cannot prove body read.
	s.data.Sessions = []surfSession{{ID: in.TraceID, Thread: in.Thread, Candidates: []mcptools.DiscoveryItem{x}, Visited: []string{x.URL}}}
	d, err := p.ReviewSurf(context.Background(), companion.Decision{Action: "reply", SurfItemID: x.ID, Messages: []string{"看完了"}})
	if err != nil || d.Action != "silent" || d.Reason != "surf_candidate_not_verified" {
		t.Fatal(d, err)
	}
}

func TestForcedSurfNeverSynthesizesShareAfterModelSilence(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		force, read, duplicate bool
		action, reason         string
	}{
		{"verified", true, true, false, "silent", "surf_forced_share_unfulfilled"},
		{"not_read", true, false, false, "silent", "surf_forced_share_unfulfilled"},
		{"default_off", false, true, false, "silent", "surf_forced_share_unfulfilled"},
		{"duplicate", true, true, true, "silent", "surf_forced_share_unfulfilled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			p, _, _ := f.manager.toolAccess()
			in := companion.Input{Thread: "filehelper", TraceID: "surf-force", Surf: true, SurfForceShare: tc.force}
			p.SetTrace(in)
			x := mcptools.DiscoveryItem{ID: "cat", Title: "猫咪在雪地玩耍", URL: "https://example.org/cat", Kind: "text"}
			s := f.manager.surfHistory
			s.data.Sessions = []surfSession{{ID: in.TraceID, Thread: in.Thread, Candidates: []mcptools.DiscoveryItem{x}}}
			if tc.read {
				markSurfBodyRead(&s.data.Sessions[0], x)
			}
			if tc.duplicate {
				s.data.Items = []surfSeen{{Item: x, Shared: map[string]time.Time{in.Thread: time.Now()}}}
			}
			d, err := p.ReviewSurf(context.Background(), companion.Decision{Action: "silent", Reason: "surf_forced_share_unfulfilled", SurfItemID: x.ID})
			if err != nil || d.Action != tc.action || d.Reason != tc.reason {
				t.Fatal(d, err)
			}
			if len(d.Messages) != 0 {
				t.Fatal("program invented a share after model silence", d)
			}
		})
	}
}
