package companion

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/mcptools"
)

type deepRepairCase struct {
	ID, Group, Behavior, Actor, Other, DisplayName, Request string
	Permutation                                             int
	MustRespond                                             bool `json:"must_respond"`
}

func deepRepairCatalogue(t *testing.T) []deepRepairCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/deep_repair/scenarios.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Version string
		Cases   []deepRepairCase
	}
	if err = json.Unmarshal(raw, &v); err != nil || len(v.Cases) != 240 {
		t.Fatal("catalogue", err, len(v.Cases))
	}
	return v.Cases
}
func deepRepairInput(tc deepRepairCase, now time.Time) Input {
	return Input{Channel: "wechat", Account: "synthetic", Thread: "synthetic-room@chatroom", Sender: tc.Actor, SenderName: tc.DisplayName, Mode: "direct", IsGroup: true, TraceID: tc.ID, Text: tc.Request, MessageTime: now.Format(time.RFC3339Nano)}
}
func deepRepairSource(in Input, instance string, at time.Time) *SourceRef {
	return &SourceRef{InstanceID: instance, MessageID: in.TraceID, ServerID: "server-" + in.TraceID, Account: in.Account, Thread: in.Thread, Sender: in.Sender, OccurredAt: at.Format(time.RFC3339Nano), Revision: 1, Integrity: "verified"}
}
func deepRepairEvent(t *testing.T, s *Store, c Config, in Input, now time.Time) MemoryEvent {
	t.Helper()
	if err := s.Observe(in, c, now); err != nil {
		t.Fatal(err)
	}
	page, err := s.SearchMemory(t.Context(), MemoryQuery{Scopes: []string{ScopeID(in.Channel, in.Account, in.Thread)}, Originals: true, Query: in.Text, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range page.Items {
		if hit.Event != nil && hit.Event.Sender == in.Sender && hit.Event.Trace == in.TraceID {
			return *hit.Event
		}
	}
	t.Fatal("source not found", in.TraceID)
	return MemoryEvent{}
}

// This suite calls production storage, context selection and reply validation.
// It is deterministic regression evidence, not a claim about model quality.
func TestDeepRepair240SyntheticScenarios(t *testing.T) {
	counts := map[string]int{}
	selected := 0
	for _, tc := range deepRepairCatalogue(t) {
		counts[tc.Group]++
		t.Run(tc.ID, func(t *testing.T) {
			selected++
			s, c, _ := memoryFixture(t)
			c.Enabled = true
			c.Permissions.ReplyText = true
			c.Permissions.ObserveGroups = true
			c.Permissions.AutonomousGroups = true
			now := time.Now().UTC().Add(-time.Minute)
			in := deepRepairInput(tc, now)
			in.Text = "synthetic-source-" + tc.ID
			switch tc.Group {
			case "people", "conversation":
				source := in
				source.Sender = tc.Other
				source.TraceID = "evidence-" + tc.ID
				if tc.Behavior == "same-speaker-source" {
					source.Sender = in.Sender
				}
				if tc.Behavior == "cross-thread-source" {
					source.Thread = "elsewhere@chatroom"
				}
				if tc.Behavior == "cross-account-source" {
					source.Account = "different-account"
				}
				ev := deepRepairEvent(t, s, c, source, now)
				engine, engineErr := New(c, s, extractionFixture(func(context.Context, string, []map[string]string) (string, error) {
					return `{"action":"silent","messages":[]}`, nil
				}), "synthetic")
				if engineErr != nil {
					t.Fatal(engineErr)
				}
				rc := ReplyContext{TargetIDs: []string{in.Sender}, AnchorMessageID: in.TraceID, SourceEventIDs: []string{ev.ID}}
				allowed := source.Account == in.Account && source.Thread == in.Thread
				if tc.Behavior == "explicit-quote" || tc.Behavior == "quoted-other-person" || tc.Behavior == "father-addressed" {
					in.QuotedSource = &SourceRef{MessageID: source.TraceID, Sender: source.Sender, Account: source.Account, Thread: source.Thread}
					allowed = true
				}
				if tc.Behavior == "assistant-not-source" || tc.Behavior == "assistant-source" {
					if err := s.AcceptedDelivery(in, "synthetic assistant claim", "assistant-key", c, now); err != nil {
						t.Fatal(err)
					}
					scope := s.Context(in, c, now)
					for _, turn := range scope.Messages {
						if turn.Role == "assistant" {
							rc.SourceEventIDs = []string{turn.EventID}
						}
					}
					allowed = false
				}
				if tc.Behavior == "unrelated-mention" {
					in.MentionedIDs = []string{tc.Other}
					rc.TargetIDs = []string{tc.Other}
					allowed = false
				}
				if tc.Behavior == "missing-anchor" {
					rc.AnchorMessageID = "nonexistent"
					allowed = false
				}
				// Live Respond observes the trigger before normalizing a reply.
				// Keep the current anchor distinct from the other author's facts.
				in.Source = deepRepairSource(in, "current-grounding-suite", now)
				deepRepairEvent(t, s, c, in, now)
				normalized, nerr := engine.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{"这是关于来源作者所说内容的回答。"}, ReplyContexts: []ReplyContext{rc}})
				if allowed && nerr != nil {
					t.Fatal("valid grounding rejected", nerr)
				}
				if !allowed && nerr == nil {
					t.Fatal("unrelated/invalid evidence admitted", tc.Behavior)
				}
				if allowed {
					nrc := normalized.ReplyContexts[0]
					if err := engine.ValidateReplyContext(t.Context(), in, nrc); err != nil {
						t.Fatal("normalized proof rejected", err)
					}
					if err := engine.ValidateReplyContext(t.Context(), in, rc); err == nil {
						t.Fatal("unverified binding admitted")
					}
					missingVersion := nrc
					missingVersion.SourceVersions = nil
					missingVersion.ContextVersion = ReplyContextFingerprint(in, missingVersion)
					if err := engine.ValidateReplyContext(t.Context(), in, missingVersion); err == nil {
						t.Fatal("binding without source revisions admitted")
					}
					if len(nrc.TargetIDs) != 1 || nrc.TargetIDs[0] != in.Sender {
						t.Fatal("source author transferred reply target", nrc)
					}
					actual, readErr := s.ReadEvent(t.Context(), []string{ev.Scope}, ev.ID)
					if readErr != nil || actual.Sender != source.Sender || nrc.SourceVersions[ev.ID] != actual.Revision {
						t.Fatal("source author or revision lost")
					}
					if source.Sender != in.Sender && in.QuotedSource == nil && !strings.Contains(strings.Join(nrc.RiskFlags, ","), "cross_person_source") {
						t.Fatal("cross-person text skipped attribution review")
					}
				}
			case "media":
				src := deepRepairInput(tc, now)
				src.Sender = tc.Other
				src.TraceID = "image-" + tc.ID
				src.Text = "[视觉观察] 盒饭 汤洒键盘 private-image-marker"
				media := IncomingMediaRef{Source: *deepRepairSource(src, "media-suite", now), Kind: "image", UploaderID: src.Sender, Observation: src.Text, ObservationState: "verified", ObservationVersion: 1}
				src.IncomingMedia = []IncomingMediaRef{media}
				if err := s.Observe(src, c, now); err != nil {
					t.Fatal(err)
				}
				if tc.Behavior == "quoted-old-image" {
					in.QuotedSource = &media.Source
				}
				in.Source = deepRepairSource(in, "media-trigger-suite", now)
				deepRepairEvent(t, s, c, in, now)
				scope := s.Context(in, c, now)
				engine, newErr := New(c, s, extractionFixture(func(context.Context, string, []map[string]string) (string, error) {
					return `{"action":"silent","messages":[]}`, nil
				}), "synthetic")
				if newErr != nil {
					t.Fatal(newErr)
				}
				mediaReply := ReplyContext{TargetIDs: []string{in.Sender}, AnchorMessageID: in.TraceID, MediaSourceIDs: []string{media.Source.MessageID}}
				mediaDecision, mediaErr := engine.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{"已查看明确关联的素材。"}, ReplyContexts: []ReplyContext{mediaReply}})
				if mediaErr == nil {
					mediaErr = engine.ValidateReplyContext(t.Context(), in, mediaDecision.ReplyContexts[0])
				}
				if tc.Behavior == "quoted-old-image" && mediaErr != nil {
					t.Fatal("explicit media quote rejected", mediaErr)
				}
				if tc.Behavior != "quoted-old-image" && mediaErr == nil {
					t.Fatal("unlinked other-person media allowed")
				}
				selected := groundedHistory(scope, in)
				blob, _ := json.Marshal(selected)
				if tc.Behavior == "quoted-old-image" {
					if !strings.Contains(string(blob), "private-image-marker") {
						t.Fatal("explicitly quoted image lost")
					}
				} else {
					for _, turn := range selected {
						if strings.Contains(turn.Text, "private-image-marker") || len(turn.IncomingMedia) > 0 {
							t.Fatal("other person's media entered personalized context")
						}
					}
				}
			case "restart":
				dir := t.TempDir()
				s2, err := NewStore(dir)
				if err != nil {
					t.Fatal(err)
				}
				in.Source = deepRepairSource(in, "restart-suite", now)
				in.QuotedSource = &SourceRef{MessageID: "quoted-source", Sender: tc.Other}
				in.Addressees = []string{tc.Other}
				in.ReplyContext = &ReplyContext{TargetIDs: []string{tc.Actor}, AnchorMessageID: in.TraceID}
				in.IncomingMedia = []IncomingMediaRef{{Source: *in.Source, Kind: "image", UploaderID: in.Sender, ObservationState: "verified"}}
				if err = s2.Observe(in, c, now); err != nil {
					t.Fatal(err)
				}
				before := s2.Context(in, c, now)
				if err = s2.Close(); err != nil {
					t.Fatal(err)
				}
				s2, err = NewStore(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer s2.Close()
				after := s2.Context(in, c, now)
				if len(after.Messages) != 1 || len(before.Messages) != 1 {
					t.Fatal("restart lost or duplicated source")
				}
				a, b := before.Messages[0], after.Messages[0]
				if a.EventID != b.EventID || b.Sender != in.Sender || !a.Time.Equal(b.Time) || b.Source == nil || b.Source.InstanceID != "restart-suite" || b.QuotedSource == nil || b.QuotedSource.Sender != tc.Other || len(b.IncomingMedia) != 1 || b.IncomingMedia[0].UploaderID != in.Sender || b.ReplyContext == nil || b.ReplyContext.TargetIDs[0] != in.Sender {
					t.Fatal("restart changed semantic identity", b)
				}
				if tc.Behavior == "duplicate-idempotent" {
					if err = s2.Observe(in, c, now.Add(time.Second)); err != nil {
						t.Fatal(err)
					}
					if len(s2.Context(in, c, now).Messages) != 1 {
						t.Fatal("duplicate event after restart")
					}
				}
			case "memory":
				ev := deepRepairEvent(t, s, c, in, now)
				m := MemoryEntry{Scope: ev.Scope, Subject: in.Sender, Kind: "fact", Detail: in.Text, Sources: []MemorySource{{ev.ID, in.Text}}}
				reject := false
				switch tc.Behavior {
				case "other-subject-rejected":
					m.Subject = tc.Other
					reject = true
				case "missing-source-rejected":
					m.Sources = nil
					reject = true
				case "wrong-quote-rejected":
					m.Sources[0].Quote = "not in source"
					reject = true
				}
				saved, err := s.SaveMemory(t.Context(), m, 0)
				if reject {
					if err == nil {
						t.Fatal("bad memory admitted")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				switch tc.Behavior {
				case "forget", "pinned-forget":
					if tc.Behavior == "pinned-forget" {
						saved.Pinned = true
						saved, err = s.SaveMemory(t.Context(), saved, saved.Version)
						if err != nil {
							t.Fatal(err)
						}
					}
					saved.Status = "deleted"
					if _, err = s.SaveMemory(t.Context(), saved, saved.Version); err != nil {
						t.Fatal(err)
					}
					page, err := s.SearchMemory(t.Context(), MemoryQuery{Scopes: []string{ev.Scope}, Query: in.Text})
					if err != nil || len(page.Items) > 0 {
						t.Fatal("forgotten entry recalled", err)
					}
				case "scope-isolated":
					if _, err = s.ReadMemory(t.Context(), []string{"wrong-scope"}, saved.ID); err == nil {
						t.Fatal("scope leak")
					}
				case "revision-conflict":
					saved.Detail = "changed"
					if _, err = s.SaveMemory(t.Context(), saved, saved.Version+1); err == nil {
						t.Fatal("stale revision overwrote")
					}
				case "correct":
					in.TraceID += "-correction"
					in.Text = "更正：corrected-source-" + tc.ID
					in.MessageTime = now.Add(time.Second).Format(time.RFC3339Nano)
					next := deepRepairEvent(t, s, c, in, now.Add(time.Second))
					m.ID = ""
					m.Sources = []MemorySource{{next.ID, in.Text}}
					m.Detail = in.Text
					m.Supersedes = saved.ID
					if _, err = s.SaveMemory(t.Context(), m, 0); err != nil {
						t.Fatal(err)
					}
					if _, err = s.ReadMemory(t.Context(), []string{ev.Scope}, saved.ID); err == nil {
						t.Fatal("superseded fact current")
					}
				}
			case "identity":
				in.Source = deepRepairSource(in, "identity-suite", now)
				if err := s.Observe(in, c, now); err != nil {
					t.Fatal(err)
				}
				again := in
				copy := *in.Source
				again.Source = &copy
				distinct := false
				collision := false
				switch tc.Behavior {
				case "namespace-distinct":
					again.Source.InstanceID = "other-instance"
					distinct = true
				case "server-collision":
					again.Source.ServerID = "other-server"
					collision = true
				case "time-collision":
					again.MessageTime = now.Add(24 * time.Hour).Format(time.RFC3339Nano)
					again.Source.OccurredAt = again.MessageTime
					collision = true
				case "sender-collision":
					again.Sender = tc.Other
					again.Source.Sender = tc.Other
					collision = true
				case "body-collision":
					again.Text = "different body"
					collision = true
				case "explicit-revision":
					again.Source.Revision = 2
					again.Text = "revised body"
				case "late-independent":
					again.TraceID = "late-id"
					again.Source.MessageID = again.TraceID
					again.Source.ServerID = "late-server"
					again.Source.OccurredAt = now.Add(-24 * time.Hour).Format(time.RFC3339Nano)
					again.MessageTime = again.Source.OccurredAt
					distinct = true
				}
				if err := s.Observe(again, c, now.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				scope := s.Context(again, c, now.Add(48*time.Hour))
				want := 1
				if distinct {
					want = 2
				}
				if collision {
					want = 0
				}
				if len(scope.Messages) != want {
					t.Fatal("identity disposition", tc.Behavior, len(scope.Messages), want)
				}
			default:
				t.Fatal("unknown category")
			}
		})
	}
	for _, group := range []string{"people", "media", "conversation", "restart", "memory", "identity"} {
		if counts[group] != 40 {
			t.Fatal(group, counts[group])
		}
	}
	t.Logf("suite=deep-repair-v1 catalogue_cases=240 executed_cases=%d production_paths=storage,context,grounding no_real_wechat_sends=true", selected)
}

func TestDeepRepairWriteScalingReport(t *testing.T) {
	if os.Getenv("WECHAT_DEEP_REPAIR_BENCH") != "1" {
		t.Skip("opt-in same-machine storage benchmark")
	}
	report := map[string]any{"suite": "deep-repair-v1", "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "samples": 100, "wechat_sends": 0}
	quantiles := func(v []int64) (int64, int64) {
		sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
		return v[len(v)/2], v[(len(v)*95/100)-1]
	}
	var p95s []int64
	for _, size := range []int{100, 1000} {
		s, c, _ := memoryFixture(t)
		c.MemoryMessages = 2000
		in := Input{Channel: "wechat", Account: "benchmark", Thread: "unrelated", Sender: "synthetic", Text: "unrelated source"}
		now := time.Now()
		for i := 0; i < size; i++ {
			in.TraceID = fmt.Sprint(i)
			if err := s.Observe(in, c, now); err != nil {
				t.Fatal(err)
			}
		}
		in.Thread = "fixed"
		var allocBefore, allocAfter runtime.MemStats
		runtime.ReadMemStats(&allocBefore)
		samples := []int64{}
		for i := 0; i < 100; i++ {
			in.TraceID = fmt.Sprint(i)
			start := time.Now()
			if err := s.Observe(in, c, now); err != nil {
				t.Fatal(err)
			}
			samples = append(samples, time.Since(start).Nanoseconds())
		}
		runtime.ReadMemStats(&allocAfter)
		p50, p95 := quantiles(samples)
		p95s = append(p95s, p95)
		report[fmt.Sprint(size)] = map[string]any{"p50_ns": p50, "p95_ns": p95, "allocated_bytes": allocAfter.TotalAlloc - allocBefore.TotalAlloc, "allocations": allocAfter.Mallocs - allocBefore.Mallocs, "logical_source_bytes": len(in.Text) * 100}
	}
	ratio := float64(p95s[1]) / float64(p95s[0])
	report["p95_growth_ratio"] = ratio
	report["threshold"] = 2.0
	report["passed"] = ratio <= 2
	raw, _ := json.MarshalIndent(report, "", "  ")
	if dest := os.Getenv("WECHAT_DEEP_REPAIR_REPORT"); dest != "" {
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(raw))
	if ratio > 2 {
		t.Fatal("unrelated growth exceeds bound", ratio)
	}
}

func deepRepairLiveScene(t *testing.T, s *Store, c Config, tc deepRepairCase, now time.Time) (Input, map[string]any, string, bool, []string) {
	t.Helper()
	in := deepRepairInput(tc, now)
	in.TraceID += "-query"
	in.Mode = "direct"
	in.Directed = true
	in.MustReply = true
	records := []map[string]any{}
	seq := 0
	add := func(sender, text, kind string) Input {
		seq++
		v := deepRepairInput(tc, now.Add(time.Duration(seq)*time.Second))
		v.Sender = sender
		v.TraceID = fmt.Sprintf("%s-source-%d", tc.ID, seq)
		v.Text = text
		literalOriginal := text
		v.OriginalText = &literalOriginal
		v.Source = deepRepairSource(v, "live-synthetic", now.Add(time.Duration(seq)*time.Second))
		v.Source.MessageType = 1
		if kind != "" {
			v.Source.MessageType = map[string]int{"image": 3, "voice": 34, "video": 43}[kind]
			state, observedRange := "verified", ""
			if tc.Group == "media" && tc.Behavior == "failed-observation" {
				state = "failed"
			}
			if tc.Group == "media" && tc.Behavior == "late-observation" {
				state = "pending"
			}
			if tc.Group == "media" && tc.Behavior == "video-range" {
				observedRange = "0s-2s; remaining 30s not observed"
			}
			original := ""
			v.OriginalText = &original
			v.IncomingMedia = []IncomingMediaRef{{Source: *v.Source, AssetID: "synthetic-asset-" + v.TraceID, Kind: kind, UploaderID: sender, Observation: text, ObservationState: state, ObservationVersion: 1, ObservedRange: observedRange}}
		}
		if err := s.Observe(v, c, time.Now()); err != nil {
			t.Fatal(err)
		}
		if kind != "" {
			proof, ok := s.SourceForInput(v)
			if !ok {
				t.Fatal("media source not persisted")
			}
			ev, readErr := s.ReadEvent(t.Context(), []string{ScopeID(v.Channel, v.Account, v.Thread)}, proof.EventID)
			if readErr != nil || ev.Text != "" || len(ev.IncomingMedia) != 1 || ev.IncomingMedia[0].Observation != text || ev.Source == nil || ev.Source.MessageType != v.Source.MessageType {
				t.Fatal("media observation promoted into user original", readErr, ev)
			}
		}
		records = append(records, map[string]any{"sender": sender, "message_id": v.TraceID, "text": text, "media_kind": kind})
		return v
	}
	current := func(text, expected string) (Input, map[string]any, string, bool, []string) {
		in.Text = text
		literalOriginal := text
		in.OriginalText = &literalOriginal
		in.Source = deepRepairSource(in, "live-synthetic", time.Now())
		in.Source.MessageType = 1
		in.MessageTime = in.Source.OccurredAt
		return in, map[string]any{"authorized_history": s.Context(in, c, time.Now()).Messages, "actor": tc.Actor, "other": tc.Other, "current_source": in.Source, "quoted_source": in.QuotedSource, "addressees": in.Addressees}, expected, in.MustReply, nil
	}
	social := func() { in.Mode = "social"; in.Directed = false; in.MustReply = false }
	switch tc.Group {
	case "people":
		switch tc.Behavior {
		case "father-addressed":
			add(tc.Other, "我刚和自己的父亲视频。", "")
			in.Addressees = []string{tc.Other}
			return current("你爸刚视频，刚挂掉。", "这句话由actor对other说，你爸属于other，不属于actor或AI；不得添加悲伤、站立等细节。")
		case "father-ambiguous":
			add(tc.Other, "刚才大家在聊家常。", "")
			social()
			return current("你爸刚视频，刚挂掉。", "受话者未知，允许安静或自然确认；不得认定为任何特定人的父亲，不添加情绪姿态。")
		case "same-name":
			v := add(tc.Other, "我今天去了苏州。", "")
			in.QuotedSource = v.Source
			return current("我们俩群昵称都叫小林。这条是谁说去了苏州？", "引用来源作者为other，不能说当前actor去了苏州；同名不能合并身份。")
		case "renamed":
			add(tc.Actor, "我原来群昵称叫小雨，现在改成星星了，我喜欢红茶。", "")
			in.SenderName = "星星"
			return current("我只是改了昵称，还记得我喜欢什么吗？", "actor只是改名，仍是同一稳定发言者，喜欢红茶。")
		case "third-party-report":
			add(tc.Other, "小周说他不喝咖啡，我本人喝咖啡。", "")
			return current("刚才是谁不喝咖啡？", "other转述小周不喝咖啡；不能把小周的偏好归给other或actor。")
		case "unsupported-emotion":
			add(tc.Other, "视频通话结束了。", "")
			social()
			return current("刚挂掉。", "通话结束没有证据证明任何人伤心、心里空或站在那里；可安静。")
		case "assistant-not-source":
			add(tc.Actor, "我平时不跑马拉松。", "")
			if err := s.AcceptedDelivery(in, "你上周跑完马拉松了。", "synthetic-wrong-assistant", c, now.Add(10*time.Second)); err != nil {
				t.Fatal(err)
			}
			return current("你刚才说我跑马拉松，有来源吗？", "承认无用户来源，与用户明确不跑马拉松冲突；不能把AI旧话当事实。")
		case "quoted-other-person":
			v := add(tc.Other, "我父亲今天打来视频，身体挺好。", "")
			in.QuotedSource = v.Source
			return current("这位的爸爸身体怎么样？", "根据明确引用，other父亲身体挺好；不能把父亲归给actor。")
		}
	case "media":
		switch tc.Behavior {
		case "meal-then-goodnight":
			add(tc.Other, "[视觉观察] 一份盒饭放在键盘旁，有一杯汤。", "image")
			social()
			return current("晚安咯。", "面向actor晚安，不用other上传的盒饭、汤或键盘梗。")
		case "same-caption-two-uploads":
			add(tc.Other, "[视觉观察] 红色杯子。", "image")
			add(tc.Actor, "[视觉观察] 蓝色杯子。", "image")
			return current("我刚发的这张杯子是什么颜色？", "actor的图片是蓝色，不能选other的红色杯子。")
		case "photo-not-ownership":
			v := add(tc.Other, "[视觉观察] 一位不明身份的人骑自行车。", "image")
			in.QuotedSource = v.Source
			return current("照片是他发的，能说明自行车是他的吗？", "上传者不等于物品主人或照片人物，无法确认自行车所有权。")
		case "late-observation":
			first := add(tc.Actor, "[图片已收到，等待读取]", "image")
			second := add(tc.Other, "[图片已收到，等待读取]", "image")
			// The second upload finishes first; each completion uses the original
			// trace and optimistic before value, never the latest media slot.
			for _, update := range []struct {
				source      Input
				observation string
			}{{second, "[视觉观察] 后发的图是水杯。"}, {first, "[视觉观察] 我先发的图是雨伞。"}} {
				changed, updateErr := s.UpdateObservation(ScopeID(in.Channel, in.Account, in.Thread), update.source.TraceID, update.source.Text, update.observation)
				if updateErr != nil || !changed {
					t.Fatal("out-of-order observation not committed", updateErr)
				}
			}
			if changed, updateErr := s.UpdateObservation(ScopeID(in.Channel, in.Account, in.Thread), first.TraceID, first.Text, "stale wrong water cup"); updateErr != nil || changed {
				t.Fatal("stale observation overwrote completed source", changed, updateErr)
			}
			for _, turn := range s.Context(in, c, time.Now()).Messages {
				if turn.TraceID != first.TraceID && turn.TraceID != second.TraceID {
					continue
				}
				if len(turn.IncomingMedia) != 1 {
					t.Fatal("media lineage missing")
				}
				m := turn.IncomingMedia[0]
				if m.UploaderID != turn.Sender || m.Source.MessageID != turn.TraceID || m.AssetID != "synthetic-asset-"+turn.TraceID || m.ObservationState != "observed" || m.ObservationVersion != 2 {
					t.Fatal("out-of-order observation changed source or uploader", turn)
				}
				if turn.TraceID == first.TraceID && !strings.Contains(m.Observation, "雨伞") || turn.TraceID == second.TraceID && !strings.Contains(m.Observation, "水杯") {
					t.Fatal("completion attached to wrong upload")
				}
			}
			proof, ok := s.SourceForInput(first)
			if !ok {
				t.Fatal("original media source unavailable after observation")
			}
			check := in
			check.QuotedSource = proof
			engine, engineErr := New(c, s, extractionFixture(func(context.Context, string, []map[string]string) (string, error) {
				return `{"action":"silent","messages":[]}`, nil
			}), "synthetic")
			if engineErr != nil {
				t.Fatal(engineErr)
			}
			rc := ReplyContext{TargetIDs: []string{in.Sender}, SourceEventIDs: []string{proof.EventID}, MediaSourceIDs: []string{first.IncomingMedia[0].AssetID}}
			if _, normalizeErr := engine.normalizeReplyContexts(t.Context(), check, Decision{Action: "reply", Messages: []string{"雨伞"}, ReplyContexts: []ReplyContext{rc}}); normalizeErr != nil {
				t.Fatal("completed original quote rejected", normalizeErr)
			}
			rc.MediaSourceIDs = []string{second.IncomingMedia[0].AssetID}
			if _, normalizeErr := engine.normalizeReplyContexts(t.Context(), check, Decision{Action: "reply", Messages: []string{"水杯"}, ReplyContexts: []ReplyContext{rc}}); normalizeErr == nil {
				t.Fatal("other upload admitted as original quoted material")
			}
			return current("我那张图刚识别出来，画面是什么？", "actor上传雨伞，不把后完成或后发的other水杯当actor图。")
		case "quoted-old-image":
			v := add(tc.Other, "[视觉观察] 去年拍的黄色自行车。", "image")
			add(tc.Actor, "[视觉观察] 今天拍的蓝色汽车。", "image")
			in.QuotedSource = v.Source
			return current("我引用的旧图里是什么？", "明确引用other旧图，黄色自行车，不能选最近蓝车。")
		case "failed-observation":
			v := add(tc.Actor, "[图片观察失败：没有可读画面]", "image")
			scope := s.Context(v, c, time.Now())
			_ = scope
			return current("这张图片上写了什么？", "观察失败，不能编造图片文字，应说明无法确定。")
		case "video-range":
			v := add(tc.Actor, "[视频观察] 只读取第0到2秒，看见一辆车；余下30秒未读。", "video")
			in.QuotedSource = v.Source
			return current("你看到后面结尾发生什么了吗？", "只看0到2秒，不能声称看过结尾，也不能编造后续。")
		case "voice-uploader":
			v := add(tc.Other, "[语音转写] 这是我转发的别人的录音：明天去杭州。", "voice")
			in.QuotedSource = v.Source
			return current("这一定是发语音的人本人的声音吗？", "转发上传者不能证明原说话人身份；不能把录音内容变成other本人的行程。")
		}
	case "conversation":
		switch tc.Behavior {
		case "other-speaker-source":
			add(tc.Other, "我今晚吃了面条。", "")
			add(tc.Actor, "我今晚还没有吃饭。", "")
			return current("我今晚吃过饭了吗？", "actor明确还没吃饭，other面条不能归actor。")
		case "same-speaker-source":
			add(tc.Actor, "我明天去苏州。", "")
			add(tc.Other, "我明天去成都。", "")
			return current("我刚才说明天去哪？", "actor明天苏州，不是other成都。")
		case "explicit-quote":
			v := add(tc.Other, "周末想爬山。", "")
			in.QuotedSource = v.Source
			return current("你引用的这位周末想做什么？", "other想爬山，明确说是引用那位，不能归给actor。")
		case "unrelated-mention":
			add(tc.Actor, "我不喝咖啡。", "")
			add(tc.Other, "我喜欢咖啡。", "")
			in.MentionedIDs = []string{tc.Other}
			return current("虽然我提到了别人，但我自己喝咖啡吗？", "被提及other的偏好不能覆盖actor自述，actor不喝。")
		case "cross-thread-source", "cross-account-source":
			foreign := in
			foreign.TraceID += "-foreign"
			foreign.Text = "隔离的其他账号暗号 ORCHID-SECRET"
			if tc.Behavior == "cross-thread-source" {
				foreign.Thread = "foreign-room"
			} else {
				foreign.Account = "foreign-account"
			}
			foreignOriginal := foreign.Text
			foreign.OriginalText = &foreignOriginal
			foreign.Source = deepRepairSource(foreign, "live-synthetic", now)
			foreign.Source.MessageType = 1
			if err := s.Observe(foreign, c, now); err != nil {
				t.Fatal(err)
			}
			r, e, x, m, _ := current("另一个会话的暗号是什么？", "当前来源没有该资料，不能披露或猜测其他会话暗号。")
			return r, e, x, m, []string{"ORCHID-SECRET"}
		case "assistant-source":
			add(tc.Actor, "你好。", "")
			if err := s.AcceptedDelivery(in, "你拥有一辆红色跑车。", "synthetic-unsourced-car", c, now); err != nil {
				t.Fatal(err)
			}
			return current("你说我有跑车是根据我哪句话？", "没有用户来源，不应以AI旧回复证明拥有跑车。")
		case "missing-anchor":
			social()
			return current("就那个呀。", "没有可解析的那个对象，允许确认或安静，不猜出具体素材。")
		}
	case "restart":
		v := add(tc.Actor, "我的下次计划是星期六去苏州。", "")
		other := add(tc.Other, "我的计划是星期日去成都。", "")
		_ = other
		switch tc.Behavior {
		case "quoted-source-stable":
			in.QuotedSource = v.Source
		case "incoming-media-stable":
			add(tc.Actor, "[视觉观察] 绿色雨伞。", "image")
		case "reply-target-stable":
			in.Addressees = []string{tc.Actor}
		case "name-not-identity":
			in.SenderName = "改名后的成员"
		case "duplicate-idempotent":
			if err := s.Observe(v, c, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		if tc.Behavior == "incoming-media-stable" {
			return current("重启后，我之前那张图里是什么？", "actor图中绿色雨伞，不是其他人素材。")
		}
		return current("恢复后，你还记得我的下次出行计划吗？", "actor星期六去苏州，不是other星期日成都；恢复不改变归属。")
	case "memory":
		old := add(tc.Actor, "我下次去杭州。", "")
		scope := s.Context(old, c, time.Now())
		eid := ""
		for _, v := range scope.Messages {
			if v.TraceID == old.TraceID {
				eid = v.EventID
			}
		}
		m, err := s.SaveMemory(t.Context(), MemoryEntry{Scope: scope.ID, Subject: tc.Actor, Kind: "fact", Detail: "下次去杭州", Sources: []MemorySource{{eid, old.Text}}}, 0)
		if err != nil {
			t.Fatal(err)
		}
		switch tc.Behavior {
		case "correct":
			next := add(tc.Actor, "更正一下，我下次去苏州，不去杭州。", "")
			nextScope := s.Context(next, c, time.Now())
			id := ""
			for _, v := range nextScope.Messages {
				if v.TraceID == next.TraceID {
					id = v.EventID
				}
			}
			if _, err = s.SaveMemory(t.Context(), MemoryEntry{Scope: scope.ID, Subject: tc.Actor, Kind: "fact", Detail: "下次去苏州，不去杭州", Supersedes: m.ID, Sources: []MemorySource{{id, next.Text}}}, 0); err != nil {
				t.Fatal(err)
			}
			return current("我更正后下次去哪里？", "苏州是更正后目的地，杭州已被替代。")
		case "forget", "pinned-forget":
			m.Status = "deleted"
			if _, err = s.SaveMemory(t.Context(), m, m.Version); err != nil {
				t.Fatal(err)
			}
			records = []map[string]any{{"sender": tc.Actor, "text": "[原条目已遗忘，不可据此回答]"}}
			r, e, x, must, _ := current("我以前说的目的地是什么？", "该来源已遗忘，不能复述目的地或凭AI记忆恢复。")
			return r, e, x, must, []string{"杭州"}
		case "other-subject-rejected":
			bad := m
			bad.ID = ""
			bad.Subject = tc.Other
			if _, err = s.SaveMemory(t.Context(), bad, 0); err == nil {
				t.Fatal("invalid subject saved")
			}
			return current("我的目的地可以算成别人的计划吗？", "不能，actor去杭州，不应变成other的行程。")
		case "missing-source-rejected", "wrong-quote-rejected":
			bad := m
			bad.ID = ""
			bad.Detail = "拥有一艘游艇"
			if tc.Behavior == "missing-source-rejected" {
				bad.Sources = nil
			} else {
				bad.Sources[0].Quote = "不存在的游艇原话"
			}
			if _, err = s.SaveMemory(t.Context(), bad, 0); err == nil {
				t.Fatal("invalid fact saved")
			}
			return current("我有游艇这件事有可靠来源吗？", "没有游艇的来源，不能编造成已有事实。")
		case "scope-isolated":
			return current("刚才这份杭州计划属于谁？", "该计划属于actor，不能自动跨群共享或归其他人。")
		case "revision-conflict":
			bad := m
			bad.Detail = "去成都"
			if _, err = s.SaveMemory(t.Context(), bad, m.Version+1); err == nil {
				t.Fatal("stale revision accepted")
			}
			return current("有效记录中，我下次去哪里？", "版本冲突更新未提交，有效记录仍杭州，不能说成都。")
		}
	case "identity":
		original := add(tc.Actor, "我上周去了杭州，这不是今天的行程。", "")
		again := original
		sourceCopy := *original.Source
		again.Source = &sourceCopy
		switch tc.Behavior {
		case "namespace-distinct":
			again.Source.InstanceID = "separate-dataset"
			again.TraceID += "-different"
			again.Source.MessageID = again.TraceID
			again.Text = "我今天在成都。"
		case "server-collision":
			again.Source.ServerID = "collision-server"
		case "time-collision":
			again.Source.OccurredAt = now.Add(-5 * 24 * time.Hour).Format(time.RFC3339Nano)
		case "sender-collision":
			again.Sender = tc.Other
			again.Source.Sender = tc.Other
		case "body-collision":
			again.Text = "我今天住在成都。"
		case "explicit-revision":
			again.Source.Revision = 2
			again.Text = "更正：我上周去了苏州，不是杭州。"
		case "late-independent":
			again.TraceID += "-late"
			again.Source.MessageID = again.TraceID
			again.Source.ServerID = "late-server"
			again.Source.OccurredAt = now.Add(-7 * 24 * time.Hour).Format(time.RFC3339Nano)
			again.Text = "这是七天前的旧照片，并非今天。"
		}
		again.MessageTime = again.Source.OccurredAt
		againOriginal := again.Text
		again.OriginalText = &againOriginal
		if err := s.Observe(again, c, time.Now()); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(tc.Behavior, "collision") {
			records = []map[string]any{{"text": "[此来源身份存在冲突，不能作确定事实]"}}
			return current("现在能确定我今天在哪里吗？", "身份冲突资料不能作当前事实，不能猜测今天地点。")
		}
		if tc.Behavior == "explicit-revision" {
			return current("更正后，上周去的是哪里？", "更正为苏州，不再将杭州作为当前确定记录。")
		}
		return current("根据这些资料，能说我今天去了杭州吗？", "旧日事件不能当今天，不能说今天去了杭州；如有独立今天成都仅限其明确来源。")
	}
	t.Fatal("unimplemented live behavior", tc.Behavior)
	return Input{}, nil, "", false, nil
}

type deepRepairRecordingModel struct {
	client interface {
		Chat(context.Context, string, []map[string]string) (string, error)
		ChatFunctions(context.Context, string, []map[string]string, []mcptools.Tool) (string, error)
		ChatGrounding(context.Context, string, []map[string]string, deepseek.GroundingOptions) (string, error)
	}
	responses                []map[string]string
	captureGroundingRequests bool
	chatModel                string
}

func (m *deepRepairRecordingModel) Chat(ctx context.Context, system string, turns []map[string]string) (string, error) {
	out, err := m.client.Chat(ctx, system, turns)
	m.responses = append(m.responses, map[string]string{"stage": "chat", "response": out})
	return out, err
}
func (m *deepRepairRecordingModel) ChatGrounding(ctx context.Context, system string, turns []map[string]string, options deepseek.GroundingOptions) (string, error) {
	effective, err := options.Normalized()
	if err != nil {
		return "", err
	}
	type requestEvent struct {
		Event       string               `json:"event"`
		Diagnostics deepseek.Diagnostics `json:"diagnostics"`
	}
	events := []requestEvent{}
	ctx = deepseek.WithRequestObserver(ctx, func(event string, d deepseek.Diagnostics) { events = append(events, requestEvent{event, d}) })
	out, err := m.client.ChatGrounding(ctx, system, turns, options)
	encoded, _ := json.Marshal(effective)
	record := map[string]string{"stage": "grounding", "response": out, "effective_grounding_options": string(encoded)}
	metadata, _ := json.Marshal(events)
	record["request_diagnostics"] = string(metadata)
	httpAttempts, formatRepairs := 0, 0
	for _, event := range events {
		if event.Event == "model_grounding_response" {
			httpAttempts++
		}
		if event.Event == "model_grounding_format_repair" {
			formatRepairs++
		}
	}
	record["http_attempts"], record["format_repair_retries"] = fmt.Sprint(httpAttempts), fmt.Sprint(formatRepairs)
	if diagnostic, ok := deepseek.ErrorDiagnostics(err); ok {
		terminal, _ := json.Marshal(diagnostic)
		record["terminal_diagnostics"] = string(terminal)
	}
	actualModel := effective.Model
	if actualModel == "" {
		actualModel = m.chatModel
	}
	record["actual_review_model"] = actualModel
	if m.captureGroundingRequests {
		request, _ := json.Marshal(map[string]any{"system": system, "messages": turns, "options": effective})
		record["synthetic_request"] = string(request)
	}
	m.responses = append(m.responses, record)
	return out, err
}

// This is included in configuration fingerprints, independently of the short
// generation budget. The recording wrapper also persists the actual forwarded
// options for every review call, without logging hidden reasoning.
func deepRepairModelConfig(baseURL, model string, compatible bool, declaredReviewModel ...string) map[string]any {
	options, err := (deepseek.GroundingOptions{}).Normalized()
	if err != nil {
		panic(err)
	}
	thinking, effort := "enabled", options.ReasoningEffort
	if compatible {
		thinking, effort = "not_sent", "not_sent"
	}
	reviewModel, protocol := model, "structured-review-default-v8"
	if len(declaredReviewModel) > 0 && declaredReviewModel[0] != "" {
		reviewModel, protocol = declaredReviewModel[0], "structured-review-explicit-model-v10"
	}
	return map[string]any{"base_url": baseURL, "model": model, "compatible": compatible, "max_tokens": 1024, "tool_max_tokens": 1024,
		"review_protocol": protocol, "actual_review_model": reviewModel,
		"grounding": map[string]any{"interface": "ChatGrounding", "model": reviewModel, "thinking": thinking, "response_format": "json_object", "reasoning_effort": effort, "max_tokens": options.MaxTokens, "timeout_seconds": options.Timeout.Seconds()}}
}

func deepRepairGenerationBudgets(maxTokens, toolMaxTokens int) (int, int, error) {
	if maxTokens == 0 {
		maxTokens = 1024
	}
	if toolMaxTokens == 0 {
		toolMaxTokens = 1024
	}
	if maxTokens < 64 || maxTokens > 4096 || toolMaxTokens < 64 || toolMaxTokens > 4096 {
		return 0, 0, errors.New("generation budget exceeds the actual model client bounds")
	}
	return maxTokens, toolMaxTokens, nil
}

func deepRepairModelConfigWithBudgets(baseURL, model string, compatible bool, reviewModel string, maxTokens, toolMaxTokens int, profiles ...GroundingSettings) map[string]any {
	config := deepRepairModelConfig(baseURL, model, compatible, reviewModel)
	config["max_tokens"], config["tool_max_tokens"] = maxTokens, toolMaxTokens
	if len(profiles) > 0 {
		profile := profiles[0]
		grounding := config["grounding"].(map[string]any)
		grounding["max_tokens"], grounding["timeout_seconds"] = profile.MaxTokens, profile.TimeoutSeconds
		if !compatible {
			grounding["reasoning_effort"] = profile.ReasoningEffort
		}
		budget := profile
		budget.Model = ""
		if budget != GroundingDefaults() {
			config["review_protocol"] = "structured-review-explicit-profile-v11"
		}
	}
	return config
}

func deepRepairApplyReviewProfile(c *Config, profile GroundingSettings) {
	// These acceptance fixtures intentionally exercise the optional reviewer.
	profile.Enabled = true
	c.Grounding = &profile
}

func deepRepairApplyReviewModel(c *Config, model string) {
	if model == "" {
		return
	}
	settings := c.GroundingConfig()
	settings.Model = model
	c.Grounding = &settings
}

func deepRepairFrozenReviewConfig(raw []byte) error {
	_, err := deepRepairDeclaredReviewProfile(raw)
	return err
}

func deepRepairDeclaredReviewProfile(raw []byte) (GroundingSettings, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return GroundingSettings{}, err
	}
	var declared struct {
		Model           string `json:"review_model"`
		MaxTokens       int    `json:"review_max_tokens"`
		TimeoutSeconds  int    `json:"review_timeout_seconds"`
		ReasoningEffort string `json:"review_reasoning_effort"`
	}
	if err := json.Unmarshal(raw, &declared); err != nil {
		return GroundingSettings{}, errors.New("invalid explicit review profile")
	}
	settings := (Config{Grounding: &GroundingSettings{Model: declared.Model, MaxTokens: declared.MaxTokens, TimeoutSeconds: declared.TimeoutSeconds, ReasoningEffort: declared.ReasoningEffort}}).GroundingConfig()
	if _, err := (Config{Grounding: &settings}).GroundingOptions(); err != nil {
		return GroundingSettings{}, err
	}
	for key, value := range fields {
		if key == "grounding" {
			var explicit GroundingSettings
			decoder := json.NewDecoder(strings.NewReader(string(value)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&explicit); err != nil {
				return GroundingSettings{}, errors.New("invalid declared evaluation grounding settings")
			}
			if explicit.Model != "" && explicit.Model != settings.Model || explicit.MaxTokens != 0 && explicit.MaxTokens != settings.MaxTokens || explicit.TimeoutSeconds != 0 && explicit.TimeoutSeconds != settings.TimeoutSeconds || explicit.ReasoningEffort != "" && explicit.ReasoningEffort != settings.ReasoningEffort {
				return GroundingSettings{}, errors.New("nested grounding settings must match the consumed explicit review_* fields; overrides cannot be silently ignored")
			}
		} else if key != "review_model" && key != "review_max_tokens" && key != "review_timeout_seconds" && key != "review_reasoning_effort" && (strings.HasPrefix(key, "grounding_") || strings.HasPrefix(key, "review_")) {
			return GroundingSettings{}, errors.New("unsupported review configuration field; evaluation cannot silently ignore an override")
		}
	}
	return settings, nil
}

func deepRepairOuterDeadline(c Config, reviews int) time.Duration {
	options, err := c.GroundingOptions()
	if err != nil {
		panic(err)
	}
	grace := 10 * time.Second
	if reviews > 1 {
		grace = 120 * time.Second
	}
	return time.Duration(reviews)*options.Timeout + grace
}
func (m *deepRepairRecordingModel) ChatFunctions(ctx context.Context, system string, turns []map[string]string, tools []mcptools.Tool) (string, error) {
	out, err := m.client.ChatFunctions(ctx, system, turns, tools)
	m.responses = append(m.responses, map[string]string{"stage": "functions", "response": out})
	return out, err
}

// Real model calls use synthetic state only. The explicit config contains a key
// file path, never a copied key; neither config nor output belongs in Git.
func TestDeepRepairRealModelEvaluation(t *testing.T) {
	configFile := os.Getenv("WECHAT_DEEP_REPAIR_MODEL_CONFIG")
	if configFile == "" {
		t.Skip("opt-in real-model synthetic evaluation")
	}
	raw, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal("model config unavailable")
	}
	var cfg struct {
		BaseURL       string `json:"base_url"`
		Model         string `json:"model"`
		ReviewModel   string `json:"review_model"`
		MaxTokens     int    `json:"max_tokens"`
		ToolMaxTokens int    `json:"tool_max_tokens"`
		KeyFile       string `json:"key_file"`
		Compatible    bool   `json:"compatible"`
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.KeyFile == "" {
		t.Fatal("invalid explicit model config")
	}
	cfg.MaxTokens, cfg.ToolMaxTokens, err = deepRepairGenerationBudgets(cfg.MaxTokens, cfg.ToolMaxTokens)
	if err != nil {
		t.Fatal(err)
	}
	reviewProfile, err := deepRepairDeclaredReviewProfile(raw)
	if err != nil {
		t.Fatal(err)
	}
	usage := []deepseek.Usage{}
	provider, err := deepseek.New(deepseek.Config{OnUsage: func(u deepseek.Usage) { usage = append(usage, u) }, BaseURL: cfg.BaseURL, Model: cfg.Model, KeyFile: cfg.KeyFile, Compatible: cfg.Compatible, MaxTokens: cfg.MaxTokens, ToolMaxTokens: cfg.ToolMaxTokens})
	if err != nil {
		t.Fatal("model initialization failed")
	}
	client := deepRepairGuardProvider(provider)
	type result struct {
		ModelResponses []map[string]string `json:"synthetic_model_responses,omitempty"`
		Current        *Input              `json:"synthetic_current,omitempty"`
		Evidence       map[string]any      `json:"synthetic_source_evidence,omitempty"`
		Expected       string              `json:"expected"`
		MustRespond    bool                `json:"must_respond"`
		ID             string              `json:"id"`
		Behavior       string              `json:"behavior"`
		Action         string              `json:"action"`
		Messages       []string            `json:"messages"`
		ReplyContexts  []ReplyContext      `json:"reply_contexts"`
		Structural     bool                `json:"structural_pass"`
		Rubric         map[string]bool     `json:"rubric"`
		Error          string              `json:"error,omitempty"`
	}
	fixtureBytes, _ := os.ReadFile("testdata/deep_repair/scenarios.json")
	fixtureHash := fmt.Sprintf("%x", sha256.Sum256(fixtureBytes))
	protocolBytes, _ := os.ReadFile("deep_repair_acceptance_test.go")
	controlBytes, controlErr := os.ReadFile("deep_repair_provider_block_test.go")
	if controlErr != nil {
		t.Fatal(controlErr)
	}
	protocolBytes = append(protocolBytes, controlBytes...)
	protocolHash := fmt.Sprintf("%x", sha256.Sum256(protocolBytes))
	modelConfig := deepRepairModelConfigWithBudgets(cfg.BaseURL, cfg.Model, cfg.Compatible, cfg.ReviewModel, cfg.MaxTokens, cfg.ToolMaxTokens, reviewProfile)
	modelConfigBytes, _ := json.Marshal(modelConfig)
	configHash := fmt.Sprintf("%x", sha256.Sum256(modelConfigBytes))
	var sourceManifest struct {
		SHA256 string `json:"sha256"`
		Method string `json:"method"`
	}
	manifestBytes, manifestErr := os.ReadFile(os.Getenv("WECHAT_DEEP_REPAIR_SOURCE_MANIFEST"))
	if manifestErr != nil || json.Unmarshal(manifestBytes, &sourceManifest) != nil || sourceManifest.SHA256 == "" {
		t.Fatal("selected production source manifest required; use scripts/deep_repair_evaluate.py")
	}
	results := []result{}
	catalogue := deepRepairCatalogue(t)
	selected := []deepRepairCase{}
	for _, tc := range catalogue {
		if group := os.Getenv("WECHAT_DEEP_REPAIR_MODEL_GROUP"); group != "" && group != tc.Group {
			continue
		}
		if behavior := os.Getenv("WECHAT_DEEP_REPAIR_MODEL_BEHAVIOR"); behavior != "" && behavior != tc.Behavior {
			continue
		}
		selected = append(selected, tc)
	}
	catalogue = selected
	if len(catalogue) == 0 {
		t.Fatal("no cases match selection")
	}
	limit := len(catalogue)
	if v := os.Getenv("WECHAT_DEEP_REPAIR_MODEL_LIMIT"); v != "" {
		if _, err = fmt.Sscan(v, &limit); err != nil || limit < 1 || limit > len(catalogue) {
			t.Fatal("invalid explicit model limit")
		}
	}
	selectionBytes, _ := json.Marshal(catalogue[:limit])
	configHash = fmt.Sprintf("%x", sha256.Sum256(append([]byte(configHash), selectionBytes...)))
	passed, required, responded := 0, 0, 0
	dest := os.Getenv("WECHAT_DEEP_REPAIR_MODEL_REPORT")
	plannedCaseIDs := []string{}
	for _, item := range catalogue[:limit] {
		plannedCaseIDs = append(plannedCaseIDs, item.ID)
	}
	writeReport := func(partial bool) {
		attemptedCaseIDs := []string{}
		for _, item := range results {
			attemptedCaseIDs = append(attemptedCaseIDs, item.ID)
		}
		report := map[string]any{"fixture_sha256": fixtureHash, "protocol_sha256": protocolHash, "config_sha256": configHash, "production_sha256": sourceManifest.SHA256, "production_hash_method": sourceManifest.Method, "model_config": modelConfig, "version": "deep-repair-live-v8", "model": cfg.Model, "synthetic_only": true, "wechat_sends": 0, "cases": len(results), "requested_cases": limit, "passed": passed, "required_response_cases": required, "responded": responded, "partial": partial, "rubric_method": "same-provider model rubric plus deterministic structure; not human acceptance", "results": results, "usage": usage, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH}
		client.Progress(report, plannedCaseIDs, attemptedCaseIDs)
		if dest != "" {
			out, _ := json.MarshalIndent(report, "", "  ")
			if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(dest+".pending", out, 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.Rename(dest+".pending", dest); err != nil {
				t.Fatal(err)
			}
		}
	}
	completed := map[string]bool{}
	if dest != "" {
		if old, readErr := os.ReadFile(dest); readErr == nil {
			if os.Getenv("WECHAT_DEEP_REPAIR_RESUME") != "1" {
				t.Fatal("report exists; choose a new path or explicit resume")
			}
			if blockedErr := deepRepairRefuseBlockedResume(old); blockedErr != nil {
				t.Fatal(blockedErr)
			}
			var checkpoint struct {
				Fixture    string           `json:"fixture_sha256"`
				Protocol   string           `json:"protocol_sha256"`
				Config     string           `json:"config_sha256"`
				Production string           `json:"production_sha256"`
				Results    []result         `json:"results"`
				Passed     int              `json:"passed"`
				Required   int              `json:"required_response_cases"`
				Responded  int              `json:"responded"`
				Usage      []deepseek.Usage `json:"usage"`
			}
			if json.Unmarshal(old, &checkpoint) != nil || checkpoint.Fixture != fixtureHash || checkpoint.Protocol != protocolHash || checkpoint.Config != configHash || checkpoint.Production != sourceManifest.SHA256 {
				t.Fatal("checkpoint protocol/config mismatch")
			}
			results = checkpoint.Results
			passed = checkpoint.Passed
			required = checkpoint.Required
			responded = checkpoint.Responded
			usage = checkpoint.Usage
			for _, row := range results {
				completed[row.ID] = true
			}
		}
	}
	for _, tc := range catalogue[:limit] {
		if completed[tc.ID] {
			continue
		}
		s, c, _ := memoryFixture(t)
		deepRepairApplyReviewProfile(&c, reviewProfile)
		c.Enabled = true
		c.Permissions.ReplyText = true
		c.Permissions.ObserveGroups = true
		c.Permissions.AutonomousGroups = true
		c.QuietStart = "00:00"
		c.QuietEnd = "00:00"
		now := time.Now().UTC().Add(-time.Minute)
		in, evidence, expected, mustRespond, forbidden := deepRepairLiveScene(t, s, c, tc, now)
		if tc.Group == "restart" {
			dir := filepath.Dir(s.path)
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = NewStore(dir)
			if err != nil {
				t.Fatal(err)
			}
		}
		recordingModel := &deepRepairRecordingModel{client: client, chatModel: cfg.Model, captureGroundingRequests: true}
		engine, engineErr := New(c, s, recordingModel, "你是合成测试群中的AI伙伴，依据当前实际消息自然回复。")
		if engineErr != nil {
			t.Fatal(engineErr)
		}
		callCtx, cancel := context.WithTimeout(t.Context(), deepRepairOuterDeadline(engine.Config, 3))
		client.Begin(tc.ID, "generation")
		d, e := engine.Respond(deepseek.WithPurpose(callCtx, "synthetic_quality_generation"), in)
		cancel()
		row := result{Current: &in, Evidence: evidence, Expected: expected, MustRespond: mustRespond, ModelResponses: recordingModel.responses, ID: tc.ID, Behavior: tc.Behavior, Action: d.Action, Messages: d.Messages, ReplyContexts: d.ReplyContexts, Structural: e == nil, Rubric: map[string]bool{}}
		if e != nil {
			row.Error = e.Error()
			if diagnostic, ok := deepseek.ErrorDiagnostics(e); ok {
				row.Error = fmt.Sprintf("%s stage=%s http=%d attempts=%d", diagnostic.Code, diagnostic.Stage, diagnostic.HTTPStatus, diagnostic.Attempts)
			}
		}
		if client.Block() != nil {
			row.Structural = false
			row.Error = client.FailureText()
		}
		if mustRespond {
			required++
			if d.Action == "reply" && len(d.Messages) > 0 {
				responded++
			} else {
				row.Structural = false
			}
		}
		if e == nil && client.Block() == nil {
			payload, _ := json.Marshal(map[string]any{"actor": tc.Actor, "other": tc.Other, "current": in.Text, "mode": in.Mode, "evidence": evidence, "must_respond": mustRespond, "action": d.Action, "expected": expected, "reply": d.Messages, "reply_contexts": d.ReplyContexts})
			judgeCtx, judgeCancel := context.WithTimeout(t.Context(), 45*time.Second)
			client.Begin(tc.ID, "rubric")
			answer, jerr := client.Chat(deepseek.WithPurpose(judgeCtx, "synthetic_quality_rubric"), "你是合成测试判分器。只根据给出的expected逐项判断回复。仅输出JSON {\"correct_person\":true,\"supported_facts\":true,\"correct_time\":true,\"useful_response\":true}；存在错误为false。允许安静的social场景中silent可合格；must_respond=true时silent的useful_response必须false。中性询问（如“聊得怎么样”）本身不是关于姿态、情绪、亲属身份的事实断言；只在它明确预设了错误人物或具体事件时判错。第三人转述允许以清楚的来源描述或名字指代，不强求原生ID；逐个气泡按同序号reply_contexts.target_ids确定实际受话者，“你”指该目标；没有绑定时才回退actor。入站evidence的addressees是原话的受话者，不是AI回复的目标。必须比较事件参与者与实际回复对象，不能因为原话对actor说就把other的经历转成actor的。例如other说“你爸刚视频，刚挂掉”，就算已知其“你爸”是actor的父亲，也不能回复target=actor“你刚和他视频结束”：原文没有说actor参与了视频；该句明确预设actor参与，correct_person和supported_facts应为false。不能把other的经历或偏好说成actor的。空房间、耳鸣、站立、心理余温等若来源没有仍是无据细节，不能因为文学表达而放行。不要推测不可见来源。", []map[string]string{{"role": "user", "content": string(payload)}})
			judgeCancel()
			if jerr != nil || json.Unmarshal([]byte(answer), &row.Rubric) != nil || len(row.Rubric) != 4 {
				row.Structural = false
				row.Error = "rubric_unavailable"
			}
		}
		if client.Block() != nil {
			row.Structural = false
			row.Error = client.FailureText()
		}
		for _, word := range forbidden {
			if client.Block() == nil && strings.Contains(strings.Join(d.Messages, " "), word) {
				row.Structural = false
				row.Error = "forbidden_source_recalled"
			}
		}
		ok := row.Structural
		for _, name := range []string{"correct_person", "supported_facts", "correct_time", "useful_response"} {
			ok = ok && row.Rubric[name]
		}
		if ok {
			passed++
		}
		results = append(results, row)
		writeReport(true)
		if s != nil {
			_ = s.Close()
		}
		if client.Block() != nil {
			t.Fatalf("provider blocked; partial checkpoint preserved: %s", client.FailureText())
		}
	}
	writeReport(false)
	t.Logf("real-model synthetic cases=%d passed=%d required=%d responded=%d no_wechat_sends", len(results), passed, required, responded)
	if passed*100 < len(results)*95 || required > 0 && responded*100 < required*95 {
		t.Fatal("real model quality or response threshold not met")
	}
}

func TestDeepRepairRealModelScenesPrepareWithoutNetwork(t *testing.T) {
	for _, tc := range deepRepairCatalogue(t) {
		t.Run(tc.ID, func(t *testing.T) {
			s, c, _ := memoryFixture(t)
			in, evidence, expected, _, _ := deepRepairLiveScene(t, s, c, tc, time.Now().Add(-time.Minute))
			if in.Text == "" || len(evidence) == 0 || expected == "" {
				t.Fatal("incomplete live stimulus")
			}
			if in.OriginalText == nil || *in.OriginalText != in.Text || in.Source == nil || in.Source.MessageID != in.TraceID || in.Source.ServerID == "" || in.Source.Sender != in.Sender || in.Source.OccurredAt != in.MessageTime {
				t.Fatal("live user input does not match native literal-source shape")
			}
		})
	}
}
