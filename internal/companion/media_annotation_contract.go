package companion

import (
	"context"
	"regexp"
	"strings"
)

// MediaAnnotationEvidence describes only the authorized focal event. Unknown,
// missing or previewed originals are never promoted to verified absence.
type MediaAnnotationEvidence struct {
	Focus            string `json:"focus"`
	EventID          string `json:"event_id,omitempty"`
	MessageID        string `json:"message_id,omitempty"`
	Scope            string `json:"scope,omitempty"`
	Revision         int    `json:"revision,omitempty"`
	CaptionState     string `json:"caption_state"`
	NativeCaption    string `json:"native_caption,omitempty"`
	CaptionBytes     int    `json:"caption_bytes,omitempty"`
	CaptionExcerpted bool   `json:"caption_excerpted,omitempty"`
	Availability     string `json:"availability"`
}

func mediaEvent(ev MemoryEvent) bool {
	return len(ev.IncomingMedia) > 0 || mediaSourceType(ev.Source)
}

func (e *Engine) mediaAnnotationEvidence(ctx context.Context, in Input) []MediaAnnotationEvidence {
	out := []MediaAnnotationEvidence{}
	seen := map[string]bool{}
	for _, f := range []struct {
		kind     string
		ref      *SourceRef
		declared bool
	}{
		{"current_media", in.Source, inputHasMedia(in)},
		{"quoted_media", in.QuotedSource, in.QuotedSource != nil && mediaSourceType(in.QuotedSource)},
	} {
		if f.ref == nil {
			continue
		}
		ev, ok := e.replyReferenceEvent(ctx, in, f.ref)
		if !ok {
			if f.declared {
				out = append(out, MediaAnnotationEvidence{Focus: f.kind, EventID: f.ref.EventID, MessageID: f.ref.MessageID, CaptionState: "unknown", Availability: "authorized_full_source_unavailable"})
			}
			continue
		}
		if !mediaEvent(ev) || seen[ev.ID] {
			continue
		}
		seen[ev.ID] = true
		item := MediaAnnotationEvidence{Focus: f.kind, EventID: ev.ID, MessageID: ev.Trace, Scope: ev.Scope, Revision: ev.Revision, CaptionState: "unknown", Availability: "authorized_full_source"}
		if ev.Complete && ev.UserTextState == "verified_original" {
			item.CaptionState = "present"
			item.NativeCaption = boundedText(ev.Text, 1200)
			item.CaptionBytes = len(ev.Text)
			item.CaptionExcerpted = item.NativeCaption != ev.Text
			if ev.Text == "" {
				item.CaptionState = "verified_empty"
			}
		}
		out = append(out, item)
	}
	return out
}

var mediaAnnotationUpload = regexp.MustCompile(`(?:上传|发送|发来|发出|转发)(?:时|的时候|当时)|(?:when (?:uploaded|sent)|on upload|at upload)`)
var mediaAnnotationStated = regexp.MustCompile(`注明|标注|注释|附言|附注|备注|写明|写着|写了|文字说明|附带文字|配文|caption|annotation|noted|stated`)
var mediaNativeCaptionSlot = regexp.MustCompile(`(?:原始文字|原文附言|随附文字|附言|配文|备注)(?:中|里|上|就|也|已经|明确|有|是|写|提到|注明|标注|说明)|(?:caption|upload note)(?: says| stated| notes| reads)`)
var mediaAnnotationGuidance = regexp.MustCompile(`请|建议|应当|应该|需要|务必|记得|最好|不要|不必|无需|避免|可以(?:让|在|加|写|注明|标注)|(?:should|please|must|could) `)
var mediaAnnotationDefinite = regexp.MustCompile(`(?:注明|标注|写明|写|备注)(?:过|了)?的(?:那|这|该|一|几|哪)?(?:句|段|行|条|些|个)?(?:话|文字|内容|来源|说明|附言)|(?:附言|配文|备注)(?:中|里|上)?(?:写了|注明了|写明了|说了)(?:什么|啥|哪)`)
var mediaAnnotationRhetorical = regexp.MustCompile(`(?:不是|不就|岂不是|难道)[^，,；;。！？!?]{0,28}(?:注明|标注|写明|附言|配文|备注)[^，,；;。！？!?]{0,12}(?:吗|嘛|么)$`)
var mediaAnnotationNegativePredicate = regexp.MustCompile(`(?:没有|没|并未|未曾|不曾|尚未|未)(?:(?:在|于)?(?:上传|发送|转发)(?:时|的时候))?(?:(?:有|曾|明确|文字|任何|写过|写|添加|附带)){0,3}(?:注明|标注|写明|附言|配文|备注)`)
var mediaAnnotationAbsence = regexp.MustCompile(`(?:不是|并非)(?:(?:在|于)?(?:上传|发送|转发)(?:时|的时候))?(?:注明|标注|写明|附言|配文|备注)`)
var mediaAnnotationDoubleNegative = regexp.MustCompile(`不是(?:没有|没|未)(?:注明|标注|写明|附言|配文|备注)`)
var mediaAnnotationUncertain = regexp.MustCompile(`没有证据|无证据|无法确认|不能确认|尚不能确定|不清楚|不知道|未核实|尚待核实`)
var mediaAnnotationAlternate = regexp.MustCompile(`(?:另外|另一|别的)[^，,；;。！？!?]{0,4}(?:语音|录音|音频|图片|图像|照片|视频|素材|文件|附件)`)

func nativeAnnotationClaim(clause SourceTextClause) bool {
	if clause.Conditional {
		return false
	}
	t := strings.ToLower(clause.Text)
	if mediaAnnotationGuidance.MatchString(t) || mediaAnnotationUncertain.MatchString(t) {
		return false // guidance about a possible caption is not an actual one
	}
	if !(mediaAnnotationUpload.MatchString(t) && mediaAnnotationStated.MatchString(t) || mediaNativeCaptionSlot.MatchString(t)) {
		return false
	}
	// Apply polarity to the native annotation predicate, not to every negative
	// word or question mark in the clause. Questions about an annotation's
	// content, and rhetorical "didn't you note it?", presuppose its existence.
	if mediaAnnotationDoubleNegative.MatchString(t) {
		return true
	}
	if mediaAnnotationNegativePredicate.MatchString(t) {
		return false
	}
	rhetorical := clause.Question && mediaAnnotationRhetorical.MatchString(t)
	if mediaAnnotationAbsence.MatchString(t) && !rhetorical {
		return false
	}
	if mediaAnnotationDefinite.MatchString(t) || rhetorical {
		return true
	}
	return !clause.Question
}

func bindAnnotationFocus(items []MediaAnnotationEvidence, text string) (MediaAnnotationEvidence, bool) {
	if mediaAnnotationAlternate.MatchString(text) {
		return MediaAnnotationEvidence{}, false
	}
	if strings.Contains(text, "引用") {
		var found MediaAnnotationEvidence
		n := 0
		for _, item := range items {
			if item.Focus == "quoted_media" {
				found = item
				n++
			}
		}
		return found, n == 1
	}
	if len(items) == 1 {
		return items[0], true
	}
	return MediaAnnotationEvidence{}, false
}

func (e *Engine) validateMediaAnnotation(ctx context.Context, in Input, r ReplyContext, text string) []SourceContractViolation {
	violations := []SourceContractViolation{}
	items := e.mediaAnnotationEvidence(ctx, in)
	for _, clause := range sourceTextClauses(text) {
		if !nativeAnnotationClaim(clause) {
			continue
		}
		item, ok := bindAnnotationFocus(items, clause.Text)
		refs := []string{}
		if item.EventID != "" && item.Scope != "" && item.Availability == "authorized_full_source" {
			refs = append(refs, item.EventID)
		}
		if ok {
			if revision, exists := r.SourceVersions[item.EventID]; exists && revision != item.Revision {
				ok = false
			}
		}
		if !ok || item.CaptionState == "unknown" {
			violations = append(violations, SourceContractViolation{Code: "reply_media_annotation_source_required", Span: clause.Text, Kind: "source_status", SourceRefs: refs, Reason: "The claim locates a note in a native media-upload caption, but a unique authorized full source and current revision do not establish that slot. Identify the source or qualify the claim; unknown is not proof of an empty caption."})
			continue
		}
		if item.CaptionState == "verified_empty" {
			violations = append(violations, SourceContractViolation{Code: "reply_media_native_annotation_contradiction", Span: clause.Text, Kind: "media_attribution", SourceRefs: refs, Reason: "This exact media event has a complete verified empty native user body. ASR/OCR/vision observation cannot fill its native-caption slot. Attribute the information to the transcription/observation rather than saying it was written or noted at upload."})
		}
	}
	return violations
}
