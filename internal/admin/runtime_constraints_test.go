package admin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestProcessAdmissionDiagnosticsDoNotExposeUntrustedOutput(t *testing.T) {
	err := processAdmissionFailure("路径不在已授权工作目录内 secret sentinel", "scope-id")
	f := err.(*RuntimeFailure)
	code, description := f.SafeToolFailure()
	if code != "path_scope_denied" || !f.NotStarted || f.Stage != "scope" || strings.Contains(description, "secret sentinel") || !strings.Contains(description, "project_read") {
		t.Fatal(code, description)
	}
	var decoded RuntimeFailure
	if json.Unmarshal([]byte(description), &decoded) != nil || decoded.EffectiveScope != "scope-id" {
		t.Fatal(description)
	}
}

func TestNamedSpeechNeverDefaultsWhenRegisteredVoiceRequested(t *testing.T) {
	voices := []voiceEntry{{ID: "mom-speech", Name: "mom", Engine: "GPT-SoVITS"}, {ID: "mom-sing", Name: "mom", Engine: "RVC"}, {ID: "default", Name: "character"}}
	for _, id := range []string{"", "default"} {
		if err := validateRequestedSpeechVoice("用mom的声线讲解", id, voices); err == nil {
			t.Fatal("named request silently defaulted", id)
		}
	}
	if err := validateRequestedSpeechVoice("用mom的声线讲解", "mom-speech", voices); err != nil {
		t.Fatal(err)
	}
	if err := validateRequestedSpeechVoice("普通语音", "", voices); err != nil {
		t.Fatal(err)
	}
}

func TestSelectedVoiceIdentityIsCallLocalAndCannotDefaultAfterFailure(t *testing.T) {
	_, p := trustedAgent(t)
	if err := p.bindSelectedVoice("selected", "profile"); err != nil {
		t.Fatal(err)
	}
	if err := p.bindSelectedVoice("", ""); err == nil {
		t.Fatal("failed named inference fell through to default")
	}
	voice, profile := p.SelectedVoiceIdentity()
	if voice != "selected" || profile != "profile" {
		t.Fatal(voice, profile)
	}
	fork := p.ForkTools().(*permittedTools)
	if voice, profile = fork.SelectedVoiceIdentity(); voice != "" || profile != "" {
		t.Fatal("another conversation inherited selected voice")
	}
	p.ResetMedia()
	if voice, profile = p.SelectedVoiceIdentity(); voice != "" || profile != "" {
		t.Fatal("new call inherited stale voice selection")
	}
}

func TestNamedRVCResultRequiresBackendRecipeAndVerifiedConfig(t *testing.T) {
	spec := &songSpec{Engine: "rvc", VoiceProfile: "selected-profile", ReferenceHash: "original-hash"}
	status := map[string]any{"recipe": map[string]any{"voice_profile_id": "selected-profile", "reference_source_sha256": "original-hash"}, "singing_voice": map[string]any{"engine": "rvc", "profile": "model-label", "config_sha256": strings.Repeat("a", 64)}}
	if err := validateMusicVoiceEvidence(status, spec); err != nil {
		t.Fatal(err)
	}
	status["recipe"].(map[string]any)["voice_profile_id"] = "default"
	if err := validateMusicVoiceEvidence(status, spec); err == nil {
		t.Fatal("default backend result mislabeled as requested voice")
	}
	status["recipe"].(map[string]any)["voice_profile_id"] = "selected-profile"
	delete(status, "singing_voice")
	if err := validateMusicVoiceEvidence(status, spec); err == nil {
		t.Fatal("request recipe alone was called voice proof")
	}
}

func TestPersistentSpeechVoiceBindingSurvivesFollowupWithoutNamedText(t *testing.T) {
	f, p := trustedAgent(t)
	task := agentTask{ID: "speech-parent", Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, State: "running"}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	stale := f.manager.agent.list(task.Account, task.Thread)[0]
	p.trace.TaskID = task.ID
	if err := p.bindTaskSpeechVoice("selected-voice"); err != nil {
		t.Fatal(err)
	}
	input, err := f.manager.claimTaskResult(stale, time.Now(), nil, "")
	if err != nil || input == nil || input.ExpectedVoiceID != "selected-voice" {
		t.Fatal("late selection lost when claiming old worker snapshot", input, err)
	}
	if err := p.bindTaskSpeechVoice(""); err == nil {
		t.Fatal("followup default voice bypassed saved selection")
	}
	if err := p.bindTaskSpeechVoice("other-voice"); err == nil {
		t.Fatal("followup replaced saved voice")
	}
	if err := p.bindTaskSpeechVoice("selected-voice"); err != nil {
		t.Fatal(err)
	}
	p.trace.TaskID = ""
	p.trace.ExpectedVoiceID = "selected-voice"
	if err := p.bindTaskSpeechVoice(""); err == nil {
		t.Fatal("explicit input binding bypassed without a task")
	}
}

func TestTaskConstraintsPersistAndRejectIdentityOrRecipeReplacement(t *testing.T) {
	f, p := trustedAgent(t)
	task := agentTask{ID: "bound-task", Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, State: "waiting", Constraints: p.taskConstraints("rvc_cover", "voice", "profile", "asset"), MusicSpec: &songSpec{VoiceID: "voice", VoiceProfile: "profile", Reference: "asset"}}
	task.Constraints.Source = &companion.SourceRef{Account: task.Account, Thread: task.Thread, Sender: task.Sender, EventID: "source"}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	stored := f.manager.agent.list(task.Account, task.Thread)[0]
	changed := stored
	changed.Sender = "another-person"
	if err := f.manager.agent.task(changed); err == nil {
		t.Fatal("requester replaced")
	}
	changed = stored
	changed.MusicSpec = &songSpec{VoiceProfile: "default", Reference: "asset"}
	if _, err := f.manager.agent.updateSnapshot(changed); err == nil {
		t.Fatal("named voice replaced")
	}
	reopened, err := newAgentControl(f.manager.agent.dir)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.list(task.Account, task.Thread)[0]
	if got.Constraints == nil || got.Constraints.Source.EventID != "source" || got.Constraints.VoiceProfile != "profile" {
		t.Fatal(got)
	}
	input := taskResultInput(got, &companion.Input{})
	if input.Source == nil || input.Source.EventID != "source" || input.ExpectedVoiceID != "voice" || input.ExpectedVoiceProfile != "profile" {
		t.Fatal(input)
	}
}

func TestRevokedOwnerCannotClaimSpecializedContinuation(t *testing.T) {
	f, p := trustedAgent(t)
	f.manager.mu.Lock()
	f.manager.config.Companion.Enabled = true
	f.manager.mu.Unlock()
	settings := f.manager.agent.config()
	settings.Enabled = true
	settings.TasksEnabled = true
	if err := f.manager.agent.save(settings); err != nil {
		t.Fatal(err)
	}
	task := agentTask{ID: "revoked-music", Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, State: "waiting", Next: time.Now().Add(-time.Second), OwnerRequested: true, MusicJob: "never-poll", Constraints: p.taskConstraints("music", "", "", "")}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	settings = f.manager.agent.config()
	settings.OwnerIDs = nil
	if err := f.manager.agent.save(settings); err != nil {
		t.Fatal(err)
	}
	in, err := f.manager.claimAgentTask(time.Now(), func(string) bool { return false })
	if err != nil || in != nil {
		t.Fatal(in, err)
	}
	stored := f.manager.agent.list(task.Account, task.Thread)[0]
	if stored.State != "needs_review" {
		t.Fatal(stored.State)
	}
}

func TestForgottenSourceStopsSpecializedWorkerAndDirectResultClaim(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(fmt.Sprint(direct), func(t *testing.T) {
			f, p := trustedAgent(t)
			observeOutboundSource(t, f, p)
			f.manager.mu.Lock()
			f.manager.config.Companion.Enabled = true
			f.manager.mu.Unlock()
			settings := f.manager.agent.config()
			settings.TasksEnabled = true
			if err := f.manager.agent.save(settings); err != nil {
				t.Fatal(err)
			}
			task := agentTask{ID: "source-bound", Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, State: "waiting", MusicJob: "must-not-poll", Next: time.Now().Add(-time.Second), Constraints: p.taskConstraints("music", "", "", "")}
			if err := f.manager.agent.task(task); err != nil {
				t.Fatal(err)
			}
			task = f.manager.agent.list(task.Account, task.Thread)[0]
			if err := f.manager.memory.Clear(companion.ScopeID(p.trace.Channel, p.trace.Account, p.trace.Thread)); err != nil {
				t.Fatal(err)
			}
			var in *companion.Input
			var err error
			if direct {
				in, err = f.manager.claimTaskResult(task, time.Now(), nil, "")
			} else {
				in, err = f.manager.claimAgentTask(time.Now(), func(string) bool { return false })
			}
			if err != nil || in != nil {
				t.Fatal(in, err)
			}
			stored := f.manager.agent.list(task.Account, task.Thread)[0]
			if stored.State != "needs_review" || !strings.Contains(stored.LastResult, "task_source_unavailable_or_changed") {
				t.Fatal(stored.State, stored.LastResult)
			}
		})
	}
}

func TestCapabilityInspectionIsReadOnlyAndDoesNotClaimExecution(t *testing.T) {
	f := setup(t)
	public := filepath.Join(f.manager.managedRoot(), "public_workspace")
	if err := os.MkdirAll(filepath.Join(public, "manifests"), 0700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(public, "entry.py")
	os.WriteFile(entry, []byte("raise RuntimeError('must never execute')"), 0600)
	manifest, _ := json.Marshal(map[string]any{"id": "synthetic", "name": "test", "entry": entry, "python": filepath.Join(public, "missing-python.exe"), "runtime_status": "validated_elsewhere"})
	os.WriteFile(filepath.Join(public, "manifests", "synthetic.json"), manifest, 0600)
	result, err := f.manager.InspectRegisteredCapabilities(t.Context())
	if err != nil || !result.ReadOnly || len(result.Capabilities) != 1 {
		t.Fatal(result, err)
	}
	row := result.Capabilities[0]
	if row.ResourceState != "unavailable" || row.ExecutionVerified || row.ClaimedValidation != "validated_elsewhere" {
		t.Fatal(row)
	}
	before, _ := os.ReadFile(entry)
	if string(before) != "raise RuntimeError('must never execute')" {
		t.Fatal("entry changed")
	}
}
