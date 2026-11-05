package companion

import (
	"testing"
	"time"
)

func mediaAnnotationFixture(t *testing.T, caption *string) (*Engine, Input, ReplyContext) {
	t.Helper()
	s, c, in := memoryFixture(t)
	in.TraceID = "synthetic-recording"
	in.Text = "[语音转写] 这是我转发的别人的录音，明天去杭州。"
	in.OriginalText = caption
	in.Source = &SourceRef{InstanceID: s.SourceInstanceID(), MessageID: in.TraceID, MessageType: 34}
	in.IncomingMedia = []IncomingMediaRef{{Source: *in.Source, AssetID: "safe-audio", Kind: "voice", UploaderID: in.Sender, Observation: in.Text, ObservationState: "verified", ObservationVersion: 1}}
	if err := s.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	ref, ok := s.SourceForInput(in)
	if !ok {
		t.Fatal("source absent")
	}
	query := Input{Channel: in.Channel, Account: in.Account, Thread: in.Thread, Sender: "bob", TraceID: "question", Text: "这一定是上传者本人的声音吗？", QuotedSource: ref}
	query.OriginalText = &query.Text
	return &Engine{Store: s, Config: c}, query, ReplyContext{SourceVersions: map[string]int{ref.EventID: ref.Revision}, SourceEventIDs: []string{ref.EventID}}
}

func TestMediaAnnotationContractRejectsASRPromotedToVerifiedEmptyNativeCaption(t *testing.T) {
	empty := ""
	e, in, r := mediaAnnotationFixture(t, &empty)
	info := e.mediaAnnotationEvidence(t.Context(), in)
	if len(info) != 1 || info[0].CaptionState != "verified_empty" || info[0].Revision < 1 || info[0].Scope == "" {
		t.Fatal(info)
	}
	for _, text := range []string{"这条语音上传时就注明了是别人录音。", "引用的语音发送时有文字说明。", "这段录音的附言写着是转发。"} {
		v := e.validateMediaAnnotation(t.Context(), in, r, text)
		if len(v) != 1 || v[0].Code != "reply_media_native_annotation_contradiction" || len(v[0].SourceRefs) != 1 || v[0].SourceRefs[0] != info[0].EventID {
			t.Fatal(text, v)
		}
	}
	for _, text := range []string{"转写中提到这是转发的录音，人声身份尚不能确认。", "上传消息的发送者是alice。", "你说这是转发的。", "没有证据说明上传时注明了来源。", "如果上传时注明了，就可以引用这段附言。", "上传时注明了吗？", "他说‘语音上传时就注明了来源’，这仍需核对。", "> 语音上传时就注明了来源\n不能仅凭这句引语确定。", "`语音上传时就注明了`只是代码示例。", "请在语音上传时注明来源。", "可以让发送者上传时注明来源。", "不要把转写说成上传时注明了。", "附言里应该写清来源。"} {
		if v := e.validateMediaAnnotation(t.Context(), in, r, text); len(v) != 0 {
			t.Fatal("non-caption assertion was overblocked", text, v)
		}
	}
}

func TestMediaAnnotationContractDistinguishesActualCaptionAndUnknownLegacy(t *testing.T) {
	caption := "这是我转发的别人的录音"
	e, in, r := mediaAnnotationFixture(t, &caption)
	if v := e.validateMediaAnnotation(t.Context(), in, r, "语音上传时就注明了这是转发。"); len(v) != 0 {
		t.Fatal("real caption treated as verified empty", v)
	}
	e, in, r = mediaAnnotationFixture(t, nil)
	v := e.validateMediaAnnotation(t.Context(), in, r, "语音上传时就注明了这是转发。")
	if len(v) != 1 || v[0].Code != "reply_media_annotation_source_required" {
		t.Fatal("unknown original was treated as definitely empty", v)
	}
	info := e.mediaAnnotationEvidence(t.Context(), in)
	if len(info) != 1 || info[0].CaptionState != "unknown" {
		t.Fatal(info)
	}
}

func TestMediaAnnotationContractOtherMediaCaptionCannotSupportFocalUpload(t *testing.T) {
	empty := ""
	e, in, r := mediaAnnotationFixture(t, &empty)
	other := Input{Channel: in.Channel, Account: in.Account, Thread: in.Thread, Sender: "alice", TraceID: "other-recording", Text: "确有文字附言", OriginalText: new(string), Source: &SourceRef{InstanceID: e.Store.SourceInstanceID(), MessageID: "other-recording", MessageType: 34}}
	*other.OriginalText = "确有文字附言"
	other.IncomingMedia = []IncomingMediaRef{{AssetID: "another-audio", Kind: "voice", UploaderID: "alice"}}
	if err := e.Store.Observe(other, e.Config, time.Now()); err != nil {
		t.Fatal(err)
	}
	if v := e.validateMediaAnnotation(t.Context(), in, r, "引用的这条语音上传时就注明了来源。"); len(v) != 1 || v[0].Code != "reply_media_native_annotation_contradiction" {
		t.Fatal("another caption filled focal empty slot", v)
	}
	if v := e.validateMediaAnnotation(t.Context(), in, r, "另外一条语音上传时就注明了来源。"); len(v) != 1 || v[0].Code != "reply_media_annotation_source_required" {
		t.Fatal("unbound alternate claim falsely assigned to focal source", v)
	}
}

func TestMediaAnnotationContractRequiresCurrentSourceRevisionAndPermission(t *testing.T) {
	empty := ""
	e, in, r := mediaAnnotationFixture(t, &empty)
	r.SourceVersions[in.QuotedSource.EventID]++
	if v := e.validateMediaAnnotation(t.Context(), in, r, "语音上传时就注明了来源。"); len(v) != 1 || v[0].Code != "reply_media_annotation_source_required" {
		t.Fatal("stale revision became a definite caption contradiction", v)
	}
	in.Account = "other-account"
	if v := e.validateMediaAnnotation(t.Context(), in, r, "语音上传时就注明了来源。"); len(v) != 1 || v[0].Code != "reply_media_annotation_source_required" {
		t.Fatal("unauthorized source disclosed as verified empty", v)
	}
}

func TestMediaAnnotationContractChecksDefiniteQuestionPresuppositionsLocally(t *testing.T) {
	empty := ""
	e, in, r := mediaAnnotationFixture(t, &empty)
	for _, text := range []string{
		"上传时注明的那句话是什么意思？",
		"你不是上传时注明了吗？",
		"这条语音上传时注明的那句话不是在说别的。",
		"这条语音的附言里写了什么？",
		"语音上传时不是没有注明来源。",
		"没有附言是另一回事，语音上传时就注明了来源。",
	} {
		v := e.validateMediaAnnotation(t.Context(), in, r, text)
		if len(v) != 1 || v[0].Code != "reply_media_native_annotation_contradiction" {
			t.Fatal("existence presupposition escaped its local caption contract", text, v)
		}
	}
	for _, text := range []string{
		"上传时有没有注明？",
		"上传时有无注明？",
		"上传时是否注明了来源？",
		"上传时注明了吗？",
		"上传时没有注明来源。",
		"语音上传时并未注明来源。",
		"没有在上传时注明来源。",
		"你不是上传时没有注明吗？",
		"语音不是上传时注明来源的。",
		"我不知道上传时注明的那句话是什么意思。",
		"如果你不是上传时注明了吗，那也需要核实。",
		"他说‘你不是上传时注明了吗？’，不能凭这句引用确认。",
	} {
		if v := e.validateMediaAnnotation(t.Context(), in, r, text); len(v) != 0 {
			t.Fatal("existence question/absence/quotation became a positive caption claim", text, v)
		}
	}
	caption := "实际的原生文字附言"
	e, in, r = mediaAnnotationFixture(t, &caption)
	if v := e.validateMediaAnnotation(t.Context(), in, r, "上传时注明的那句话是什么意思？"); len(v) != 0 {
		t.Fatal("present caption still treated as empty", v)
	}
	e, in, r = mediaAnnotationFixture(t, nil)
	if v := e.validateMediaAnnotation(t.Context(), in, r, "你不是上传时注明了吗？"); len(v) != 1 || v[0].Code != "reply_media_annotation_source_required" {
		t.Fatal("unknown caption became a definite absence", v)
	}
}
