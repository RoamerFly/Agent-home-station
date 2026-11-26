package bot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type quotedTestSource struct {
	*fakeSource
	resolution linuxnative.QuoteResolution
}

type quotedLiteralModel struct {
	current  map[string]any
	contents []string
}

func requestActorFromPacket(packet map[string]any) string {
	for _, key := range []string{"request_actor_id", "message_sender_id", "speaker_id"} {
		if value, exists := packet[key]; exists {
			actor, _ := value.(string)
			return actor
		}
	}
	return ""
}

func (m *quotedLiteralModel) Chat(_ context.Context, _ string, turns []map[string]string) (string, error) {
	for _, turn := range turns {
		m.contents = append(m.contents, turn["content"])
		var row map[string]any
		if json.Unmarshal([]byte(turn["content"]), &row) == nil && row["kind"] == "current_message" {
			m.current = row
		}
	}
	return `{"action":"silent","messages":[]}`, nil
}

func TestPrivateNativeQuoteBodyNeverEntersModelOrStoredQuote(t *testing.T) {
	w, _, c, _ := mediaWorker(t)
	model := &quotedLiteralModel{}
	engine, err := companion.New(c.Config, c.Store, model, "test")
	if err != nil {
		t.Fatal(err)
	}
	w.model = engine
	w.config.Channel = "wechat"
	thread := "synthetic-private-owner"
	w.allowed[thread] = true
	m := fixtureMessage("private-followup", thread, "/ai 继续核对", "incoming")
	m.Type, m.Quote = 49, &linuxnative.QuoteReference{ServerID: "777", Type: 1}
	original := fixtureMessage("private-receipt", thread, "合成跨来源私聊正文", "outgoing")
	original.ServerID = "777"
	w.source = &quotedTestSource{fakeSource: &fakeSource{messages: map[string]linuxnative.Message{m.ID: m}}, resolution: linuxnative.QuoteResolution{AccountID: "test", MessageID: m.ID, ThreadID: thread, State: "resolved", Message: &original}}
	checks := 0
	w.config.ProtectQuote = func(_ context.Context, got linuxnative.Message) bool {
		checks++
		return got.ServerID == original.ServerID && got.ThreadID == thread
	}
	w.handle(t.Context(), fixtureEvent("1", m.ID, thread, "incoming", "wechat"))
	if w.persistErr != nil || checks != 1 || model.current == nil || strings.Contains(strings.Join(model.contents, "\n"), original.Body) || model.current["quoted_source"] != nil {
		t.Fatal("protected quote reached the model or current request was lost", checks, model.current, w.persistErr)
	}
	scope := c.Store.Context(companion.Input{Channel: "wechat", Account: "test", Thread: thread}, c.Config, time.Now())
	for _, turn := range scope.Messages {
		if turn.TraceID != m.ID {
			continue
		}
		if turn.QuotedSource != nil || strings.Contains(turn.Text, original.Body) {
			t.Fatal("protected quote entered ordinary memory", turn)
		}
		return
	}
	t.Fatal("current request was not stored")
}

func TestNativeQuotedTextPreservesOwnLiteralAndSeparateQuotedAuthor(t *testing.T) {
	w, _, c, _ := mediaWorker(t)
	model := &quotedLiteralModel{}
	engine, err := companion.New(c.Config, c.Store, model, "test")
	if err != nil {
		t.Fatal(err)
	}
	w.model = engine
	engine.Config.QuietStart, engine.Config.QuietEnd = "00:00", "00:00"
	w.config.Channel = "wechat"
	records := []Record{}
	w.config.Record = func(r Record) { records = append(records, r) }
	w.config.GroupRules = map[string]GroupRule{"room@chatroom": {Mode: "social"}}
	engine.Config.Permissions.ObserveGroups = true
	engine.Config.Permissions.AutonomousGroups = true
	type57 := 57
	m := fixtureMessage("new-native-quote", "room@chatroom", "  这是我自己的新更正  ", "incoming")
	literal := strings.TrimSpace(m.Body)
	canonical := m
	canonical.Body = literal
	m.SenderID = "quoting-member"
	m.ServerID = "888"
	m.Type = 49
	m.AppMessageType = &type57
	m.Quote = &linuxnative.QuoteReference{ServerID: "777", Type: 1}
	original := fixtureMessage("referenced-user-message", m.ThreadID, "另一位成员的原话", "incoming")
	original.SenderID = "quoted-member"
	original.ServerID = "777"
	w.source = &quotedTestSource{fakeSource: &fakeSource{messages: map[string]linuxnative.Message{m.ID: m}}, resolution: linuxnative.QuoteResolution{AccountID: "test", MessageID: m.ID, ThreadID: m.ThreadID, State: "resolved", Message: &original}}
	w.handle(context.Background(), fixtureEvent("1", m.ID, m.ThreadID, "incoming", "wechat"))
	if w.persistErr != nil {
		t.Fatal(w.persistErr)
	}
	if model.current == nil || model.current["text"] != literal || requestActorFromPacket(model.current) != m.SenderID {
		t.Fatal("first context lost current quoted text", model.current, records)
	}
	q, ok := model.current["quoted_source"].(map[string]any)
	if !ok || q["sender"] != original.SenderID || q["message_id"] != original.ID {
		t.Fatal("first context lost separate quote author", model.current)
	}
	quoted, ok := model.current["quoted_message_evidence"].(map[string]any)
	if !ok || quoted["text"] != original.Body || quoted["message_sender_id"] != original.SenderID {
		t.Fatal("original from the native quote was unavailable before any stored copy existed")
	}
	in := companion.Input{Channel: "wechat", Account: "test", Thread: m.ThreadID}
	scope := c.Store.Context(in, c.Config, time.Now())
	for _, turn := range scope.Messages {
		if turn.TraceID == m.ID {
			ev, err := c.Store.ReadEvent(context.Background(), []string{scope.ID}, turn.EventID)
			if err != nil || ev.Text != literal || ev.Sender != m.SenderID || turn.QuotedSource == nil || turn.QuotedSource.Sender != original.SenderID || turn.Source == nil || turn.Source.MessageType != 49 || turn.Source.ServerID != m.ServerID || turn.Source.BodyHash != messageSource(canonical).BodyHash {
				t.Fatal("quoted literal/source projection wrong", ev, turn, err)
			}
			return
		}
	}
	t.Fatal("quoted user event not stored")
}

func (s *quotedTestSource) QuotedMessage(context.Context, string) (linuxnative.QuoteResolution, error) {
	return s.resolution, nil
}

func TestQuotedVoiceSenderIsNotPresentedAsVoiceSpeaker(t *testing.T) {
	w, _, _, _ := mediaWorker(t)
	current := fixtureMessage("quoting-event", "room@chatroom", "这是转发的语音", "incoming")
	current.SenderID = "quoting-member"
	current.Quote = &linuxnative.QuoteReference{ServerID: "voice-server", Type: 34}
	voice := fixtureMessage("voice-event", current.ThreadID, "转写中的我", "incoming")
	voice.SenderID, voice.ServerID, voice.Type = "uploading-member", "voice-server", 34
	w.source = &quotedTestSource{fakeSource: &fakeSource{}, resolution: linuxnative.QuoteResolution{AccountID: "test", MessageID: current.ID, ThreadID: current.ThreadID, State: "resolved", Message: &voice}}
	body := w.readQuoteContext(context.Background(), &current)
	start := strings.Index(body, "{")
	var packet map[string]any
	if start < 0 || json.Unmarshal([]byte(body[start:]), &packet) != nil {
		t.Fatal("missing quote packet", body)
	}
	if packet["request_actor_id"] != current.SenderID || packet["message_sender_id"] != voice.SenderID || packet["quoted_message_sender_id"] != voice.SenderID || packet["voice_speaker_id"] != nil || packet["voice_speaker_state"] != "unknown" {
		t.Fatal("quote sender/voice roles were conflated", packet)
	}
	if _, exists := packet["speaker_id"]; exists {
		t.Fatal("voice uploader became a literal speaker", packet)
	}
	if packet["text"] != "[语音]" {
		t.Fatal("unverified quoted transcript became user wording", packet)
	}
}

func TestQuotedReplyKeepsUserTextAndVerifiedBotReferenceWithoutFakeMention(t *testing.T) {
	w, _, _, _ := mediaWorker(t)
	type57 := 57
	no := false
	m := fixtureMessage("incoming-quote", "room@chatroom", "还是文件吧", "incoming")
	m.Type = 49
	m.AppMessageType = &type57
	m.Quote = &linuxnative.QuoteReference{ServerID: "777", Type: 1}
	m.Mentions.State = "known"
	m.Mentions.Self = &no
	original := fixtureMessage("bot-original", "room@chatroom", "这首音乐可以发原文件", "outgoing")
	original.SenderID = "test"
	original.ServerID = "777"
	w.source = &quotedTestSource{fakeSource: &fakeSource{}, resolution: linuxnative.QuoteResolution{AccountID: "test", MessageID: m.ID, ThreadID: m.ThreadID, State: "resolved", Message: &original}}
	quoteText := w.readQuoteContext(context.Background(), &m)
	if !m.ReplyToSelf || *m.Mentions.Self || !strings.Contains(quoteText, "这首音乐可以发原文件") || !strings.Contains(quoteText, `"from_bot_account":true`) {
		t.Fatal(m, quoteText)
	}
	w.config.GroupRules = map[string]GroupRule{"room@chatroom": {Mode: "mention"}}
	working := quotedPlainBody(m)
	prompt, eligible := w.prompt(working, time.Now())
	if !eligible || prompt != "还是文件吧" || !w.addressed(m) || strings.Contains(messageSummary(m), "分享卡片") {
		t.Fatal("quoted text misclassified", prompt, eligible, messageSummary(m))
	}
	bad := w.source.(*quotedTestSource)
	bad.resolution.ThreadID = "other@chatroom"
	m.ReplyToSelf = false
	quoteText = w.readQuoteContext(context.Background(), &m)
	if m.ReplyToSelf || strings.Contains(quoteText, "这首音乐可以发原文件") {
		t.Fatal("cross-chat quoted content admitted")
	}
}

func TestQuotedShareRetainsExactSourceWithoutNeedingCachedAsset(t *testing.T) {
	w, _, _, _ := mediaWorker(t)
	m := fixtureMessage("quote-card", "room@chatroom", "把这个原视频发我", "incoming")
	m.Quote = &linuxnative.QuoteReference{ServerID: "777", Type: 49}
	original := fixtureMessage("original-card", m.ThreadID, "", "outgoing")
	original.Type, original.ServerID, original.SenderID = 49, "777", "test"
	original.Link = &linuxnative.LinkCard{Title: "猫咪看雪", Description: "公开播放卡片", URL: "https://www.bilibili.com/video/BV1425hzpEtn/?p=2"}
	w.source = &quotedTestSource{fakeSource: &fakeSource{}, resolution: linuxnative.QuoteResolution{AccountID: "test", MessageID: m.ID, ThreadID: m.ThreadID, State: "resolved", Message: &original}}
	w.runtime.Media = []*mediaDelivery{{Input: companion.Input{Thread: m.ThreadID, Sender: "alice"}, Kind: "share", Title: original.Link.Title, URL: original.Link.URL, Status: linuxnative.ToolStatus{ServerID: "777"}}}
	raw := w.readQuoteContext(context.Background(), &m)
	if !m.ReplyToSelf || !strings.Contains(raw, `"source_url":"https://www.bilibili.com/video/BV1425hzpEtn/?p=2"`) || !strings.Contains(raw, `"kind":"share"`) || !strings.Contains(raw, `"requester_id":"alice"`) {
		t.Fatal(raw)
	}
	// After runtime history retention, the verified native card still supplies
	// its exact source; a same-title recent card must not replace this one.
	w.runtime.Media = nil
	raw = w.readQuoteContext(context.Background(), &m)
	if !strings.Contains(raw, `"source_url":"https://www.bilibili.com/video/BV1425hzpEtn/?p=2"`) || !strings.Contains(raw, `"message_id":"original-card"`) || !strings.Contains(raw, `"server_id":"777"`) {
		t.Fatal(raw)
	}
	for _, bad := range []string{"javascript:alert(1)", "https://user:password@example.org/video", "file:///private/movie"} {
		if quotedSourceURL(bad) != "" {
			t.Fatal("unsafe quoted source", bad)
		}
	}
}

func TestQuotedShareSelectsOriginalForAnalysisAndMediaProvenance(t *testing.T) {
	w, _, c, _ := mediaWorker(t)
	type57 := 57
	current := fixtureMessage("quoted-share-request", "room@chatroom", "总结一下这个视频", "incoming")
	current.Type, current.AppMessageType = 49, &type57
	current.SenderID, current.ServerID = "requester", "request-server"
	current.Quote = &linuxnative.QuoteReference{ServerID: "share-server", Type: 49}
	original := fixtureMessage("original-share", current.ThreadID, "", "incoming")
	original.Type, original.ServerID, original.SenderID = 49, "share-server", "share-sender"
	original.Link = &linuxnative.LinkCard{Title: "公开视频", Description: "卡片简介", URL: "https://v.kuaishou.com/example"}
	w.source = &quotedTestSource{fakeSource: &fakeSource{}, resolution: linuxnative.QuoteResolution{AccountID: "test", MessageID: current.ID, ThreadID: current.ThreadID, State: "resolved", Message: &original}}
	if got := w.readQuoteContext(context.Background(), &current); got == "" || current.ResolvedQuote == nil {
		t.Fatal("quote was not resolved", got)
	}
	analysis, ok := quotedAnalysisMessage(current)
	if !ok || analysis.ID != original.ID || analysis.Link == nil || analysis.Link.URL != original.Link.URL {
		t.Fatalf("quoted share was not selected as analysis target: ok=%v analysis=%+v", ok, analysis)
	}
	engine, err := companion.New(c.Config, c.Store, &quotedLiteralModel{}, "test")
	if err != nil {
		t.Fatal(err)
	}
	input := companion.Input{Channel: "wechat", Account: "test", Thread: current.ThreadID, Sender: current.SenderID, TraceID: current.ID, MessageTime: current.CreatedAt}
	bindInputSource(&input, current, engine)
	if len(input.IncomingMedia) != 1 || input.IncomingMedia[0].Source.MessageID != original.ID || input.IncomingMedia[0].Kind != "share" {
		t.Fatalf("quoted share media provenance lost: %+v", input.IncomingMedia)
	}
}

func TestQuotedShareWithNewVideoURLAnalyzesCurrentReply(t *testing.T) {
	type57 := 57
	current := fixtureMessage("quoted-new-video", "room@chatroom", "看这条 https://b23.tv/eqZkIxJ", "incoming")
	current.Type, current.AppMessageType = 49, &type57
	original := fixtureMessage("original-share", current.ThreadID, "", "incoming")
	original.Type = 49
	original.Link = &linuxnative.LinkCard{Title: "旧分享", URL: "https://b23.tv/HrIgMVh"}
	current.ResolvedQuote = &original
	analysis, selectedOriginal := quotedAnalysisMessage(current)
	if selectedOriginal || analysis.ID != current.ID || analysis.Body != current.Body {
		t.Fatalf("new link should remain the analysis target: original=%v analysis=%+v", selectedOriginal, analysis)
	}
	_, _, c, _ := mediaWorker(t)
	engine, err := companion.New(c.Config, c.Store, &quotedLiteralModel{}, "test")
	if err != nil {
		t.Fatal(err)
	}
	input := companion.Input{Channel: "wechat", Account: "test", Thread: current.ThreadID, Sender: current.SenderID, TraceID: current.ID, OriginalText: &current.Body}
	bindInputSource(&input, current, engine)
	if input.QuotedSource == nil || len(input.IncomingMedia) != 0 {
		t.Fatalf("old quoted share must be context only when a new link is present: %+v", input)
	}
	current.Body = "总结引用的视频"
	analysis, selectedOriginal = quotedAnalysisMessage(current)
	if !selectedOriginal || analysis.ID != original.ID {
		t.Fatalf("quoted card should remain the target without a new link: original=%v analysis=%+v", selectedOriginal, analysis)
	}
}

func TestQuotedNativeVideoPreservesCurrentLiteralAndChecksOriginalPermission(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		enabled bool
		foreign bool
	}{
		{name: "readable", enabled: true},
		{name: "video disabled"},
		{name: "different conversation", enabled: true, foreign: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			w, _, base, _ := mediaWorker(t)
			model := &quotedLiteralModel{}
			engine, err := companion.New(base.Config, base.Store, model, "test")
			if err != nil {
				t.Fatal(err)
			}
			engine.Config.QuietStart, engine.Config.QuietEnd = "00:00", "00:00"
			w.model, w.config.Channel = engine, "wechat"
			w.config.GroupRules = map[string]GroupRule{"room@chatroom": {Mode: "all"}}
			w.config.Options.ReadVideos = scenario.enabled
			observer := &typedCountingObservationAnalyzer{}
			w.config.Analyzer = observer
			current := fixtureMessage("quote-request", "room@chatroom", "画面有什么？", "incoming")
			current.Type, current.SenderID = 49, "quoting-member"
			current.Quote = &linuxnative.QuoteReference{ServerID: "777", Type: 43}
			original := fixtureMessage("quoted-native-video", current.ThreadID, "", "incoming")
			original.Type, original.ServerID, original.SenderID = 43, "777", "original-uploader"
			if scenario.foreign {
				original.ThreadID = "foreign@chatroom"
			}
			w.source = &quotedTestSource{fakeSource: &fakeSource{messages: map[string]linuxnative.Message{current.ID: current}}, resolution: linuxnative.QuoteResolution{AccountID: "test", MessageID: current.ID, ThreadID: current.ThreadID, State: "resolved", Message: &original}}
			w.handle(t.Context(), fixtureEvent("1", current.ID, current.ThreadID, "incoming", "wechat"))
			if w.persistErr != nil || model.current["text"] != current.Body || requestActorFromPacket(model.current) != current.SenderID {
				t.Fatal("current quoted request was lost or rewritten", w.persistErr, model.current)
			}
			parts := strings.Join(model.contents, "\n")
			if scenario.enabled && !scenario.foreign {
				if observer.calls.Load() != 1 || !strings.Contains(parts, "实际帧内容："+original.ID) || !strings.Contains(parts, companion.ObservationVisualFrames) {
					t.Fatal("exact original video observation was not available to the model")
				}
				obs, _ := model.current["media_observations"].([]any)
				if len(obs) != 1 {
					t.Fatal("wrong number of media sources", obs)
				}
				media := obs[0].(map[string]any)
				source := media["source"].(map[string]any)
				if source["message_id"] != original.ID || media["uploader_id"] != original.SenderID {
					t.Fatal("video evidence attributed to the quoting user", media)
				}
			} else if observer.calls.Load() != 0 || strings.Contains(parts, "实际帧内容：") {
				t.Fatal("video permission or conversation boundary bypassed")
			}
		})
	}
}

func TestMediaProvenanceSurvivesRuntimeRecovery(t *testing.T) {
	w, _, c, in := mediaWorker(t)
	in.Sender, in.SenderName, in.OriginTraceID, in.TaskID = "alice", "小甲", "original-request", "task-1"
	m := companion.GeneratedMedia{ID: "source", Kind: "voice", AssetID: "audio", ParentAssetID: "video", CachedMIME: "audio/mp4", URL: "https://example.org/video", Title: "音轨"}
	if !w.queueMedia(context.Background(), c, in, m) {
		t.Fatal("not queued")
	}
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	d := w.runtime.Media[0]
	if d.ParentAssetID != "video" || d.CachedMIME != "audio/mp4" || d.Input.OriginTraceID != "original-request" {
		t.Fatal(d)
	}
	d.State = "accepted"
	w.mediaRecord(d, "media_sent", "sent")
	scope := c.Store.Context(in, c.Config, time.Now())
	if len(scope.Messages) == 0 {
		t.Fatal("no memory")
	}
	got := scope.Messages[len(scope.Messages)-1].Media
	if got == nil || got.ParentAssetID != "video" || got.CachedMIME != "audio/mp4" || got.RequestTraceID != "original-request" || got.RequesterID != "alice" {
		t.Fatal(got)
	}
}
