package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/workspacefs"
)

type enrollmentRuntime struct {
	mu     sync.Mutex
	active map[string]context.CancelFunc
	wg     sync.WaitGroup
	closed bool
}

func (r *enrollmentRuntime) close() {
	r.mu.Lock()
	r.closed = true
	for _, cancel := range r.active {
		cancel()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

type enrollmentSample struct {
	ConfirmationMessage    string  `json:"confirmation_message_id,omitempty"`
	SingleSpeakerConfirmed bool    `json:"owner_single_speaker_confirmed,omitempty"`
	Message                string  `json:"message_id"`
	Thread                 string  `json:"thread_id"`
	SHA                    string  `json:"sha256"`
	Transcript             string  `json:"transcript"`
	Seconds                float64 `json:"seconds"`
}
type enrollmentJob struct {
	Stage    string          `json:"-"`
	ID       string          `json:"job_id"`
	Action   string          `json:"action"`
	Revision int             `json:"revision"`
	State    string          `json:"state"`
	Reason   string          `json:"reason,omitempty"`
	Text     string          `json:"preview_text"`
	Origin   companion.Input `json:"origin"`
	Delivery string          `json:"delivery,omitempty"`
	Digest   string          `json:"package_digest"`
	Preview  string          `json:"preview_sha256,omitempty"`
	VoiceID  string          `json:"voice_id,omitempty"`
}
type enrollmentProfile struct {
	ID       string             `json:"profile_id"`
	Name     string             `json:"name"`
	Owner    string             `json:"owner"`
	Revision int                `json:"revision"`
	Samples  []enrollmentSample `json:"samples"`
	Jobs     []enrollmentJob    `json:"jobs"`
}
type enrollmentInput struct {
	Action     string `json:"action"`
	Name       string `json:"name"`
	Message    string `json:"message_id"`
	Transcript string `json:"transcript"`
	Text       string `json:"preview_text"`
	Job        string `json:"job_id"`
}

func (p *permittedTools) enrollmentAllowed() bool {
	return p.privileged() && p.trace.Sender != "" && p.workspaceExecutionAllowed() && p.manager.agent.config().Host.Mode == "full" && p.manager.agent.config().CrossConversation && p.manager.agentTaskActive(p.trace) && p.manager.enrollmentInstalled()
}
func (m *Manager) enrollmentInstalled() bool {
	ctx := context.Background()
	store, err := m.packageStore()
	if err != nil {
		return false
	}
	installed, err := store.Installed(ctx, "speech")
	return err == nil && installed.Package.Files["runtime/enroll.py"] != "" && m.namedSpeechPluginConfigured(ctx)
}

func (p *permittedTools) enrollmentTools() []mcptools.Tool {
	if !p.enrollmentAllowed() {
		return nil
	}
	return []mcptools.Tool{agentTool("voice_enroll", "Manage the real owner's private SPEAKING voice. Use add only for a recording the owner explicitly selected, with its actual message_id and verified/corrected transcript. Never enroll by proximity. preview uses a reference recording and sends a new sentence privately by collect; only train on an explicit request. status inspects, resume continues an interrupted job, collect delivers its private audition idempotently. A reference preview is not trained weights. For singing use the separate RVC workflow.", `{"type":"object","properties":{"action":{"type":"string","enum":["add","list","status","preview","train","resume","cancel","collect","delete"]},"name":{"type":"string"},"message_id":{"type":"string"},"transcript":{"type":"string"},"preview_text":{"type":"string"},"job_id":{"type":"string"}},"required":["action"],"additionalProperties":false}`)}
}
func (m *Manager) enrollDir(id string) string { return filepath.Join(m.dir, "voice-enrollments", id) }
func (m *Manager) loadEnrollment(id string) (enrollmentProfile, error) {
	var pr enrollmentProfile
	data, e := readEnrollmentFile(filepath.Join(m.enrollDir(id), "profile.json"), 8<<20)
	if e != nil {
		return pr, e
	}
	e = json.Unmarshal(data, &pr)
	if e != nil || pr.ID != id {
		return pr, errors.New("voice_enrollment_metadata_invalid")
	}
	return pr, nil
}
func (m *Manager) saveEnrollment(pr enrollmentProfile) error {
	b, err := json.Marshal(pr)
	if err != nil {
		return err
	}
	return writePrivate(filepath.Join(m.enrollDir(pr.ID), "profile.json"), b)
}
func enrollmentSummary(pr enrollmentProfile) map[string]any {
	var seconds float64
	for _, s := range pr.Samples {
		seconds += s.Seconds
	}
	jobs := make([]map[string]any, 0, len(pr.Jobs))
	for _, j := range pr.Jobs {
		jobs = append(jobs, enrollmentJobView(j))
	}
	return map[string]any{"name": pr.Name, "profile_id": pr.ID, "sample_revision": pr.Revision, "sample_count": len(pr.Samples), "raw_audio_seconds": seconds, "jobs": jobs}
}
func readEnrollmentFile(path string, limit int64) ([]byte, error) {
	r, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	f, err := workspacefs.OpenRegular(r, filepath.Base(path))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("voice_enrollment_metadata_too_large")
	}
	return b, err
}
func (m *Manager) enrollmentStages(pr *enrollmentProfile) {
	for i := range pr.Jobs {
		j := &pr.Jobs[i]
		if j.State != "running" {
			continue
		}
		if m.enrollment.active[j.ID] == nil {
			j.State = "interrupted"
			continue
		}
		directory := filepath.Join(m.enrollDir(pr.ID), "jobs", j.ID)
		raw, err := readEnrollmentFile(filepath.Join(directory, ".running.json"), 8192)
		if err != nil {
			continue
		}
		var phase struct {
			ID       string `json:"job_id"`
			Revision int
			Stage    string
		}
		if json.Unmarshal(raw, &phase) == nil && phase.ID == j.ID && phase.Revision == j.Revision && len(phase.Stage) < 64 {
			j.Stage = phase.Stage
		}
	}
}
func (p *permittedTools) callEnrollment(ctx context.Context, _ string, raw json.RawMessage) (string, error) {
	if !p.enrollmentAllowed() {
		return "", toolInputError("private_voice_enrollment_owner_authorization_required")
	}
	var in enrollmentInput
	if musicStrictJSON(raw, &in) != nil {
		return "", toolInputError("invalid_voice_enrollment_arguments")
	}
	if !enrollmentActionRequested(p.trace, in.Action) {
		return "", toolInputError("owner_must_explicitly_request_this_voice_action")
	}
	p.privateDiagnostics = true
	m := p.manager
	rt := &m.enrollment
	encode := func(v any) (string, error) { b, e := json.Marshal(v); return string(b), e }
	if in.Action == "list" {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		dirs, e := os.ReadDir(filepath.Join(m.dir, "voice-enrollments"))
		if e != nil && !os.IsNotExist(e) {
			return "", e
		}
		rows := []map[string]any{}
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			pr, e := m.loadEnrollment(d.Name())
			if e != nil {
				return "", e
			}
			if pr.Owner == p.trace.Sender {
				m.enrollmentStages(&pr)
				rows = append(rows, enrollmentSummary(pr))
			}
		}
		return encode(rows)
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 80 {
		return "", toolInputError("specify_private_voice_name")
	}
	id := "ve-" + agentID(p.trace.Account, p.trace.Sender, in.Name)[:24]
	if in.Action == "add" {
		return p.addEnrollmentSample(ctx, id, in)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	pr, e := m.loadEnrollment(id)
	if e != nil || pr.Owner != p.trace.Sender {
		return "", toolInputError("private_voice_enrollment_not_found")
	}
	m.enrollmentStages(&pr)
	var job *enrollmentJob
	for i := range pr.Jobs {
		j := &pr.Jobs[i]

		if j.ID == in.Job {
			job = j
		}
	}
	switch in.Action {
	case "status":
		return encode(enrollmentSummary(pr))
	case "preview", "train", "resume":
		return p.startEnrollmentLocked(ctx, &pr, in, job)
	case "cancel":
		if job == nil {
			return "", toolInputError("specify_existing_job_id")
		}
		wasRunning := job.State == "running" || job.State == "interrupted"
		// Stop a queued private audition as well as an active local worker.  The
		// completed preview file remains private library data, but it must not
		// silently re-enter the WeChat queue after its owner changed their mind.
		if e = p.cancelEnrollmentDelivery(ctx, *job); e != nil {
			return "", e
		}
		job.State = "cancelled"
		job.Reason = "已取消，未提交的试听不会发送"
		if wasRunning {
			if e = m.saveEnrollment(pr); e != nil {
				return "", e
			}
			if stop := rt.active[job.ID]; stop != nil {
				stop()
			}
		}
		if e = m.saveEnrollment(pr); e != nil {
			return "", e
		}
		return encode(enrollmentJobView(*job))
	case "collect":
		// A preview is a deliverable only after the worker has durably verified it.
		// In particular, a cancelled/interrupted job must not be resurrected by a
		// later model turn that merely repeats `collect`.
		if job == nil || job.Preview == "" || (job.State != "reference_preview_ready" && job.State != "trained") {
			return "", toolInputError("audition_not_yet_verified_check_status")
		}
		receipt, deliveryErr := p.deliverEnrollment(ctx, pr, *job)
		if deliveryErr != nil {
			return "", deliveryErr
		}
		job.Delivery = enrollmentReceipt(receipt)
		if e = m.saveEnrollment(pr); e != nil {
			return "", e
		}
		return receipt, nil
	case "delete":
		for _, j := range pr.Jobs {
			if rt.active[j.ID] != nil {
				return "", toolInputError("cancel_voice_job_before_deleting")
			}
		}
		if err := m.removeEnrollmentVoices(pr); err != nil {
			return "", err
		}
		root, e := workspacefs.EnsurePath(filepath.Dir(m.enrollDir(id)))
		if e != nil {
			return "", e
		}
		defer root.Close()
		if e = root.RemoveAll(id); e != nil {
			return "", e
		}
		return encode(map[string]bool{"deleted": true})
	default:
		return "", toolInputError("invalid_voice_enrollment_action")
	}
}
func (p *permittedTools) addEnrollmentSample(ctx context.Context, id string, in enrollmentInput) (string, error) {
	if in.Message == "" {
		return "", toolInputError("select_an_original_owner_voice_message")
	}
	m := p.manager
	asset, msg, e := m.resolveInboundArtifact(ctx, p.trace, in.Message)
	if e != nil || msg.SenderID != p.trace.Sender || msg.Type != 34 {
		return "", toolInputError("voice_sample_requires_owners_original_voice_in_this_conversation")
	}
	audio, e := m.cachedAssetData(asset)
	if e != nil {
		return "", toolInputError("voice_sample_original_unavailable")
	}
	wav, e := normalizeSpeech(ctx, m.dir, audio)
	if e != nil {
		return "", toolInputError("voice_sample_could_not_decode")
	}
	_, duration, e := namedSpeechWAVInfo(wav)
	if e != nil {
		return "", e
	}
	confirmation := outboundAnchor(p.trace)
	singleSpeaker := confirmation != "" && enrollmentSingleSpeakerConfirmed(p.trace)
	transcript := strings.TrimSpace(in.Transcript)
	if transcript == "" {
		asr, e := m.newMediaTranscriptionPolicy(ctx, msg, wav)
		if e != nil {
			return "", toolInputError("voice_transcript_unavailable_supply_verified_transcript")
		}
		transcript, e = asr.transcribe(ctx)
		if e != nil {
			return "", toolInputError("voice_transcript_unavailable_supply_verified_transcript")
		}
	}
	if strings.TrimSpace(transcript) == "" || len(transcript) > 16384 {
		return "", toolInputError("invalid_reference_transcript")
	}
	rt := &m.enrollment
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !p.enrollmentAllowed() || rt.closed {
		return "", toolInputError("voice_enrollment_authorization_changed")
	}
	pr, e := m.loadEnrollment(id)
	if os.IsNotExist(e) {
		pr = enrollmentProfile{ID: id, Name: in.Name, Owner: p.trace.Sender}
	} else if e != nil {
		return "", e
	}
	if pr.Owner != p.trace.Sender {
		return "", errors.New("private_voice_owner_mismatch")
	}
	hash := namedSpeechSHA(wav)
	for i, old := range pr.Samples {
		if old.Thread == msg.ThreadID && old.Message == msg.ID {
			if old.SHA != hash {
				return "", toolInputError("voice_sample_original_changed")
			}
			if old.Transcript != transcript || singleSpeaker && !old.SingleSpeakerConfirmed {
				if singleSpeaker {
					pr.Samples[i].ConfirmationMessage = confirmation
				}
				pr.Samples[i].SingleSpeakerConfirmed = old.SingleSpeakerConfirmed || singleSpeaker
				pr.Samples[i].Transcript = transcript
				pr.Revision++
				if e = m.saveEnrollment(pr); e != nil {
					return "", e
				}
			}
			b, _ := json.Marshal(enrollmentSummary(pr))
			return string(b), nil
		}
		if old.SHA == hash {
			return "", toolInputError("identical_recording_already_enrolled")
		}
	}
	root, e := workspacefs.EnsurePath(filepath.Join(m.enrollDir(id), "samples"))
	if e != nil {
		return "", e
	}
	defer root.Close()
	if e = publishVerifiedStream(root, hash+".wav", bytes.NewReader(wav), int64(len(wav)), hash); e != nil {
		return "", e
	}
	pr.Samples = append(pr.Samples, enrollmentSample{ConfirmationMessage: confirmation, SingleSpeakerConfirmed: singleSpeaker, Message: msg.ID, Thread: msg.ThreadID, SHA: hash, Transcript: transcript, Seconds: duration})
	pr.Revision++
	if e = m.saveEnrollment(pr); e != nil {
		return "", e
	}
	b, e := json.Marshal(enrollmentSummary(pr))
	return string(b), e
}
