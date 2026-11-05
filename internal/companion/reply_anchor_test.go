package companion

import (
	"encoding/json"
	"testing"
	"time"
)

func TestReplyAnchorPreservesDirectedSourceAcrossLaterTargetInterruption(t *testing.T) {
	s, c, _ := memoryFixture(t)
	groundedInput(t, s, c, "beta", "earlier-call", "我刚与父亲视频。")
	in := groundedInput(t, s, c, "alpha", "directed-report", "你爸刚视频，刚挂掉。")
	in.Addressees = []string{"beta"}
	e := &Engine{Store: s, Config: c}
	d, err := e.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{"那通视频刚结束。"}, ReplyContexts: []ReplyContext{{TargetIDs: []string{"beta"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if d.ReplyContexts[0].AnchorMessageID != in.TraceID || !d.ReplyContexts[0].QuoteRequired {
		t.Fatal("did not quote explicit directed source", d)
	}
	before, _ := json.Marshal(d)
	groundedInput(t, s, c, "beta", "irrelevant-later", "我去拿快递了。")
	next, err := e.normalizeReplyContexts(t.Context(), in, d)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(next)
	if string(before) != string(after) {
		t.Fatal("normalization redirected or re-signed existing source", string(before), string(after))
	}
}

func TestReplyAnchorPreservesOldQuotedPhotoAcrossLaterTargetInterruption(t *testing.T) {
	s, c, _ := memoryFixture(t)
	old := Input{Channel: "wechat", Account: "synthetic", Thread: "room@chatroom", Sender: "beta", TraceID: "old-photo", Text: "黄色自行车。", IsGroup: true, Mode: "direct"}
	old.Source = &SourceRef{InstanceID: "review-sequence", MessageID: old.TraceID, Account: old.Account, Thread: old.Thread, Sender: old.Sender, MessageType: 3, Integrity: "verified"}
	old.IncomingMedia = []IncomingMediaRef{{Source: SourceRef{MessageID: old.TraceID, Sender: old.Sender}, AssetID: "old-photo-asset", Kind: "image", UploaderID: old.Sender, Observation: "黄色自行车", ObservationState: "verified"}}
	if err := s.Observe(old, c, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	oldSource, ok := s.SourceForInput(old)
	if !ok {
		t.Fatal("missing photo source")
	}
	in := groundedInput(t, s, c, "alpha", "quote-question", "这张旧图里是什么？")
	in.QuotedSource = oldSource
	e := &Engine{Store: s, Config: c}
	d, err := e.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{"你之前发的图里显示黄色自行车。"}, ReplyContexts: []ReplyContext{{TargetIDs: []string{"beta"}, AnchorEventID: oldSource.EventID, AnchorMessageID: old.TraceID, SourceEventIDs: []string{oldSource.EventID}, SourceVersions: map[string]int{oldSource.EventID: oldSource.Revision}, MediaSourceIDs: []string{"old-photo-asset"}}}})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(d)
	groundedInput(t, s, c, "beta", "unrelated-new-photo-comment", "明天想吃什么？")
	next, err := e.normalizeReplyContexts(t.Context(), in, d)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(next)
	if string(before) != string(after) || next.ReplyContexts[0].AnchorMessageID != old.TraceID {
		t.Fatal("old photo anchor changed to unrelated new speech", string(after))
	}
}

func TestReplyAnchorNeverUsesLatestParticipantMessageWithoutLinkedSource(t *testing.T) {
	s, c, _ := memoryFixture(t)
	groundedInput(t, s, c, "beta", "unrelated-beta", "我去拿快递。")
	in := groundedInput(t, s, c, "alpha", "current-question", "刚才那事呢？")
	in.QuotedSource = &SourceRef{MessageID: "missing-explicit-quote", Sender: "beta"}
	e := &Engine{Store: s, Config: c}
	if _, err := e.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{"有后续吗？"}, ReplyContexts: []ReplyContext{{TargetIDs: []string{"beta"}}}}); err == nil {
		t.Fatal("latest unrelated beta message became an invented anchor")
	}
	if _, err := e.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{"回复当前人。"}, ReplyContexts: []ReplyContext{{TargetIDs: []string{in.Sender}, AnchorMessageID: "invented-anchor"}}}); err == nil {
		t.Fatal("invented explicit anchor silently replaced by current message")
	}
}

func TestReplyAnchorRejectsChangedExistingProofInsteadOfSelectingNewSource(t *testing.T) {
	s, c, _ := memoryFixture(t)
	in := groundedInput(t, s, c, "alpha", "source-before-edit", "你爸刚视频。")
	in.Addressees = []string{"beta"}
	e := &Engine{Store: s, Config: c}
	d, err := e.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{"刚结束了。"}, ReplyContexts: []ReplyContext{{TargetIDs: []string{"beta"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE events SET revision=revision+1 WHERE id=?", d.ReplyContexts[0].AnchorEventID); err != nil {
		t.Fatal(err)
	}
	groundedInput(t, s, c, "beta", "new-unrelated", "今天还有别的安排。")
	if _, err = e.normalizeReplyContexts(t.Context(), in, d); err == nil {
		t.Fatal("stale anchor changed instead of stopping")
	}
}

func TestSourceFreeSystemAnchorIsNotAUserMessageOrArbitraryQuote(t *testing.T) {
	s, c, _ := memoryFixture(t)
	e := &Engine{Store: s, Config: c}
	in := Input{Channel: "wechat", Account: "synthetic", Thread: "room@chatroom", IsGroup: true, Mode: "proactive", TraceID: "proactive-synthetic-run"}
	d, err := e.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{"有个新话题可以聊聊。"}})
	if err != nil || d.ReplyContexts[0].State != "system_context" || len(d.ReplyContexts[0].SourceEventIDs) > 0 {
		t.Fatal("authorized proactive run required an invented user message", d, err)
	}
	for _, which := range []string{"quote", "person", "event", "trace"} {
		bad := d.ReplyContexts[0]
		switch which {
		case "quote":
			bad.QuoteRequired = true
		case "person":
			bad.TargetIDs = []string{"someone"}
		case "event":
			bad.AnchorEventID = "invented-user-source"
		case "trace":
			bad.AnchorMessageID = "unrelated-trace"
		}
		bad.ContextVersion = ReplyContextFingerprint(in, bad)
		if e.ValidateReplyContext(t.Context(), in, bad) == nil {
			t.Fatal("system run became an arbitrary user quote", which)
		}
	}
}
