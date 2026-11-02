package admin

import (
	"context"
	"errors"
	"time"
	"wechat-bot/internal/companion"
)

func (m *Manager) validateTaskSource(t agentTask) error {
	if err := validateTaskConstraints(t); err != nil {
		return err
	}
	if t.OwnerRequested && !(&permittedTools{manager: m, trace: companion.Input{Account: t.Account, Thread: t.Thread, Sender: t.Sender}}).privileged() {
		return errors.New("task_owner_authorization_revoked: 主人授权已撤销，未继续执行任务")
	}
	if t.Constraints == nil || t.Constraints.Source == nil {
		return nil
	}
	source := t.Constraints.Source
	if source.Integrity == "conflicted" || source.Integrity == "quarantined" {
		return errors.New("task_source_identity_conflicted: 来源身份冲突，未继续执行任务")
	}
	// Remember-disabled requests can still authorize work without a persisted
	// event. If an event was retained, its visibility and exact revision apply
	// before any worker or continuation can execute additional tools.
	if source.EventID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ev, err := m.memory.ReadEvent(ctx, []string{companion.ScopeID("wechat", t.Account, taskSourceThread(t))}, source.EventID)
	if err != nil || ev.Role != "user" || ev.Sender != t.Sender || source.Revision > 0 && source.Revision != ev.Revision {
		return errors.New("task_source_unavailable_or_changed: 来源已遗忘、更正或无法核验，未继续执行任务")
	}
	return nil
}

// TaskConstraints are minted by the authenticated tool adapter. Model text
// cannot replace requester, destination, source or selected voice on resume.
type TaskConstraints struct {
	Purpose string `json:"purpose"`
	Account string `json:"account"`
	// Thread is the evidence/source conversation. DeliveryThread is optional
	// for compatibility with existing tasks whose result belongs there too.
	Thread         string                  `json:"thread"`
	DeliveryThread string                  `json:"delivery_thread,omitempty"`
	Requester      string                  `json:"requester"`
	Source         *companion.SourceRef    `json:"source,omitempty"`
	ReplyContext   *companion.ReplyContext `json:"reply_context,omitempty"`
	VoiceID        string                  `json:"voice_id,omitempty"`
	VoiceProfile   string                  `json:"voice_profile_id,omitempty"`
	AssetID        string                  `json:"asset_id,omitempty"`
}

func (p *permittedTools) taskConstraints(purpose, voice, profile, asset string) *TaskConstraints {
	c := &TaskConstraints{Purpose: purpose, Account: p.trace.Account, Thread: p.trace.Thread, Requester: p.trace.Sender, VoiceID: voice, VoiceProfile: profile, AssetID: asset}
	if p.trace.Source != nil {
		v := *p.trace.Source
		c.Source = &v
	}
	if p.trace.ReplyContext != nil {
		v := *p.trace.ReplyContext
		c.ReplyContext = &v
	}
	return c
}
func validateTaskConstraints(t agentTask) error {
	if c := t.Constraints; c != nil {
		if c.Account != t.Account || c.Thread != taskSourceThread(t) || c.Requester != t.Sender || c.Purpose == "" {
			return errors.New("task_identity_constraints_mismatch")
		}
		if c.DeliveryThread != "" && c.DeliveryThread != taskDeliveryThread(t) {
			return errors.New("task_delivery_constraints_mismatch")
		}
		if c.Source != nil && (c.Source.Account != "" && c.Source.Account != t.Account || c.Source.Thread != "" && c.Source.Thread != taskSourceThread(t) || c.Source.Sender != "" && c.Source.Sender != t.Sender) {
			return errors.New("task_source_constraints_mismatch")
		}
		if c.VoiceProfile != "" && (t.MusicSpec == nil || t.MusicSpec.VoiceProfile != c.VoiceProfile) {
			return errors.New("task_voice_constraints_mismatch")
		}
		if c.VoiceID != "" && t.MusicSpec != nil && t.MusicSpec.VoiceID != c.VoiceID {
			return errors.New("task_voice_identity_constraints_mismatch")
		}
		if c.AssetID != "" && (t.MusicSpec == nil || t.MusicSpec.Reference != c.AssetID) {
			return errors.New("task_asset_constraints_mismatch")
		}
	}
	return nil
}

func taskSourceThread(t agentTask) string {
	if t.SourceThread != "" {
		return t.SourceThread
	}
	return t.Thread
}

func taskDeliveryThread(t agentTask) string {
	if t.DeliveryThread != "" {
		return t.DeliveryThread
	}
	return t.Thread
}
func taskResultInput(t agentTask, in *companion.Input) *companion.Input {
	// A retryable model failure may occur after an authorized read completed.
	// Carry only the task's own persisted checkpoint into the resumed input;
	// the bot validates its evidence again before exposing it to the model.
	in.RecoveryToolResults = append([]companion.RecoveryToolResult(nil), t.RecoveryTools...)
	if t.RecoveryEvidence != nil {
		copy := *t.RecoveryEvidence
		in.RecoveryEvidence = &copy
	}
	if t.RecoveryPrivacy != nil {
		copy := *t.RecoveryPrivacy
		copy.SourceThreads = append([]string(nil), t.RecoveryPrivacy.SourceThreads...)
		in.RecoveryPrivacy = &copy
	}
	if t.Constraints != nil {
		in.Source = t.Constraints.Source
		// A quote in the source group must never be replayed against an owner
		// private delivery conversation. The source stays available as evidence.
		if taskSourceThread(t) == taskDeliveryThread(t) {
			in.ReplyContext = t.Constraints.ReplyContext
		}
		in.ExpectedVoiceID = t.Constraints.VoiceID
		in.ExpectedVoiceProfile = t.Constraints.VoiceProfile
	}
	return in
}

func (p *permittedTools) bindTaskSpeechVoice(voiceID string) error {
	if err := p.bindSelectedVoice(voiceID, ""); err != nil {
		return err
	}
	if p.trace.ExpectedVoiceID != "" && p.trace.ExpectedVoiceID != voiceID {
		return toolInputError("task_voice_selection_mismatch: 当前请求绑定的声线不能被默认或其他声线替代")
	}
	if p.trace.TaskID == "" {
		return nil
	}
	trusted := p.privileged()
	a := p.manager.agent
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.data.Tasks[p.trace.TaskID]
	if !ok || t.Account != p.trace.Account || t.Thread != p.trace.Thread || t.Sender != p.trace.Sender || t.State == "cancelled" {
		return toolInputError("task_voice_request_identity_unavailable")
	}
	if t.Constraints != nil && t.Constraints.VoiceID != "" && t.Constraints.VoiceID != voiceID {
		return toolInputError("task_voice_selection_mismatch: 原任务指定的声线不能被默认或其他声线替代")
	}
	if voiceID == "" || t.Constraints != nil && t.Constraints.VoiceID == voiceID {
		return nil
	}
	old := copyTask(t)
	if t.Constraints == nil {
		t.Constraints = p.taskConstraints("speech", "", "", "")
	} else {
		t.Constraints = copyTask(t).Constraints
	}
	t.Constraints.VoiceID = voiceID
	t.OwnerRequested = t.OwnerRequested || trusted
	a.data.Tasks[t.ID] = t
	if err := a.persist(); err != nil {
		a.data.Tasks[t.ID] = old
		return err
	}
	return nil
}

func (p *permittedTools) SelectedVoiceIdentity() (string, string) {
	p.mediaMu.Lock()
	defer p.mediaMu.Unlock()
	voice, profile := p.selectedVoiceID, p.selectedVoiceProfile
	if voice == "" {
		voice = p.trace.ExpectedVoiceID
	}
	if profile == "" {
		profile = p.trace.ExpectedVoiceProfile
	}
	return voice, profile
}
func (p *permittedTools) bindSelectedVoice(voice, profile string) error {
	p.mediaMu.Lock()
	defer p.mediaMu.Unlock()
	if p.selectedVoiceID != "" && p.selectedVoiceID != voice || p.trace.ExpectedVoiceID != "" && p.trace.ExpectedVoiceID != voice || p.selectedVoiceProfile != "" && p.selectedVoiceProfile != profile || p.trace.ExpectedVoiceProfile != "" && p.trace.ExpectedVoiceProfile != profile {
		return toolInputError("named_voice_selection_mismatch: 本轮已选择的声线不能由默认或其他声线替代")
	}
	if voice != "" {
		p.selectedVoiceID = voice
	}
	if profile != "" {
		p.selectedVoiceProfile = profile
	}
	return nil
}
