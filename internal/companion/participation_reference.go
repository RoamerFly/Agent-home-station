package companion

import (
	"context"
	"regexp"
	"strings"
)

// This is a local output contract, not a relationship/event extraction store.
// Only the current literal or an explicit quote chain can establish its focus.
type ParticipationReferenceEvidence struct {
	Active                    bool                      `json:"active"`
	Focus                     string                    `json:"focus,omitempty"`
	Source                    ParticipationFocusSource  `json:"source"`
	SubjectReferences         []string                  `json:"subject_references"`
	EstablishedParticipantIDs []string                  `json:"established_participant_ids"`
	ReporterID                string                    `json:"reporter_id"`
	TargetRoles               []ParticipationTargetRole `json:"target_roles"`
	Interpretation            string                    `json:"interpretation"`
}

type ParticipationFocusSource struct {
	EventID   string `json:"event_id,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	Scope     string `json:"scope,omitempty"`
	Revision  int    `json:"revision,omitempty"`
	AuthorID  string `json:"literal_author_id,omitempty"`
}

type ParticipationTargetRole struct {
	TargetID      string `json:"target_id"`
	Participation string `json:"participation"`
}

type participationFocus struct {
	meta      ParticipationFocusSource
	words     string
	fromQuote bool
}

var communicationPredicate = regexp.MustCompile(`视频通话|通电话|打(?:了|过|完)?电话|通话|聊天|聊(?:得|了|过|完|着|到|什么)|说话|视频`)
var thirdPartyKinship = regexp.MustCompile(`(?:你们|您们|你|您|我)(?:的)?(?:爸爸|妈妈|父亲|母亲|爸|妈|爷爷|奶奶|外公|外婆|哥哥|姐姐|弟弟|妹妹|丈夫|妻子|儿子|女儿)`)
var firstPartyCommunication = regexp.MustCompile(`^(?:我|我们)(?:刚刚|刚才|刚|已经|正在|在|才|又|也|正|就)*(?:(?:和|跟|与|同)[^，。！？;；:：说问]{1,28})?(?:视频(?:通话)?|通话|通电话|打(?:了|过|完)?电话|聊(?:天|了|过|完|着|得)|说话)`)
var firstPartyCommunicationMention = regexp.MustCompile(`^(?:如果|假如|要是)?(?:我|我们)(?:刚刚|刚才|刚|已经|正在|在|才|又|也|正|就|没|没有|不|未|是否|是不是)*(?:(?:和|跟|与|同)[^，。！？;；:：说问]{1,28})?(?:视频(?:通话)?|通话|通电话|打(?:了|过|完)?电话|聊天|聊|说话)`)
var pluralCommunication = regexp.MustCompile(`(?:你们|您们|你俩|您俩|你两位|您两位|你二位|您二位)([^，。！？:：;；\n]{0,40})(?:视频(?:通话)?|通(?:了|过|完)?话|通电话|打(?:了|过|完)?电话|聊(?:天|得|了|过|完|着|到|什么|好)?|说话)`)
var participationContentQuestion = regexp.MustCompile(`怎么样|怎样|如何|什么|多久|多长|什么时候|聊得|聊到哪`)
var communicationAspect = regexp.MustCompile(`刚|已经|正在|结束|挂掉|挂断|聊完|打完|通着|视频中|通话中`)
var communicationVideoContext = regexp.MustCompile(`视频通话|通话|电话|聊天|聊得|聊了|聊过|聊完|挂|视频结束|视频完|视频中`)
var thirdPartyPronounSubject = regexp.MustCompile(`^(?:他|她|他们|她们)(?:刚|已经|正在|在|和|跟|与|通话|视频|打电话)`)
var namedCommunicationTail = regexp.MustCompile(`^(?:刚刚|刚才|刚|已经|正在|在|才|又|也|正)*(?:视频|通话|通电话|打电话|和|跟|与|聊)`)
var completedCommunicationPredicate = regexp.MustCompile(`聊(?:了|过|完|着)|打(?:了|过|完)电话`)
var namedCommunicationMentionTail = regexp.MustCompile(`^(?:刚刚|刚才|刚|已经|正在|在|才|又|也|正|没|没有|不|未)*(?:视频|通话|通电话|打电话|和|跟|与|聊)`)
var namedKinshipTail = regexp.MustCompile(`^(?:的)?(?:爸爸|妈妈|父亲|母亲|爸|妈|爷爷|奶奶|外公|外婆|哥哥|姐姐|弟弟|妹妹|丈夫|妻子|儿子|女儿)`)
var communicationOutcomeNegation = regexp.MustCompile(`(?:没|没有|未)(?:聊|谈|沟通)(?:好|得|拢|成|顺|完)|(?:没|没有|未)打完电话`)
var communicationAbsence = regexp.MustCompile(`没有|从未|没|并未|尚未|未曾|不曾|未`)

func clearCommunicationMention(text string) bool {
	if !communicationPredicate.MatchString(text) {
		return false
	}
	if strings.Contains(text, "视频") && !communicationVideoContext.MatchString(text) {
		return false
	}
	return communicationAspect.MatchString(text) || strings.Contains(text, "通话") || strings.Contains(text, "电话") || strings.Contains(text, "聊")
}

func (e *Engine) participationFocusSources(ctx context.Context, in Input) (participationFocus, []participationFocus) {
	current := participationFocus{}
	if e.Store != nil {
		if source, ok := e.Store.SourceForInput(in); ok {
			if ev, err := e.Store.ReadEvent(ctx, []string{ScopeID(in.Channel, in.Account, in.Thread)}, source.EventID); err == nil {
				words, state := literalTurn(eventEvidenceTurn(ev))
				if reliableLiteral(words, state) {
					current = participationFocus{meta: ParticipationFocusSource{ev.ID, ev.Trace, ev.Scope, ev.Revision, ev.Sender}, words: words}
				}
			}
		}
	}
	if current.meta.MessageID == "" && ephemeralReplyContext(in, e.Config) {
		words, state := e.inputLiteral(in)
		if reliableLiteral(words, state) {
			current = participationFocus{meta: ParticipationFocusSource{MessageID: in.Source.MessageID, Scope: ScopeID(in.Channel, in.Account, in.Thread), Revision: in.Source.Revision, AuthorID: in.Sender}, words: words}
		}
	}
	quotes := []participationFocus{}
	seen := map[string]bool{}
	ref := in.QuotedSource
	for ref != nil {
		ev, ok := e.replyReferenceEvent(ctx, in, ref)
		if !ok || seen[ev.ID] {
			break
		}
		seen[ev.ID] = true
		words, state := literalTurn(eventEvidenceTurn(ev))
		if reliableLiteral(words, state) {
			quotes = append(quotes, participationFocus{meta: ParticipationFocusSource{ev.ID, ev.Trace, ev.Scope, ev.Revision, ev.Sender}, words: words, fromQuote: true})
		}
		ref = ev.QuotedSource
	}
	return current, quotes
}

func (e *Engine) participationKnownLabels(in Input) map[string]string {
	owners := map[string]map[string]bool{}
	add := func(id, label string) {
		if id == "" || label == "" {
			return
		}
		if owners[label] == nil {
			owners[label] = map[string]bool{}
		}
		owners[label][id] = true
	}
	add(in.Sender, in.Sender)
	add(in.Sender, in.SenderName)
	for _, turn := range e.groundingScope(in).Messages {
		if turn.Role == "user" {
			add(turn.Sender, turn.Sender)
			add(turn.Sender, turn.SenderName)
		}
	}
	if in.QuotedSource != nil {
		add(in.QuotedSource.Sender, in.QuotedSource.Sender)
		add(in.QuotedSource.Sender, in.QuotedSource.SenderName)
	}
	labels := map[string]string{}
	for label, ids := range owners {
		if len(ids) == 1 {
			for id := range ids {
				labels[label] = id
			}
		}
	}
	return labels
}

func participationClauseFacts(source participationFocus, in Input, labels map[string]string) (bool, []string, []string) {
	thirdOverall := false
	focalParticipants, focalSubjects, otherParticipants := []string{}, []string{}, []string{}
	for _, clause := range sourceTextClauses(source.words) {
		if !clearCommunicationMention(clause.Text+" "+source.words) || !communicationPredicate.MatchString(clause.Text) {
			continue
		}
		third := false
		participants, subjects := []string{}, []string{}
		text := strings.TrimSpace(clause.Text)
		for _, subject := range thirdPartyKinship.FindAllString(text, -1) {
			subjects = append(subjects, subject)
			third = true
		}
		if thirdPartyPronounSubject.MatchString(text) {
			third = true
			subjects = append(subjects, "unresolved_third_person")
		}
		// The literal's I is its author, and only an affirmative direct event
		// statement supports participation. Possessives such as my father do not.
		self := firstPartyCommunication.MatchString(text)
		if firstPartyCommunicationMention.MatchString(text) && source.meta.AuthorID != in.Sender {
			third = true
		}
		if self {
			if source.meta.AuthorID != in.Sender {
				third = true
			}
			if !clause.Conditional && !clause.Negated && !clause.Question {
				participants = append(participants, source.meta.AuthorID)
			}
		}
		for label, id := range labels {
			if id == "" || label == "" {
				continue
			}
			for offset := 0; offset < len(text); {
				at := strings.Index(text[offset:], label)
				if at < 0 {
					break
				}
				at += offset
				tail := strings.TrimSpace(text[at+len(label):])
				offset = at + len(label)
				if namedKinshipTail.MatchString(tail) {
					third = true
					subjects = append(subjects, label+namedKinshipTail.FindString(tail))
					continue
				}
				if !namedCommunicationMentionTail.MatchString(tail) {
					continue
				}
				if id != in.Sender {
					third = true
					subjects = append(subjects, label)
				}
				if namedCommunicationTail.MatchString(tail) && !clause.Conditional && !clause.Negated && !clause.Question {
					participants = append(participants, id)
				}
			}
		}
		otherParticipants = append(otherParticipants, participants...)
		if third {
			if !thirdOverall {
				focalParticipants = uniqueSortedEvidence(participants)
			} else {
				// Distinct clauses may describe different calls. Do not let a
				// self-call elsewhere in the message supply the third-party
				// event's participants; retain only support common to each focus.
				common := []string{}
				for _, id := range focalParticipants {
					for _, candidate := range participants {
						if id == candidate {
							common = append(common, id)
							break
						}
					}
				}
				focalParticipants = common
			}
			thirdOverall = true
			focalSubjects = append(focalSubjects, subjects...)
		}
	}
	if thirdOverall {
		return true, uniqueSortedEvidence(focalParticipants), uniqueSortedEvidence(focalSubjects)
	}
	return false, uniqueSortedEvidence(otherParticipants), []string{}
}

func (e *Engine) participationReferenceEvidence(ctx context.Context, in Input, r ReplyContext) ParticipationReferenceEvidence {
	out := ParticipationReferenceEvidence{EstablishedParticipantIDs: []string{}, SubjectReferences: []string{}, TargetRoles: []ParticipationTargetRole{}, ReporterID: in.Sender, Interpretation: "Only the focal current literal/explicit quotation establishes event participation. Reporting, quoting, family ownership and an unrelated older self-call do not establish joining this event. Unestablished does not mean the person did not participate. Use an explicit third person or ask about participation rather than presupposing it; do not derive feelings, surroundings or experiences from the reporter role."}
	if !in.IsGroup || in.SourceOnlyContext || in.ConversationDelivery {
		return out
	}
	current, quotes := e.participationFocusSources(ctx, in)
	labels := e.participationKnownLabels(in)
	selected := current
	third, participants, subjects := participationClauseFacts(current, in, labels)
	if !third {
		for _, quote := range quotes {
			qt, qp, qs := participationClauseFacts(quote, in, labels)
			if qt {
				selected, third, participants, subjects = quote, qt, qp, qs
				break
			}
		}
	}
	out.Active, out.Source, out.EstablishedParticipantIDs, out.SubjectReferences = third, selected.meta, participants, subjects
	if third {
		out.Focus = "current_third_party_communication"
		if selected.fromQuote {
			out.Focus = "explicit_quoted_communication"
		}
	}
	for _, target := range r.TargetIDs {
		state := "not_established_for_this_focal_event"
		for _, id := range participants {
			if id == target {
				state = "explicit_participation_in_focal_literal"
			}
		}
		out.TargetRoles = append(out.TargetRoles, ParticipationTargetRole{target, state})
	}
	return out
}

func pluralParticipationClaim(clause SourceTextClause) bool {
	if clause.Conditional {
		return false
	}
	for _, match := range pluralCommunication.FindAllStringSubmatch(clause.Text, -1) {
		between := strings.TrimSpace(match[1])
		rolePrefix := between
		for _, prefix := range []string{"不是", "没有", "没", "刚刚", "刚才", "刚", "已经", "正在", "在"} {
			rolePrefix = strings.TrimPrefix(rolePrefix, prefix)
		}
		outer := false
		for _, prefix := range []string{"说的是", "说的那", "提到", "指的是", "引用", "转述", "听说", "知道", "问的是", "是在说", "了解到"} {
			outer = outer || strings.HasPrefix(rolePrefix, prefix)
		}
		outer = outer || strings.HasPrefix(rolePrefix, "说") && !strings.HasPrefix(rolePrefix, "说话")
		if outer {
			continue
		}
		future := false
		for _, prefix := range []string{"下次", "以后", "改天", "有空", "准备", "打算", "计划", "可以", "要不要", "不如", "想不想", "将会", "等会"} {
			future = future || strings.HasPrefix(between, prefix)
		}
		if future {
			continue
		}
		rhetorical := strings.Contains(between, "不是") && strings.HasSuffix(strings.TrimSpace(clause.Text), "吗")
		outcomeNegated := communicationOutcomeNegation.MatchString(clause.Text)
		if communicationAbsence.MatchString(between) && !rhetorical && !outcomeNegated {
			continue
		}
		if strings.Contains(clause.Text, "聊聊") && !communicationAspect.MatchString(between) {
			continue
		}
		contentQuestion := participationContentQuestion.MatchString(clause.Text)
		if clause.Question && !contentQuestion && !rhetorical && !outcomeNegated {
			continue
		} // A real clarification asks whether they participated.
		if contentQuestion || rhetorical || outcomeNegated || communicationAspect.MatchString(between) || completedCommunicationPredicate.MatchString(match[0]) {
			return true
		}
	}
	return false
}

func (e *Engine) validateParticipationReference(ctx context.Context, in Input, r ReplyContext, text string) []SourceContractViolation {
	issues := []SourceContractViolation{}
	evidence := e.participationReferenceEvidence(ctx, in, r)
	if !evidence.Active {
		return issues
	}
	missing := false
	for _, role := range evidence.TargetRoles {
		missing = missing || role.Participation != "explicit_participation_in_focal_literal"
	}
	if !missing {
		return issues
	}
	refs := []string{}
	if evidence.Source.EventID != "" {
		refs = append(refs, evidence.Source.EventID)
	}
	for _, clause := range sourceTextClauses(text) {
		if pluralParticipationClaim(clause) {
			issues = append(issues, SourceContractViolation{Code: "reply_participation_reference_required", Span: clause.Text, Kind: "wrong_person", SourceRefs: refs, Reason: "This outgoing plural includes its target in the focal third-party communication, but this exact current/quoted source does not establish that target's participation. Use explicit third-person reference or ask whether they joined. Reporting/quoting, a relative's event, or an unrelated earlier call cannot supply participation or personal experience; this is not proof of nonparticipation."})
		}
	}
	return issues
}
