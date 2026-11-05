package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func enrollmentOwnerText(in companion.Input) string {
	if in.BackgroundResult || in.Surf || in.Mode == "autonomous" {
		return ""
	}
	text := in.Text
	if in.OriginalText != nil {
		text = *in.OriginalText
	}
	return strings.ToLower(companion.UserRequestText(text))
}

func enrollmentSingleSpeakerConfirmed(in companion.Input) bool {
	text := enrollmentOwnerText(in)
	for _, negative := range []string{"不确定", "好像", "可能", "不是", "不只", "还有别人", "？", "?"} {
		if strings.Contains(text, negative) {
			return false
		}
	}
	for _, phrase := range []string{"只有我一个人", "只有我自己", "只有我的声音", "只有我在说", "仅有我本人", "我自己的单人", "only my voice", "only me speaking"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

func enrollmentActionRequested(in companion.Input, action string) bool {
	if action != "train" && action != "add" && action != "delete" && action != "resume" {
		return true
	}
	text := enrollmentOwnerText(in)
	has := func(options ...string) bool {
		for _, s := range options {
			if strings.Contains(text, s) {
				return true
			}
		}
		return false
	}
	if has("需要几", "需要多少", "需要什么", "怎么训练", "如何训练", "是否能", "是不是", "能不能告诉", "？", "?", "不要", "先别", "别用", "别训", "不训练", "取消", "不用", "don't", "do not", "not train") {
		return false
	}
	switch action {
	case "train":
		return has("训练", "训一版", "再训", "重训", "train")
	case "delete":
		return has("删除", "删掉", "移除", "delete", "remove")
	case "resume":
		return has("继续", "恢复", "重试", "resume", "retry")
	default:
		return has("样本", "声线", "声音模型", "训练", "克隆", "用我的声音", "补录", "加进去", "这段也", "voice", "sample")
	}
}

func enrollmentJobView(j enrollmentJob) map[string]any {
	return map[string]any{"job_id": j.ID, "action": j.Action, "revision": j.Revision, "state": j.State, "reason": j.Reason, "preview_verified": j.Preview != "", "voice_id": j.VoiceID, "private_delivery": j.Delivery, "stage": j.Stage}
}

func (p *permittedTools) startEnrollmentLocked(ctx context.Context, pr *enrollmentProfile, in enrollmentInput, job *enrollmentJob) (string, error) {
	m := p.manager
	rt := &m.enrollment
	if rt.closed || m.draining() {
		return "", errors.New("voice_enrollment_service_draining")
	}
	if len(pr.Samples) == 0 {
		return "", toolInputError("select_voice_samples_first")
	}
	store, err := m.packageStore()
	if err != nil {
		return "", err
	}
	inst, err := store.Installed(ctx, "speech")
	if err != nil || !inst.Enabled || inst.Removed {
		return "", errors.New("speech_plugin_unavailable")
	}
	pkg, err := store.PackageDirectory(inst.Digest)
	if err != nil {
		return "", err
	}
	if _, err = os.Stat(filepath.Join(pkg, "runtime", "enroll.py")); err != nil {
		return "", errors.New("speech_plugin_enrollment_upgrade_required")
	}
	encode := func(j *enrollmentJob) (string, error) {
		b, e := json.Marshal(enrollmentJobView(*j))
		return string(b), e
	}
	if in.Action == "resume" {
		if job == nil || job.Revision != pr.Revision || job.Digest != inst.Digest {
			return "", toolInputError("resume_requires_same_job_samples_and_plugin_version")
		}
		if job.State != "interrupted" && job.State != "failed" {
			return encode(job)
		}
	} else {
		origin := p.trace
		identity := origin.OriginTraceID
		if identity == "" && origin.Source != nil {
			identity = origin.Source.MessageID
		}
		if identity == "" {
			identity = origin.TraceID
		}
		if identity == "" {
			return "", toolInputError("voice_enrollment_requires_stable_request")
		}
		sentence := strings.TrimSpace(in.Text)
		if sentence == "" {
			sentence = "今天的微风轻轻吹过窗台，这是一段新的声音试听。"
		}
		for _, s := range pr.Samples {
			if strings.Contains(s.Transcript, sentence) {
				return "", toolInputError("preview_sentence_must_be_new")
			}
		}
		if len([]rune(sentence)) > 500 {
			return "", toolInputError("preview_text_too_long")
		}
		id := "vjob-" + agentID(pr.ID, origin.Thread, identity, in.Action, fmt.Sprint(pr.Revision))[:24]
		for i := range pr.Jobs {
			if pr.Jobs[i].ID == id {
				return encode(&pr.Jobs[i])
			}
		}
		for _, j := range pr.Jobs {
			if rt.active[j.ID] != nil {
				return "", toolInputError("voice_job_in_progress_check_status")
			}
		}
		origin = companion.Input{Account: origin.Account, Channel: origin.Channel, Thread: origin.Thread, Sender: origin.Sender, IsGroup: origin.IsGroup, Mode: origin.Mode, Source: origin.Source, TaskID: origin.TaskID, OriginTraceID: identity}
		scope := &permittedTools{manager: m}
		scope.SetTrace(origin)
		// Bind the user's original grant now, before any long-running GPU work.
		scope.trace.TraceID = identity
		scope.trace.ReplyContext = p.trace.ReplyContext
		proof, err := scope.prepareCrossMediaOrigin(ctx)
		if err != nil {
			return "", err
		}
		origin.ReplyContext = proof.ReplyContext
		pr.Jobs = append(pr.Jobs, enrollmentJob{ID: id, Action: in.Action, Text: sentence, Revision: pr.Revision, Digest: inst.Digest, Origin: origin})
		job = &pr.Jobs[len(pr.Jobs)-1]
	}
	for _, j := range pr.Jobs {
		if rt.active[j.ID] != nil {
			return "", toolInputError("voice_job_in_progress_check_status")
		}
	}
	job.State = "running"
	job.Reason = ""
	if err = m.saveEnrollment(*pr); err != nil {
		return "", err
	}
	work, cancel := context.WithCancel(context.Background())
	if rt.active == nil {
		rt.active = map[string]context.CancelFunc{}
	}
	rt.active[job.ID] = cancel
	rt.wg.Add(1)
	scoped := &permittedTools{manager: m}
	scoped.SetTrace(job.Origin)
	go scoped.executeEnrollment(work, cancel, *pr, *job)
	return encode(job)
}

func (p *permittedTools) executeEnrollment(ctx context.Context, cancel context.CancelFunc, pr enrollmentProfile, job enrollmentJob) {
	m := p.manager
	rt := &m.enrollment
	defer rt.wg.Done()
	defer cancel()
	defer func() { rt.mu.Lock(); delete(rt.active, job.ID); rt.mu.Unlock() }()
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !p.enrollmentAllowed() {
					cancel()
					return
				}
			}
		}
	}()
	result, runErr := p.runEnrollment(ctx, pr, job, m.enrollDir(pr.ID))
	wasCancelled := ctx.Err() != nil
	cancel()
	<-monitorDone
	rt.mu.Lock()
	defer rt.mu.Unlock()
	pr, err := m.loadEnrollment(pr.ID)
	if err != nil {
		return
	}
	for i := range pr.Jobs {
		j := &pr.Jobs[i]
		if j.ID != job.ID || j.State != "running" {
			continue
		}
		switch {
		case rt.closed || wasCancelled:
			j.State = "interrupted"
			j.Reason = "运行已中断，可从同一任务恢复"
		case pr.Revision != j.Revision:
			j.State = "interrupted"
			j.Reason = "样本版本已更新，请用当前样本重新生成"
		case !p.enrollmentAllowed():
			j.State = "interrupted"
			j.Reason = "当前授权已变更"
		case runErr != nil:
			j.State = "failed"
			j.Reason = runErr.Error()
		default:
			// Copy the verified audition before publishing the new catalogue entry.
			err = copyEnrollmentOutput(result.Dir, "preview.wav", m.enrollDir(pr.ID), j.ID+".wav", result.PreviewSHA)
			if err == nil && j.Action == "train" {
				j.VoiceID, err = m.registerEnrollmentVoice(pr, *j, result)
			}
			if err != nil {
				j.State = "failed"
				j.Reason = err.Error()
			} else {
				j.Preview = result.PreviewSHA
				if j.Action == "train" {
					j.State = "trained"
				} else {
					j.State = "reference_preview_ready"
				}
			}
		}
		if err = m.saveEnrollment(pr); err != nil || j.Preview == "" {
			return
		}
		delivery, stop := context.WithTimeout(context.Background(), 90*time.Second)
		var receipt string
		receipt, err = p.deliverEnrollment(delivery, pr, *j)
		stop()
		if err != nil {
			j.Delivery = err.Error()
		} else {
			j.Delivery = enrollmentReceipt(receipt)
		}
		_ = m.saveEnrollment(pr)
		return
	}
}

func (p *permittedTools) deliverEnrollment(ctx context.Context, pr enrollmentProfile, j enrollmentJob) (string, error) {
	origin := &permittedTools{manager: p.manager}
	origin.SetTrace(j.Origin)
	origin.trace.TraceID = j.Origin.OriginTraceID
	if !origin.enrollmentAllowed() || pr.Owner != p.trace.Sender || !origin.crossAllowed(pr.Owner, pr.Owner) {
		return "", errors.New("private_audition_delivery_not_authorized")
	}
	path := filepath.Join(p.manager.enrollDir(pr.ID), j.ID+".wav")
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() {
		return "", errors.New("verified_audition_missing")
	}
	assetIn := companion.Input{Account: p.trace.Account, Channel: "wechat", Thread: pr.Owner, Sender: pr.Owner, TraceID: j.ID}
	a, err := p.manager.rememberStreamAsset(assetIn, path, "试听.wav", "audio/wav", st.Size())
	if err != nil {
		return "", err
	}
	if a.Hash != j.Preview {
		return "", errors.New("private_audition_hash_changed")
	}
	raw, _ := json.Marshal(crossMediaInput{Source: pr.Owner, Target: pr.Owner, Asset: a.ID, Kind: "voice", Key: j.ID})
	return origin.sendMedia(ctx, raw)
}

// cancelEnrollmentDelivery owns the narrow race window between an owner
// cancelling a preview and the native worker submitting that preview.  It
// changes only a still-unsubmitted, hash-bound batch; a submitted part is
// never guessed away or resent under a new idempotency key.
func (p *permittedTools) cancelEnrollmentDelivery(ctx context.Context, j enrollmentJob) error {
	parts := strings.Fields(j.Delivery)
	if len(parts) != 2 || parts[1] == "" {
		return nil
	}
	operationID := parts[1]
	m := p.manager
	m.crossSendMu.Lock()
	defer m.crossSendMu.Unlock()
	batch, err := m.readCrossBatch(operationID)
	if err != nil {
		return errors.New("private_audition_delivery_status_unknown")
	}
	if batch.Account != p.trace.Account || batch.Origin != j.Origin.Thread || batch.Sender != j.Origin.Sender || batch.Source != p.trace.Sender || batch.Target != p.trace.Sender {
		return errors.New("private_audition_delivery_identity_mismatch")
	}
	if !batch.CancelledAt.IsZero() {
		return nil
	}
	native, ok := m.native.(nativeTools)
	if !ok {
		return errors.New("private_audition_delivery_status_unavailable")
	}
	for _, part := range batch.Parts {
		status, lookupErr := native.LookupSend(ctx, part.Key)
		if errors.Is(lookupErr, linuxnative.ErrToolNotFound) {
			continue
		}
		if lookupErr != nil {
			return errors.New("private_audition_delivery_status_unknown")
		}
		if validCrossReceipt(batch, part, status) || status.State != "" && status.State != "prepared" {
			return errors.New("private_audition_already_submitted")
		}
	}
	batch.CancelledAt = time.Now().UTC()
	data, _ := json.Marshal(batch)
	if err = writePrivate(filepath.Join(m.dir, "cross-media", batch.ID+".json"), data); err != nil {
		return err
	}
	return m.queueCrossBatch(batch)
}

func enrollmentReceipt(raw string) string {
	var status struct {
		State string `json:"state"`
		ID    string `json:"operation_id"`
	}
	if json.Unmarshal([]byte(raw), &status) != nil || status.ID == "" {
		return "unknown"
	}
	return status.State + " " + status.ID
}
