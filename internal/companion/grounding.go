package companion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"wechat-bot/internal/deepseek"
)

func relevantSender(in Input, sender string) bool {
	if sender == in.Sender {
		return true
	}
	if in.QuotedSource != nil && in.QuotedSource.Sender == sender {
		return true
	}
	for _, id := range in.Addressees {
		if id == sender {
			return true
		}
	}
	return false
}
func turnEvidenceKind(t Turn) string {
	if t.DeviceObservation != nil {
		return "device_tool_observation"
	}
	if t.Role == "assistant" {
		return "assistant_statement"
	}
	text, state := literalTurn(t)
	if state == "redacted" {
		return "background_redacted"
	}
	if text == "" && len(t.IncomingMedia) > 0 {
		return "media_upload_event"
	}
	if state == "unknown" {
		return "unverified_user_event"
	}
	return "user_source"
}
func groundedHistory(scope Scope, in Input) []Turn {
	out := append([]Turn(nil), scope.Messages...)
	if in.SourceOnlyContext {
		kept := []Turn{}
		for _, t := range out {
			if in.Source != nil && (t.EventID == in.Source.EventID && t.EventID != "" || t.TraceID == in.Source.MessageID && t.TraceID != "") || in.QuotedSource != nil && (t.EventID == in.QuotedSource.EventID && t.EventID != "" || t.TraceID == in.QuotedSource.MessageID && t.TraceID != "") {
				kept = append(kept, t)
			}
		}
		return kept
	}
	if !in.IsGroup {
		return out
	}
	previous := ""
	for i := range out {
		t := &out[i]
		if t.Role == "user" {
			previous = t.Sender
		}
		unrelated := t.Role == "user" && groupHistoryRelation(in, *t) == "other_member_background"
		if t.Role == "assistant" {
			if t.ReplyContext != nil {
				unrelated = true
				for _, id := range t.ReplyContext.TargetIDs {
					if relevantSender(in, id) {
						unrelated = false
					}
				}
			} else if t.Media != nil && t.Media.RequesterID != "" {
				// A delayed attachment belongs to its recorded request even when
				// another group member spoke while it was being delivered.
				unrelated = !relevantSender(in, t.Media.RequesterID)
			} else {
				unrelated = previous != "" && !relevantSender(in, previous)
			}
		}
		if unrelated && (len(t.IncomingMedia) > 0 || t.Media != nil || strings.Contains(t.Text, "视觉观察") || strings.Contains(t.Text, "媒体的观察资料") || t.Role == "assistant") {
			t.Text = "[其他参与者的媒体或回复；不是当前发言者的经历，需明确引用后读取来源]"
			t.IncomingMedia = nil
			t.Media = nil
		}
	}
	return out
}

// Use one binding for history visibility, memory queries and the model's label.
// Older quotes with only a sender remain distinguishable from an exact quote.
func groupHistoryRelation(in Input, turn Turn) string {
	if turn.Role != "user" {
		return "assistant_or_tool_history"
	}
	if turn.Sender != "" && turn.Sender == in.Sender {
		return "current_sender_history"
	}
	if ref := in.QuotedSource; ref != nil && (ref.Sender == "" || ref.Sender == turn.Sender) {
		switch {
		case ref.EventID != "":
			if turn.EventID == ref.EventID || turn.Source != nil && turn.Source.EventID == ref.EventID {
				return "quoted_message_evidence"
			}
		case ref.MessageID != "":
			if sameTimelineMessage(turn, ref) {
				return "quoted_message_evidence"
			}
		case ref.Sender != "":
			return "legacy_quoted_sender_context"
		}
	}
	if turn.Sender != "" && slices.Contains(in.Addressees, turn.Sender) {
		return "addressed_member_evidence"
	}
	return "other_member_background"
}

func (e *Engine) groundingScope(in Input) Scope {
	if e.Store == nil {
		return Scope{}
	}
	scope := e.Store.Context(in, e.Config, time.Now())
	if in.SourceOnlyContext {
		scope.Messages = groundedHistory(scope, in)
		scope.Summary = ""
		scope.Notes = nil
	}
	return scope
}
func (e *Engine) ValidateReplyContext(ctx context.Context, in Input, r ReplyContext) error {
	// A protected console action chooses exact assets and destinations, without
	// a model decision based on recalled conversation text. Unrelated memory
	// updates must not cancel an upload. Authorization, route, asset integrity
	// and the original context fingerprint are still checked below/by the host.
	consoleAction := in.Mode == "admin_action" && in.SourceOnlyContext && in.Source == nil && in.QuotedSource == nil && len(r.SourceEventIDs) == 0 && r.State == "system_context"
	if r.EvidenceEpoch != "" && !consoleAction {
		if e.Store == nil {
			return errors.New("reply_evidence_epoch_unavailable")
		}
		epoch, err := e.Store.RecallEpoch(ctx)
		if err != nil || epoch != r.EvidenceEpoch {
			return errors.New("reply_evidence_epoch_changed")
		}
	}
	if r.EvidenceAuthorization != "" && !e.memoryResultAuthorized(r.EvidenceAuthorization) {
		return errors.New("reply_evidence_authorization_changed")
	}
	if in.Source != nil && !in.Preview {
		if e.Store == nil {
			return errors.New("reply_source_identity_unavailable")
		}
		if e.Config.Permissions.Remember {
			if _, ok := e.Store.SourceForInput(in); !ok {
				return errors.New("reply_source_identity_unavailable")
			}
		}
	}
	if !in.Preview {
		if len(r.SourceEventIDs) > 0 && r.State != "verified" {
			return errors.New("reply_context_not_verified")
		}
		if r.ContextVersion == "" || r.ContextVersion != ReplyContextFingerprint(in, r) {
			return errors.New("reply_context_fingerprint_mismatch")
		}
	}
	if len(r.SourceEventIDs) == 0 && !in.Preview {
		if systemReplyContext(in) {
			if r.State != "system_context" {
				return errors.New("reply_system_context_not_verified")
			}
		} else if ephemeralReplyContext(in, e.Config) {
			if r.State != "ephemeral_context" {
				return errors.New("reply_ephemeral_context_not_verified")
			}
		} else {
			return errors.New("reply_source_proof_missing")
		}
	}
	if len(r.TargetIDs) == 0 && in.Sender != "" {
		return errors.New("reply_target_missing")
	}
	scope := e.groundingScope(in)
	allowed := map[string]bool{in.Sender: true}
	if in.QuotedSource != nil {
		allowed[in.QuotedSource.Sender] = true
	}
	for _, id := range in.Addressees {
		allowed[id] = true
	}
	for _, id := range r.TargetIDs {
		if id == "" || !allowed[id] {
			return errors.New("reply_target_not_grounded")
		}
		if id != in.Sender && !r.QuoteRequired {
			return errors.New("reply_retarget_requires_quote")
		}
	}
	if r.AnchorMessageID != "" || r.AnchorEventID != "" {
		if _, err := e.chooseReplyAnchor(ctx, in, r, true); err != nil {
			return err
		}
	}
	if len(r.SourceEventIDs) > 0 && (e.Store == nil || e.Store.db == nil) {
		return errors.New("reply_source_unavailable")
	}
	for _, id := range r.SourceEventIDs {
		ev, err := e.Store.ReadEvent(ctx, []string{scope.ID}, id)
		if err != nil {
			return errors.New("reply_source_unavailable")
		}
		if ev.Role != "user" && ev.Role != "admin_correction" {
			return errors.New("assistant_statement_is_not_fact_source")
		}
		if !relevantSender(in, ev.Sender) {
			// Public group text can ground a claim ABOUT its actual author.
			// It never changes the audience or licenses another person's image.
			if len(ev.IncomingMedia) > 0 || ev.Media != nil || ev.Text == "" || ev.Source != nil && ev.Source.MessageType != 0 && ev.Source.MessageType != 1 {
				return errors.New("reply_source_personal_media_requires_link")
			}
		}
	}
	for _, id := range r.MediaSourceIDs {
		found := false
		for _, t := range scope.Messages {
			for _, m := range t.IncomingMedia {
				if (m.Source.EventID == id || m.Source.MessageID == id || m.AssetID == id) && relevantSender(in, m.UploaderID) {
					found = true
				}
			}
		}
		for _, m := range in.IncomingMedia {
			if (m.Source.EventID == id || m.Source.MessageID == id || m.AssetID == id) && relevantSender(in, m.UploaderID) {
				found = true
			}
		}
		if !found {
			return errors.New("reply_media_owner_or_source_mismatch")
		}
	}
	return nil
}
func (e *Engine) normalizeReplyContexts(ctx context.Context, in Input, d Decision) (Decision, error) {
	// Normalization may add versions/risks/anchors. Never mutate the caller's
	// persisted plan or the immutable candidate being independently checked.
	contexts := make([]ReplyContext, len(d.ReplyContexts))
	for i := range d.ReplyContexts {
		contexts[i] = *cloneReplyContext(&d.ReplyContexts[i])
	}
	d.ReplyContexts = contexts
	if len(d.ReplyContexts) > 0 && len(d.ReplyContexts) != len(d.Messages) {
		return Decision{}, errors.New("reply_context_count_mismatch")
	}
	if len(d.ReplyContexts) == 0 {
		d.ReplyContexts = make([]ReplyContext, len(d.Messages))
	}
	for i := range d.ReplyContexts {
		r := &d.ReplyContexts[i]
		if r.ContextVersion != "" || r.State != "" {
			if err := e.ValidateReplyContext(ctx, in, *r); err != nil {
				return Decision{}, err
			}
			continue
		}
		if r.SourceVersions == nil {
			r.SourceVersions = map[string]int{}
		}
		if len(r.SourceEventIDs) == 0 && e.Store != nil {
			if source, ok := e.Store.SourceForInput(in); ok && source.EventID != "" {
				r.SourceEventIDs = []string{source.EventID}
			}
		}
		if in.Source != nil && in.Source.EventID != "" && len(r.SourceEventIDs) == 0 {
			r.SourceEventIDs = []string{in.Source.EventID}
		}
		if len(r.SourceEventIDs) > 0 && (e.Store == nil || e.Store.db == nil) {
			return Decision{}, errors.New("reply_source_unavailable")
		}
		for _, id := range r.SourceEventIDs {
			ev, err := e.Store.ReadEvent(ctx, []string{ScopeID(in.Channel, in.Account, in.Thread)}, id)
			if err != nil {
				return Decision{}, err
			}
			if !relevantSender(in, ev.Sender) {
				r.RiskFlags = append(r.RiskFlags, "cross_person_source")
			}
			r.SourceVersions[id] = ev.Revision
		}
		if len(r.TargetIDs) == 0 && in.Sender != "" {
			r.TargetIDs = []string{in.Sender}
		}
		retargeted := false
		for _, target := range r.TargetIDs {
			retargeted = retargeted || target != in.Sender
		}
		if retargeted {
			r.QuoteRequired = true
		}
		if len(r.SourceEventIDs) > 0 {
			anchor, err := e.chooseReplyAnchor(ctx, in, *r, false)
			if err != nil {
				return Decision{}, err
			}
			r.AnchorMessageID = anchor.Trace
			r.AnchorEventID = anchor.ID
			present := false
			for _, id := range r.SourceEventIDs {
				present = present || id == anchor.ID
			}
			if !present {
				r.SourceEventIDs = append(r.SourceEventIDs, anchor.ID)
			}
			r.SourceVersions[anchor.ID] = anchor.Revision
		} else if r.AnchorMessageID == "" {
			r.AnchorMessageID = in.TraceID
			if r.AnchorMessageID == "" && in.Source != nil {
				r.AnchorMessageID = in.Source.MessageID
			}
		}
		if len(r.SourceEventIDs) == 0 {
			if systemReplyContext(in) {
				r.State = "system_context"
			} else if ephemeralReplyContext(in, e.Config) {
				r.State = "ephemeral_context"
			}
		} else {
			r.State = "verified"
		}
		if in.Preview {
			r.State = "verified"
		}
		r.ContextVersion = ReplyContextFingerprint(in, *r)
		if err := e.ValidateReplyContext(ctx, in, *r); err != nil {
			return Decision{}, err
		}
	}
	return d, nil
}
func groundingRisks(in Input, d Decision) []string {
	risks := []string{}
	if unsupportedPersonalDetails(in.Text, d.Messages) || misboundIncomingPerson(in, d) {
		risks = append(risks, "unsupported_personal_claim")
	}
	text := in.Text + " " + strings.Join(d.Messages, " ")
	for _, word := range []string{"你爸", "你妈", "他爸", "她爸", "父亲", "母亲", "心里空", "心里有点空", "站那", "刚才那张", "梦里", "你那张"} {
		if strings.Contains(text, word) {
			risks = append(risks, "unresolved_personal_reference")
			break
		}
	}
	if regexp.MustCompile(`(你|他|她|您|我)(的)?(爸|妈|父|母|爷爷|奶奶|外公|外婆|哥哥|姐姐|弟弟|妹妹|丈夫|妻子|儿子|女儿|家人)|很(伤心|难过|孤独|害怕|失落)|一定.*(难过|伤心|想念)|那是(他|她|你)的|你(以前|曾经|上次)|他(以前|曾经)|她(以前|曾经)`).MatchString(text) {
		risks = append(risks, "personal_relation_or_experience")
	}
	if len(in.Addressees) > 1 || len(in.MentionedIDs) > 1 {
		risks = append(risks, "multiple_participants")
	}
	// Short, underspecified group messages cannot support a long personal scene.
	// This structural signal deliberately catches paraphrases absent from any
	// vocabulary list while leaving short ordinary greetings on the fast path.
	if len([]rune(UserRequestText(in.Text))) < 16 && len([]rune(strings.Join(d.Messages, ""))) >= 16 {
		risks = append(risks, "underspecified_source_with_expanded_reply")
	}
	if regexp.MustCompile(`他|她|他们|她们|那个人|你们家|你家`).MatchString(text) {
		risks = append(risks, "third_person_reference")
	}
	for _, r := range d.ReplyContexts {
		risks = append(risks, r.RiskFlags...)
		if len(r.TargetIDs) > 1 || len(r.MediaSourceIDs) > 0 {
			risks = append(risks, "multiple_people_or_media")
		}
	}
	if in.ReplyContext != nil && in.ReplyContext.QuoteRequired {
		risks = append(risks, "delayed_reply")
	}
	return risks
}
func (e *Engine) ReviewGroundedReply(ctx context.Context, in Input, d Decision) (Decision, error) {
	return e.reviewGroundedReply(ctx, in, d, false)
}

// DiagnoseGroundedReply is an explicit, non-delivering management operation.
func (e *Engine) DiagnoseGroundedReply(ctx context.Context, in Input, d Decision) (Decision, error) {
	return e.reviewGroundedReply(ctx, in, d, true)
}

func (e *Engine) reviewGroundedReply(ctx context.Context, in Input, d Decision, diagnostic bool) (Decision, error) {
	if !in.IsGroup && (d.Action != "reply" || len(d.Messages) == 0) {
		return d, nil
	}
	if d.Action != "reply" && !in.MustReply || len(d.Messages) == 0 && !in.MustReply {
		return d, nil
	}
	if d.Action == "reply" && len(d.Messages) == 0 && (d.ImageID != "" || len(d.MediaIDs) > 0) {
		return d, nil
	}
	if original, ok := d.GenerationEvidence(); ok {
		if previous, exists := GenerationEvidenceFromContext(ctx); exists && original != previous {
			return e.groundingFailed(in, d, nil, "preflight_context_invalid", generationEvidenceChanged(), nil)
		}
		ctx = withGenerationEvidence(ctx, original)
	}
	evidence, captureErr := e.captureGenerationEvidence(ctx)
	if captureErr != nil {
		return e.groundingFailed(in, d, nil, "preflight_context_invalid", captureErr, nil)
	}
	ctx = withGenerationEvidence(ctx, evidence)
	d.generationEvidence = &evidence
	required := in.MustReply
	originalAction := d.Action
	if len(d.Messages) == 0 {
		d.Messages = []string{""}
		d.ReplyContexts = nil
	}
	d.Action = "reply"
	if err := e.validateExistingReplyProof(ctx, in, d); err != nil {
		return e.groundingFailed(in, d, nil, "preflight_proof_invalid", err, nil)
	}
	proposed := d
	var err error
	d, err = e.normalizeReplyContexts(ctx, in, d)
	if err != nil {
		if !recoverableInitialBindingError(proposed, err) {
			return e.groundingFailed(in, d, nil, "preflight_context_invalid", err, nil)
		}
		proposed.ReplyContexts = nil
		d, err = e.normalizeReplyContexts(ctx, in, proposed)
		if err != nil {
			return e.groundingFailed(in, proposed, nil, "preflight_context_invalid", err, nil)
		}
		for i := range d.ReplyContexts {
			d.ReplyContexts[i].RiskFlags = append(d.ReplyContexts[i].RiskFlags, "unverified_proposed_binding")
		}
	}
	if !diagnostic {
		// Automatic delivery uses the captured evidence and local authorization.
		if originalAction != "reply" || len(d.Messages) == 0 || strings.TrimSpace(strings.Join(d.Messages, "")) == "" {
			return e.groundingFailed(in, d, nil, "preflight_context_invalid", errors.New("reply_grounding_candidate_empty"), nil)
		}
		d, err = e.bindReplyEvidence(ctx, in, d)
		if err == nil {
			err = e.validateReplyTexts(ctx, in, d)
		}
		if err != nil {
			return e.groundingFailed(in, d, nil, "preflight_binding_invalid", err, nil)
		}
		e.writeReplyAudit(in, d, nil, "validated", nil)
		return d, nil
	}
	referenceIssues := e.sourceReferenceIssues(ctx, in, d)
	qualityIssues := mediaReplyQualityIssues(in, d)
	if !in.IsGroup && !e.temporalSpecificityRisk(ctx, in, d) && len(referenceIssues) == 0 && len(qualityIssues) == 0 {
		d, err = e.bindReplyEvidence(ctx, in, d)
		if err == nil {
			e.writeReplyAudit(in, d, nil, "validated", nil)
		}
		if err != nil {
			return e.groundingFailed(in, d, nil, "allowed_binding_invalid", err, nil)
		}
		return d, nil
	}
	risks := groundingRisks(in, d)
	risks = append(risks, e.structuralGroundingRisks(ctx, in, d)...)
	if len(qualityIssues) > 0 {
		risks = append(risks, "media_process_commentary")
	}
	access, private := e.Tools.(GroupReplyPrivacy)
	needsPrivacy := in.IsGroup && (diagnosticDraft(d.Messages) || private && access.NeedsPrivateReplyReview())
	if needsPrivacy {
		risks = append(risks, "group_privacy")
	}
	if required && originalAction != "reply" {
		risks = append(risks, "platform_requires_response")
	}
	d, err = e.bindReplyEvidence(ctx, in, d)
	if err != nil {
		return e.groundingFailed(in, d, risks, "preflight_binding_invalid", err, nil)
	}
	namedMismatch := false
	for i, text := range d.Messages {
		if e.validateNamedAddress(in, d.ReplyContexts[i], text) != nil {
			namedMismatch = true
		}
	}
	if namedMismatch {
		risks = append(risks, "named_address_target_mismatch")
	}
	if len(referenceIssues) > 0 {
		risks = append(risks, "source_reference_contract")
	}
	if len(risks) == 0 {
		e.writeReplyAudit(in, d, nil, "validated", nil)
		return d, nil
	}
	var initial GroundingVerdict
	if len(qualityIssues) > 0 {
		initial = GroundingVerdict{Verdict: "reject", Bubbles: qualityIssues}
	}
	empty := false
	for _, text := range d.Messages {
		empty = empty || strings.TrimSpace(text) == ""
	}
	if !empty && !namedMismatch && len(referenceIssues) == 0 && len(qualityIssues) == 0 {
		initial, err = e.checkReplyGrounding(ctx, in, d)
		if err != nil {
			failedCheck, _ := json.Marshal(map[string]any{"initial_check": initial})
			return e.groundingFailed(in, d, risks, "initial_check_failed", err, failedCheck)
		}
		if initial.Verdict == "allow" {
			if err = e.validateReplyTexts(ctx, in, d); err != nil {
				return e.groundingFailed(in, d, risks, "allowed_binding_invalid", err, nil)
			}
			payload, _ := json.Marshal(map[string]any{"initial_verdict": initial, "candidate": d})
			e.writeReplyAudit(in, d, risks, "checked", payload)
			return d, nil
		}
	}
	state := map[string]any{}
	if initial.ReviewBinding != nil {
		_ = json.Unmarshal(initial.ReviewBinding.OriginalState, &state)
	} else {
		state = e.groundingReviewState(ctx, in, d, needsPrivacy)
	}
	// Host audit snapshots and reverse aliases never enter an editor request.
	state["rejected_verdict"] = map[string]any{"verdict": initial.Verdict, "bubbles": initial.Bubbles}
	state["validation_error"] = "rejected_or_uncertain_candidate"
	if empty {
		state["validation_error"] = "platform_requires_useful_reply"
	}
	if namedMismatch {
		state["validation_error"] = "reply_named_address_target_mismatch"
	}
	if len(referenceIssues) > 0 {
		state["validation_error"] = "reply_source_reference_contract_failed"
		state["program_contract_violations"] = referenceIssues
	}
	routes, routeErr := e.replyRoutes(ctx, in, d)
	if routeErr != nil {
		return e.groundingFailed(in, d, risks, "repair_routes_invalid", routeErr, nil)
	}
	addReplyRouteState(state, routes)
	// Route proof stays host-only. Fact-evidence aliases do not authorize a
	// destination, change its quote anchor or mint a new permission grant.
	const editPolicy = `你是回复编辑器。依据rejected_verdict和当前来源修复candidate，保留有用且已核实的内容与实际进度，写得自然。只输出JSON：{"action":"reply|silent","bubbles":[{"text":"自然回复","route_id":"R1"}],"private_report":""}。
每泡选reply_routes已有route_id；目标、引用和授权由宿主绑定。outgoing_audiences决定出站“你”的对象；转述者、上传者和引用者不自动参与事件。人物、时间、媒体内容按对应来源与覆盖范围表达，分开用户原话和机器观察；助手旧话不证明人的事实。
原稿中未确认的操作仍写为未确认；已有成果和必要边界保留。用户问进度时可说明实际障碍，群聊内部故障写入private_report。must_reply=true时给有效回应或必要澄清；其他情况可silent。称呼与所选member_reply目标一致。`
	repairBinding, _, projectionErr := projectGroundingState(state)
	if projectionErr != nil {
		return e.groundingFailed(in, d, risks, "repair_input_invalid", projectionErr, nil)
	}
	proofs := routeProofs(routes)
	boundRepair, _ := canonicalGroundingJSON(map[string]any{"review_binding": repairBinding, "route_proofs": proofs})
	repairHash := sha256.Sum256(boundRepair)
	repairID := hex.EncodeToString(repairHash[:])
	payload, _ := json.Marshal(map[string]any{"review_binding": repairBinding, "route_proofs": proofs, "binding_hash": repairID})
	answer, callErr := e.groundingChat(deepseek.WithPurpose(ctx, "reply_grounding_repair"), editPolicy, []map[string]string{{"role": "user", "content": string(repairBinding.ReviewInput)}})
	if callErr != nil {
		return e.groundingFailed(in, d, risks, "repair_failed", callErr, payload)
	}
	out, decodeErr := decodeRouteEditedReply(answer)
	if decodeErr != nil {
		return e.groundingFailed(in, d, risks, "repair_invalid", errors.New("reply_grounding_repair_invalid"), payload)
	}
	if out.Action == "silent" || len(out.Bubbles) == 0 {
		return e.groundingFailed(in, d, risks, "repair_no_supported_reply", errors.New("reply_grounding_repair_no_supported_reply"), payload)
	}
	candidate, err := e.applyReplyRoutes(ctx, in, d, out.Bubbles, routes)
	if err != nil {
		return e.groundingFailed(in, d, risks, "repair_binding_invalid", err, payload)
	}
	candidate, err = e.normalizeReplyContexts(ctx, in, candidate)
	if err != nil {
		return e.groundingFailed(in, d, risks, "repair_binding_invalid", err, payload)
	}
	final, err := e.checkReplyGroundingWithPurpose(ctx, in, candidate, "reply_grounding_final_check")
	if err != nil {
		failedCheck, _ := json.Marshal(map[string]any{"repair_input": json.RawMessage(payload), "final_check": final})
		return e.groundingFailed(in, candidate, risks, "final_check_failed", err, failedCheck)
	}
	checks, _ := json.Marshal(map[string]any{"initial_verdict": initial, "repair_binding": repairBinding, "route_proofs": proofs, "edited_bubbles": out.Bubbles, "repair_binding_hash": repairID, "repaired_candidate": candidate, "final_verdict": final})
	if final.Verdict != "allow" {
		return e.groundingFailed(in, candidate, risks, "final_check_rejected", &groundingRejectedError{verdict: final}, checks)
	}
	if err = e.validateReplyTexts(ctx, in, candidate); err != nil {
		return e.groundingFailed(in, candidate, risks, "final_binding_invalid", err, checks)
	}
	e.writeReplyAudit(in, candidate, risks, "repaired_and_checked", checks)
	return candidate, nil
}

func (e *Engine) validateExistingReplyProof(ctx context.Context, in Input, d Decision) error {
	for _, r := range d.ReplyContexts {
		if r.ContextVersion != "" || r.State != "" || r.EvidenceEpoch != "" || r.EvidenceAuthorization != "" {
			if err := e.ValidateReplyContext(ctx, in, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func recoverableInitialBindingError(d Decision, err error) bool {
	for _, r := range d.ReplyContexts {
		if r.ContextVersion != "" || r.State != "" || len(r.SourceVersions) > 0 || r.EvidenceEpoch != "" || r.EvidenceAuthorization != "" {
			return false
		}
	}
	switch err.Error() {
	case "reply_context_count_mismatch", "reply_target_missing", "reply_target_not_grounded", "reply_anchor_not_grounded", "reply_retarget_quote_unavailable", "assistant_statement_is_not_fact_source":
		return true
	}
	return false
}

func (e *Engine) groundingFailed(in Input, d Decision, risks []string, state string, err error, snapshot []byte) (Decision, error) {
	failure := reviewFailure(state, err)
	d.ReviewFailure = &failure
	e.writeReplyAudit(in, d, risks, state, snapshot)
	var changed *GenerationEvidenceChangedError
	// Preflight previously returned an error. Keep that control flow (especially
	// the task pause signal) while enriching it; never turn a changed generation
	// into a successful optional-silence result and lose its continuation.
	if in.MustReply || errors.As(err, &changed) || strings.HasPrefix(state, "preflight_") || state == "allowed_binding_invalid" {
		return Decision{}, &ReviewError{Failure: failure, cause: err}
	}
	return Decision{Action: "silent", Messages: []string{}, Reason: reviewOutcomeReason(state, failure), ReviewFailure: &failure}, nil
}

func reviewOutcomeReason(state string, failure ReviewFailure) string {
	if failure.Code == "grounding_review_context_invalidated" {
		return "grounding_context_invalidated"
	}
	if state == "final_check_rejected" || state == "repair_no_supported_reply" {
		return "grounding_check_rejected"
	}
	return "grounding_check_failed"
}

func (e *Engine) structuralGroundingRisks(ctx context.Context, in Input, d Decision) []string {
	risks := []string{}
	if e.temporalSpecificityRisk(ctx, in, d) {
		risks = append(risks, "temporal_specificity_expansion")
	}
	if len(e.preparedExecutionEvidence(ctx, in)) > 0 {
		risks = append(risks, "observed_execution_results")
	}
	if in.QuotedSource != nil {
		risks = append(risks, "quoted_source")
	}
	if len(in.IncomingMedia) > 0 {
		risks = append(risks, "incoming_media")
	}
	if in.BackgroundResult || in.HistoricalReview || in.TaskID != "" || len(in.PlannedReviews) > 0 {
		risks = append(risks, "continued_or_delayed_context")
	}
	if len(e.preparedMemoryEvidence(ctx, in)) > 0 {
		risks = append(risks, "recalled_evidence")
	}
	for _, r := range d.ReplyContexts {
		if len(r.SourceEventIDs) > 1 || r.QuoteRequired {
			risks = append(risks, "multiple_or_delayed_sources")
		}
		for _, target := range r.TargetIDs {
			if target != in.Sender {
				risks = append(risks, "changed_audience")
			}
		}
		for _, id := range r.SourceEventIDs {
			if e.Store == nil {
				continue
			}
			if ev, err := e.Store.ReadEvent(ctx, []string{ScopeID(in.Channel, in.Account, in.Thread)}, id); err == nil {
				if ev.Sender != in.Sender || len(ev.IncomingMedia) > 0 {
					risks = append(risks, "cross_person_or_media_source")
				}
			}
		}
	}
	return risks
}
func replyAudiences(d Decision) []map[string]any {
	out := []map[string]any{}
	for i, r := range d.ReplyContexts {
		out = append(out, map[string]any{"message_index": i, "target_ids": r.TargetIDs, "outgoing_you_means": r.TargetIDs, "outgoing_singular_second_person_forms": []string{"你", "您"}, "outgoing_plural_second_person_forms": pluralSecondPersonForms, "plural_second_person_must_include": r.TargetIDs, "additional_plural_participants": "require their own evidence; incoming addressees do not replace the outgoing targets", "anchor_message_id": r.AnchorMessageID})
	}
	return out
}
func (e *Engine) proposedReviewBindings(in Input, scope Scope, original Decision, messages []string, proposals []ReplyContext) []ReplyContext {
	out := make([]ReplyContext, len(messages))
	for i := range out {
		if i < len(original.ReplyContexts) {
			out[i] = original.ReplyContexts[i]
		}
		if i >= len(proposals) || len(proposals[i].TargetIDs) == 0 {
			continue
		}
		p := proposals[i]
		p.EvidenceEpoch = out[i].EvidenceEpoch
		p.EvidenceAuthorization = out[i].EvidenceAuthorization
		// An assistant statement can explain why we are apologizing, but it is
		// never a user fact. Keep that evidence in the history, not the fact
		// binding. Unknown or inaccessible IDs still fail normal validation.
		p.SourceEventIDs = e.userFactSources(scope.ID, p.SourceEventIDs)
		if p.AnchorEventID != "" && len(e.userFactSources(scope.ID, []string{p.AnchorEventID})) == 0 {
			p.AnchorEventID = ""
			p.AnchorMessageID = ""
		}
		p.State = ""
		p.ContextVersion = ""
		p.SourceVersions = nil
		// Every new audience must already be an explicit participant link.
		allowed := true
		changed := false
		for _, id := range p.TargetIDs {
			if !relevantSender(in, id) {
				allowed = false
			}
			found := false
			for _, old := range out[i].TargetIDs {
				if old == id {
					found = true
				}
			}
			changed = changed || !found
		}
		if !allowed {
			p.TargetIDs = []string{"unverified_target"}
			out[i] = p
			continue
		}
		if changed {
			p.QuoteRequired = true
			p.RiskFlags = append(p.RiskFlags, "review_retargeted")
		}
		out[i] = p
	}
	return out
}

func (e *Engine) userFactSources(scope string, ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if e.Store != nil {
			if ev, err := e.Store.ReadEvent(context.Background(), []string{scope}, id); err == nil && ev.Role == "assistant" {
				continue
			}
		}
		out = append(out, id)
	}
	return out
}

func musicDeliveryRequested(turns []map[string]string) bool {
	for _, t := range turns {
		var p struct {
			Kind string
			Task bool `json:"task_continuation"`
		}
		if json.Unmarshal([]byte(t["content"]), &p) == nil && p.Kind == "current_message" && p.Task {
			return true
		}
	}
	return false
}

const compactGroupPolicy = `
【群聊】
回应current_message；历史、引用和工具结果按原时间理解。分清message_sender_id、speaker_id、引用作者和reply_contexts.target_ids；素材上传者不自动是内容中的人物。旁观可接话或silent，明确提问但未@可用quote_reply:true。群里自然聊当前内容，内部诊断留在管理记录。
引用素材看quoted_message_context与recent_media_focus；变更或停止用delivery_intent:file|replace|cancel。工具结果按真实阶段表达，已送达的内容直接复用。
只输出JSON：{"action":"reply","messages":["完整回复"],"reason":"reply"}或{"action":"silent","messages":[],"reason":"silent"}。图片用可用image_id，附件用工具返回的media_ids。`

// These lexical signals request semantic review. They cannot reject a candidate
// by themselves: quoted speech, hypotheses and supported history need scopes.
func unsupportedPersonalDetails(source string, messages []string) bool {
	draft := strings.Join(messages, " ")
	for _, signal := range []string{"心里有点空", "心里空", "空落落", "屋里突然", "屋里一下", "屏幕一黑", "耳朵还", "耳边还", "残留说话声", "站那儿", "站在那里", "缓一缓", "你一定很难过", "她很伤心", "他很伤心"} {
		if strings.Contains(draft, signal) && !strings.Contains(source, signal) {
			return true
		}
	}
	return false
}

var pluralSecondPersonForms = []string{"你们", "您们", "你俩", "您俩", "你两位", "您两位", "你二位", "您二位"}

const secondPersonParticipantForm = `(?:你们|您们|你俩|您俩|你两位|您两位|你二位|您二位|你|您)`

var kinshipSecondPerson = regexp.MustCompile(secondPersonParticipantForm + `(的)?(爸|妈|父亲|母亲|爷爷|奶奶|外公|外婆|哥哥|姐姐|弟弟|妹妹|丈夫|妻子|儿子|女儿|家人)`)

func unresolvedKinship(in Input) bool {
	text, _ := literalInput(in)
	return in.IsGroup && len(in.Addressees) == 0 && in.QuotedSource == nil && kinshipSecondPerson.MatchString(text)
}

// Incoming second-person relations belong to their explicit addressees. This
// check is deliberately separate from source-existence validation: a perfectly
// real source can still be applied to the wrong outgoing audience. A neutral
// acknowledgment remains valid, as does retargeting with a verified quote.
func misboundIncomingPerson(in Input, d Decision) bool {
	text, _ := literalInput(in)
	if !in.IsGroup || !kinshipSecondPerson.MatchString(text) || len(in.Addressees) == 0 {
		return false
	}
	for i, text := range d.Messages {
		if i >= len(d.ReplyContexts) {
			continue
		}
		matches := len(d.ReplyContexts[i].TargetIDs) > 0
		for _, target := range d.ReplyContexts[i].TargetIDs {
			linked := false
			for _, addressee := range in.Addressees {
				linked = linked || target == addressee
			}
			matches = matches && linked
		}
		if !matches && (kinshipSecondPerson.MatchString(text) || reboundPersonalExperience.MatchString(text) || reboundSingularFeeling.MatchString(text)) {
			return true
		}
	}
	return false
}

var reboundPersonalExperience = regexp.MustCompile(secondPersonParticipantForm + `(?:刚刚|刚才|刚|已经|之前|上次|曾经|又|才)?(?:和|跟|与|陪|一起|聊|通话|视频|见面|见了|回家|去了|经历)[^。！？!?\n]{0,48}`)
var reboundSingularFeeling = regexp.MustCompile(`(?:你|您)(?:刚刚|刚才|刚|已经|之前|上次|曾经|又|才)?(?:感到|觉得)[^。！？!?\n]{0,48}`)

func (e *Engine) preparedMemoryEvidence(ctx context.Context, in Input) []map[string]string {
	if in.SourceOnlyContext {
		return nil
	}
	var p struct {
		Messages []map[string]string `json:"messages"`
	}
	if json.Unmarshal(e.preparedContext(), &p) != nil {
		return nil
	}
	out := []map[string]string{}
	for _, t := range p.Messages {
		var header struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal([]byte(t["content"]), &header) == nil && header.Kind == "long_term_memory" {
			out = append(out, t)
		}
	}
	return e.refreshMemoryPackets(ctx, out)
}

func systemReplyContext(in Input) bool {
	return in.Mode == "proactive" || in.Mode == "admin_action" || in.Surf || in.ReportOnly || in.TaskID != "" && in.BackgroundResult
}
func ephemeralReplyContext(in Input, c Config) bool {
	r := in.Source
	return !c.Permissions.Remember && r != nil && r.Integrity == "verified" && r.Account == in.Account && r.Thread == in.Thread && r.Sender == in.Sender && r.MessageID != "" && r.MessageID == in.TraceID && r.ServerID != ""
}

// ReplyContextFingerprint binds the verified source versions and audience. It
// deliberately excludes presentation names and transient reception metadata.
func ReplyContextFingerprint(in Input, r ReplyContext) string {
	ids := func(v []string) []string { out := append([]string{}, v...); sort.Strings(out); return out }
	versions := map[string]int{}
	for k, v := range r.SourceVersions {
		versions[k] = v
	}
	var source any
	if r.State == "ephemeral_context" && in.Source != nil {
		source = []any{in.Source.Account, in.Source.Thread, in.Source.Sender, in.Source.MessageID, in.Source.ServerID, in.Source.OccurredAt, in.Source.BodyHash}
	}
	fields := []any{in.Account, in.Thread, r.State, r.AnchorMessageID, r.AnchorEventID, ids(r.TargetIDs), ids(r.SourceEventIDs), versions, ids(r.MediaSourceIDs), source, r.EvidenceEpoch, r.EvidenceAuthorization}
	if in.ConversationDelivery {
		fields = append(fields, "conversation_delivery")
	}
	b, _ := json.Marshal(fields)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
