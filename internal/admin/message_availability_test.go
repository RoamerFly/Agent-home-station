package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/linuxnative"
)

// The source emits a new committed native message after the production worker
// establishes its startup tail. No management read or real WeChat send is used.
type availabilityNative struct {
	*fakeNative
	message    linuxnative.Message
	started    bool
	downloads  atomic.Int32
	quoteReads atomic.Int32
	channels   atomic.Int32
	file       bool
	failFile   bool
}

func (n *availabilityNative) Events(_ context.Context, after string) (linuxnative.EventPage, error) {
	if !n.started {
		n.started = true
		return linuxnative.EventPage{NextCursor: "0"}, nil
	}
	if after == "0" {
		event := linuxnative.Event{Cursor: "1", Kind: "message.committed"}
		event.Data.MessageID, event.Data.ThreadID = n.message.ID, n.message.ThreadID
		event.Data.Direction, event.Data.Origin = "incoming", "remote"
		return linuxnative.EventPage{Items: []linuxnative.Event{event}, NextCursor: "1"}, nil
	}
	return linuxnative.EventPage{NextCursor: after}, nil
}

func (n *availabilityNative) Message(_ context.Context, id string) (linuxnative.Message, error) {
	if id != n.message.ID {
		return linuxnative.Message{}, errors.New("unexpected synthetic message request")
	}
	return n.message, nil
}

func (n *availabilityNative) QuotedMessage(_ context.Context, id string) (linuxnative.QuoteResolution, error) {
	n.quoteReads.Add(1)
	if id != n.message.ID || !n.message.IsQuotedText() {
		return linuxnative.QuoteResolution{}, errors.New("unexpected quote lookup")
	}
	original := n.message
	original.ID, original.ServerID, original.SenderID = "original-parent-message", "original-server", "parent-other"
	original.Type, original.Body, original.Quote, original.AppMessageType = 1, "这是另一位家长的原话", nil, nil
	return linuxnative.QuoteResolution{AccountID: n.Account(), MessageID: id, ThreadID: n.message.ThreadID, State: "resolved", Message: &original}, nil
}

func (n *availabilityNative) IncomingMedia(context.Context, string, int64) ([]byte, string, error) {
	n.downloads.Add(1)
	return nil, "", errors.New("unexpected media download before installed capability")
}

func (n *availabilityNative) ChannelsMedia(_ context.Context, id string) (linuxnative.ChannelsSource, error) {
	n.channels.Add(1)
	if id != n.message.ID || n.message.AppMessageType == nil || *n.message.AppMessageType != 51 {
		return linuxnative.ChannelsSource{}, errors.New("unexpected channels source request")
	}
	return linuxnative.ChannelsSource{AccountID: n.Account(), MessageID: id, ThreadID: n.message.ThreadID, ObjectID: "synthetic-object", State: "identified"}, nil
}

func (n *availabilityNative) IncomingMediaTo(_ context.Context, id, path string, _ int64) (int64, string, error) {
	n.downloads.Add(1)
	if !n.file || id != n.message.ID || n.failFile {
		return 0, "", errors.New("synthetic attachment unavailable")
	}
	data := []byte("synthetic parent-group attachment original")
	return int64(len(data)), "text/plain", os.WriteFile(path, data, 0600)
}

type availabilityModel struct {
	mu             sync.Mutex
	calls          int
	packet         map[string]any
	all            string
	answer         string
	groundingQuote string
}

// The synthetic reviewer cites the exact known observation in the host's
// evidence table. This tests the real review protocol, not model judgment quality.
func (m *availabilityModel) ChatGrounding(_ context.Context, _ string, turns []map[string]string, _ deepseek.GroundingOptions) (string, error) {
	var packet struct {
		Units    []companion.GroundingReviewUnit  `json:"review_units"`
		Evidence []companion.GroundingEvidenceRow `json:"evidence_table"`
	}
	if len(turns) != 1 || json.Unmarshal([]byte(turns[0]["content"]), &packet) != nil || len(packet.Units) == 0 || m.groundingQuote == "" {
		return "", errors.New("unexpected synthetic grounding request")
	}
	ref := ""
	for _, row := range packet.Evidence {
		if row.Kind == "media_observation" && strings.Contains(row.Text, m.groundingQuote) {
			ref = row.Ref
			break
		}
	}
	if ref == "" {
		return "", errors.New("synthetic summary lacks its actual observation evidence")
	}
	units := []companion.GroundingUnitVerdict{}
	for _, unit := range packet.Units {
		units = append(units, companion.GroundingUnitVerdict{UnitID: unit.ID, Atoms: []companion.GroundingAtom{{Span: unit.Text, Mode: "assertion", Claim: unit.Text, Status: "supported", Supports: []companion.GroundingSupport{{Ref: ref, Quote: m.groundingQuote}}, Reason: "Synthetic fixed observation supports only this test summary or unavailability report."}}})
	}
	raw, _ := json.Marshal(map[string]any{"units": units})
	return string(raw), nil
}

func (m *availabilityModel) Chat(_ context.Context, _ string, turns []map[string]string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	for _, turn := range turns {
		m.all += turn["content"]
		var packet map[string]any
		if json.Unmarshal([]byte(turn["content"]), &packet) == nil && packet["kind"] == "current_message" {
			m.packet = packet
		}
	}
	if m.answer != "" {
		return m.answer, nil
	}
	return `{"action":"silent","messages":[]}`, nil
}

type availabilityObserver func(context.Context, linuxnative.Message) (string, error)

func (observe availabilityObserver) Analyze(ctx context.Context, message linuxnative.Message) (string, error) {
	return observe(ctx, message)
}

func TestParentGroupMessageAvailabilityRoutesAndRetainsOriginalSource(t *testing.T) {
	for _, kind := range []string{"text", "quoted", "image", "sticker", "sticker-unavailable", "voice", "native-video", "channels-share", "public-share", "file", "file-unavailable"} {
		t.Run(kind, func(t *testing.T) {
			var f *webFixture
			if kind == "sticker-unavailable" {
				f, _, _, _ = mediaPolicyFixture(t, false)
			} else {
				f = setup(t)
			}
			msg := linuxnative.Message{AccountID: "test", ID: "synthetic-" + kind, ServerID: "server-" + kind, ThreadID: "synthetic-parent-group@chatroom", SenderID: "parent-current", Type: 1, Body: "这是当前家长自己的正文", Direction: "incoming", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
			mentioned := true
			msg.Mentions.State, msg.Mentions.Self = "known", &mentioned
			n := &availabilityNative{fakeNative: f.native}
			subtype := 0
			switch kind {
			case "quoted":
				msg.Type, subtype = 49, 57
				msg.AppMessageType = &subtype
				msg.Quote = &linuxnative.QuoteReference{Type: 1, ServerID: "original-server"}
			case "image":
				msg.Type = 3
			case "sticker", "sticker-unavailable":
				msg.Type = 47
			case "voice":
				msg.Type = 34
			case "native-video":
				msg.Type = 43
			case "channels-share", "public-share":
				msg.Type, subtype = 49, 5
				if kind == "channels-share" {
					subtype = 51
				}
				msg.AppMessageType = &subtype
				msg.Link = &linuxnative.LinkCard{Title: "未经观察的合成分享标题", URL: "https://public.example/synthetic?token=synthetic-private-token"}
			case "file", "file-unavailable":
				msg.Type, subtype = 49, 6
				msg.AppMessageType = &subtype
				msg.Attachments = []linuxnative.Attachment{{Kind: "file", Filename: "parent-notes.txt", MessageID: msg.ID, State: "identified"}}
				n.file, n.failFile = true, kind == "file-unavailable"
			}
			if kind != "text" && kind != "quoted" {
				msg.Body = ""
			}
			n.message, f.manager.native = msg, n
			cfg := f.manager.Config().Companion
			cfg.Enabled, cfg.Permissions.Remember, cfg.Permissions.ObserveGroups = true, true, true
			cfg.QuietStart, cfg.QuietEnd = "00:00", "00:00"
			cfg.Conversation = companion.ConversationDefaults()
			cfg.Conversation.AggregateMS = 0
			f.manager.mu.Lock()
			f.manager.config.Companion = cfg
			f.manager.config.Behavior.ReadImages, f.manager.config.Behavior.ReadVideos = !n.file, !n.file
			f.manager.mu.Unlock()
			model := &availabilityModel{}
			engine, err := companion.New(cfg, f.manager.memory, model, "synthetic parent-group availability contract")
			if err != nil {
				t.Fatal(err)
			}
			options := bot.DefaultOptions()
			options.ReadImages, options.ReadVideos = !n.file, !n.file
			options.RandomDelay, options.RecoverBacklog = false, false
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			var settled, incomplete atomic.Bool
			var report string
			worker, err := bot.New(bot.Config{ObservationFailed: func(in companion.Input, reason string) error {
				if in.TraceID != msg.ID || in.Thread != msg.ThreadID || report != "" {
					t.Error("incorrect or duplicate private report")
				}
				report = reason
				cancel()
				return nil
			}, Channel: "wechat", StateDir: filepath.Join(f.dir, "availability-worker"), AllowedThreads: []string{msg.ThreadID}, PollInterval: time.Millisecond, Options: &options, Analyzer: mediaUnderstanding{manager: f.manager}, GroupRules: map[string]bot.GroupRule{msg.ThreadID: {Mode: "mention"}}, Record: func(record bot.Record) {
				f.manager.record(record)
				if record.MessageID == msg.ID && record.Event == "group_input_read_failed" {
					incomplete.Store(true)
				}
				if record.MessageID == msg.ID && record.Status == "silent" {
					settled.Store(true)
					cancel()
				}
			}}, n, engine, n)
			if err != nil {
				t.Fatal(err)
			}
			if err = worker.Run(ctx); err != nil {
				t.Fatal(err)
			}
			model.mu.Lock()
			calls, packet, all := model.calls, model.packet, model.all
			model.mu.Unlock()
			isPlain := kind == "text" || kind == "quoted"
			failed := !isPlain && kind != "file"
			if failed {
				if !incomplete.Load() || report == "" || settled.Load() || calls != 0 || packet != nil {
					t.Fatalf("failed read incorrectly reached group: report=%q calls=%d", report, calls)
				}
			} else if incomplete.Load() || report != "" || !settled.Load() || calls != 1 || packet == nil {
				t.Fatal("readable message did not reach one decision", calls)
			}
			wantDownloads := int32(0)
			switch kind {
			case "file", "sticker-unavailable":
				wantDownloads = 1
			case "file-unavailable":
				wantDownloads = 2
			}
			if downloads := n.downloads.Load(); downloads != wantDownloads {
				t.Fatal("wrong media/file dispatch", downloads, wantDownloads)
			}
			if n.quoteReads.Load() != 0 && kind != "quoted" || kind == "quoted" && n.quoteReads.Load() != 1 {
				t.Fatal("wrong quote dispatch", n.quoteReads.Load())
			}
			if n.channels.Load() != 0 && kind != "channels-share" || kind == "channels-share" && n.channels.Load() != 1 {
				t.Fatal("channels and public-share dispatch were confused", n.channels.Load())
			}
			if isPlain && packet["text"] != msg.Body {
				t.Fatal("current user's literal was replaced", packet["text"])
			}
			if !failed && (packet["request_actor_id"] != msg.SenderID || packet["thread"] != msg.ThreadID || packet["message_id"] != msg.ID) {
				t.Fatal("current request actor or conversation changed")
			}
			if kind == "file" {
				media, ok := packet["media_observations"].([]any)
				if !ok || len(media) != 1 {
					t.Fatal("missing original media reference")
				}
				ref, ok := media[0].(map[string]any)
				wantState := "observed"
				if !ok || ref["observation_state"] != wantState || ref["uploader_id"] != msg.SenderID {
					t.Fatal("media availability or uploader identity changed", ref["observation_state"])
				}
				if ref["result_available"] != (kind == "file") || ref["voice_speaker_id"] != nil || ref["transcript_first_person_id"] != nil {
					t.Fatal("unavailable media became evidence or uploader became voice speaker")
				}
			}
			if kind == "sticker-unavailable" && !strings.Contains(report, "sticker_download_unavailable") {
				t.Fatal("sticker download failure became a successful observation")
			}
			if kind == "quoted" {
				quoted, ok := packet["quoted_source"].(map[string]any)
				if !ok || quoted["sender"] != "parent-other" || quoted["message_id"] != "original-parent-message" {
					t.Fatal("quote lost separate original actor")
				}
			}
			if strings.Contains(all, "synthetic-private-token") {
				t.Fatal("private share parameter reached model context")
			}
			if kind == "file" && (!strings.Contains(all, "收到文件原件") || !strings.Contains(all, "asset_id")) {
				t.Fatal("file was treated as share or vision instead of retained original")
			}
			scope := f.manager.memory.Context(companion.Input{Channel: "wechat", Account: "test", Thread: msg.ThreadID, Sender: msg.SenderID}, cfg, time.Now())
			found := false
			for _, turn := range scope.Messages {
				if turn.TraceID != msg.ID {
					continue
				}
				event, readErr := f.manager.memory.ReadEvent(t.Context(), []string{scope.ID}, turn.EventID)
				if readErr != nil || event.Sender != msg.SenderID || turn.Source == nil || turn.Source.MessageID != msg.ID || turn.Source.MessageType != msg.Type || turn.Source.Account != msg.AccountID || turn.Source.Thread != msg.ThreadID || turn.Source.ServerID != msg.ServerID {
					t.Fatal("original source identity lost", readErr)
				}
				if event.Text != msg.Body {
					t.Fatal("observation or placeholder became original user text")
				}
				found = true
			}
			if !found {
				t.Fatal("original event was not retained")
			}
		})
	}
}

func TestParentGroupPreferredChannelsSummaryCreatesSourceBoundPlanWithoutSending(t *testing.T) {
	for _, observed := range []bool{true, false} {
		name := "verified-observation"
		if !observed {
			name = "observation-unavailable"
		}
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			kind := 51
			msg := linuxnative.Message{ID: "preferred-channels", ServerID: "preferred-server", AccountID: "test", ThreadID: "synthetic-parent-group@chatroom", SenderID: "parent-uploader", Type: 49, AppMessageType: &kind, Direction: "incoming", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
			n := &availabilityNative{fakeNative: f.native, message: msg}
			cfg := f.manager.Config().Companion
			cfg.Enabled, cfg.Permissions.Remember = true, true
			cfg.Permissions.ObserveGroups, cfg.Permissions.AutonomousGroups, cfg.Permissions.ReplyText = true, true, true
			cfg.QuietStart, cfg.QuietEnd = "00:00", "00:00"
			cfg.Conversation = companion.ConversationDefaults()
			cfg.Conversation.AggregateMS = 0
			seed := companion.Input{Channel: "wechat", Account: "test", Thread: msg.ThreadID, Sender: "parent-uploader", TraceID: "preference-source", Text: "本群分享默认总结发送"}
			if err := f.manager.memory.Observe(seed, cfg, time.Now()); err != nil {
				t.Fatal(err)
			}
			if err := f.manager.memory.SetPreferences(companion.ScopeID("wechat", "test", msg.ThreadID), "分享默认总结发送", 0); err != nil {
				t.Fatal(err)
			}
			answer := "实际内容暂未读取到，目前不能总结这段视频。"
			if observed {
				answer = "视频展示了折纸步骤：先对折，再沿折痕展开。"
			}
			raw, _ := json.Marshal(map[string]any{"action": "reply", "messages": []string{answer}})
			model := &availabilityModel{answer: string(raw)}
			model.groundingQuote = "实际视频内容未取得"
			if observed {
				model.groundingQuote = "画面展示先对折纸张，再沿折痕展开"
			}
			engine, err := companion.New(cfg, f.manager.memory, model, "synthetic preferred Channels summary")
			if err != nil {
				t.Fatal(err)
			}
			observerCalls := 0
			analyzer := availabilityObserver(func(ctx context.Context, current linuxnative.Message) (string, error) {
				observerCalls++
				ref, ok := companion.MediaSourceFromContext(ctx)
				if !ok || ref.MessageID != msg.ID || ref.Account != msg.AccountID || ref.Thread != msg.ThreadID || ref.Sender != msg.SenderID || current.ID != msg.ID {
					return "", errors.New("synthetic observation source changed")
				}
				event, err := f.manager.memory.ReadEvent(ctx, []string{companion.ScopeID("wechat", "test", msg.ThreadID)}, ref.EventID)
				if err != nil || event.Text != "" {
					return "", errors.New("original share source not retained before observation")
				}
				if !observed {
					return "", &bot.ObservationUnavailableError{Observation: "只有卡片，实际视频内容未取得。", Reason: "synthetic_observation_unavailable"}
				}
				return "[视频号实际画面观察，合成证据]画面展示先对折纸张，再沿折痕展开；未验证音频。", nil
			})
			options := bot.DefaultOptions()
			options.RandomDelay, options.RecoverBacklog = false, false
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			var planned, reported atomic.Bool
			var recordsMu sync.Mutex
			var reasons []string
			dir := filepath.Join(f.dir, "preferred-share-worker")
			worker, err := bot.New(bot.Config{ObservationFailed: func(in companion.Input, reason string) error {
				if in.TraceID != msg.ID || in.Thread != msg.ThreadID || reason == "" {
					t.Error("lost failure scope")
				}
				reported.Store(true)
				cancel()
				return nil
			}, Channel: "wechat", StateDir: dir, AllowedThreads: []string{msg.ThreadID}, PollInterval: time.Millisecond, Options: &options, Analyzer: analyzer, GroupRules: map[string]bot.GroupRule{msg.ThreadID: {Mode: "mention"}}, Record: func(record bot.Record) {
				recordsMu.Lock()
				reasons = append(reasons, record.Event+":"+record.Reason)
				recordsMu.Unlock()
				if record.Event == "utterance_plan_queued" && record.MessageID == msg.ID {
					planned.Store(true)
					cancel()
				}
			}}, n, engine, n)
			if err != nil {
				t.Fatal(err)
			}
			if err = worker.Run(ctx); err != nil {
				t.Fatal(err)
			}
			model.mu.Lock()
			packet := model.packet
			model.mu.Unlock()
			if !observed {
				if !reported.Load() || planned.Load() || packet != nil || observerCalls != 1 {
					t.Fatal("unreadable share reached group reply or lacked private report", reported.Load(), planned.Load(), observerCalls)
				}
				return
			}
			if !planned.Load() || observerCalls != 1 || packet == nil {
				recordsMu.Lock()
				defer recordsMu.Unlock()
				t.Fatal("preferred unmentioned Channels card did not become a reply plan", planned.Load(), observerCalls, reasons)
			}
			if packet["conversation_preferences"] != "分享默认总结发送" || packet["mode"] != "social" || packet["directed"] != false {
				t.Fatal("preference missing, or an explicit mention/request was fabricated")
			}
			media, ok := packet["media_observations"].([]any)
			if !ok || len(media) != 1 {
				t.Fatal("observation missing from engine input")
			}
			observation, ok := media[0].(map[string]any)
			if !ok || observation["result_available"] != observed {
				t.Fatal("verified and unavailable observations were confused")
			}
			var state struct {
				Plans []struct {
					Trace    string                 `json:"trace"`
					Input    companion.Input        `json:"input"`
					Messages []string               `json:"messages"`
					Sent     []string               `json:"sent"`
					Quote    *linuxnative.ToolQuote `json:"quote"`
				} `json:"utterance_plans"`
			}
			data, err := os.ReadFile(filepath.Join(dir, "messages-v2.json"))
			if err != nil || json.Unmarshal(data, &state) != nil || len(state.Plans) != 1 {
				t.Fatal("reply plan was not persisted", err)
			}
			plan := state.Plans[0]
			if len(plan.Messages) != 1 || plan.Messages[0] != answer || len(plan.Sent) != 0 || plan.Input.Thread != msg.ThreadID || plan.Input.Sender != msg.SenderID || plan.Trace != msg.ID || plan.Quote == nil || plan.Quote.MessageID != msg.ID || plan.Quote.ServerID != msg.ServerID {
				t.Fatal("plan lost its original scope/quote, or attempted delivery")
			}
		})
	}
}
