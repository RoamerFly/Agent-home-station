package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/pluginhost"
)

type musicAudioWindow struct {
	Offset  float64 `json:"offset_seconds"`
	Seconds float64 `json:"seconds"`
}

// A generated recording belongs to its persisted music task. It is not an
// incoming message or a manufactured media asset. Progress fields may change;
// requester, recipe, source and cancellation authority may not.
type musicAudioOrigin struct {
	p                                                            *permittedTools
	task                                                         agentTask
	audio                                                        []byte
	audioHash, taskVersion, authorization, epoch, expected, kind string
	sources                                                      map[string]int
	refs                                                         []musicAudioSource
}

type musicAudioSource struct {
	ref    companion.SourceRef
	sender string
}

func musicAudioTaskVersion(task agentTask) string {
	raw, _ := json.Marshal([]any{task.ID, task.Account, task.Thread, task.Sender, task.MusicJob, task.MusicTitle, task.MusicKind, task.MusicSpec, task.Constraints, task.OwnerRequested})
	return agentID(string(raw))
}

func musicAudioExpectedGender(spec *songSpec) string {
	if spec == nil {
		return ""
	}
	return spec.VocalGender
}

func (p *permittedTools) musicAudioVoiceMatches(task agentTask) bool {
	voice, profile := p.SelectedVoiceIdentity()
	id, selectedProfile := "", ""
	if task.MusicSpec != nil {
		id, selectedProfile = task.MusicSpec.VoiceID, task.MusicSpec.VoiceProfile
	}
	if voice != "" && voice != id || profile != "" && profile != selectedProfile {
		return false
	}
	return task.Constraints == nil || (task.Constraints.VoiceID == "" || task.Constraints.VoiceID == id) && (task.Constraints.VoiceProfile == "" || task.Constraints.VoiceProfile == selectedProfile)
}

func (p *permittedTools) musicAudioAuthorization(kind string) string {
	if p == nil || p.manager == nil || !p.servicePluginsAllowed() || p.trace.Channel != "wechat" || p.trace.Surf || p.manager.draining() || !p.manager.agentTaskActive(p.trace) || (kind != "voice" && kind != "file" && kind != "both") || !p.musicKindPermitted(kind) {
		return ""
	}
	// Persistent continuations follow claimAgentTask, independently of the
	// reactive bot's Config.Enabled switch. Explicit scoped reads have no
	// scheduler TaskID and retain their existing requester/task proof instead.
	if p.trace.TaskID != "" && (!p.manager.agent.config().TasksEnabled || !p.manager.Config().Companion.Enabled) {
		return ""
	}
	if strings.HasSuffix(p.trace.Thread, "@chatroom") && !p.manager.Config().Companion.Permissions.ObserveGroups {
		return ""
	}
	voice, profile := p.SelectedVoiceIdentity()
	raw, _ := json.Marshal([]any{p.trace.Account, p.trace.Channel, p.trace.Thread, p.trace.Sender, p.trace.TaskID, p.trace.TraceID, p.trace.Source, p.trace.ReplyContext, voice, profile, p.privileged(), kind, p.manager.Config().Companion.Permissions.SendVoice, p.manager.Config().Companion.Permissions.SendFile})
	return agentID(string(raw))
}

func (p *permittedTools) newMusicAudioOrigin(ctx context.Context, audio []byte, expected, jobID string, spec *songSpec, kind string) (*musicAudioOrigin, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	authorization := p.musicAudioAuthorization(kind)
	if authorization == "" || jobID == "" || !validID(jobID) || expected != musicAudioExpectedGender(spec) {
		return nil, errors.New("music_audio_origin_not_authorized")
	}
	if len(audio) == 0 || len(audio) > 64<<20 {
		return nil, errors.New("music_audio_source_limit_64mib")
	}
	var selected *agentTask
	for _, task := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
		if task.MusicJob != jobID || p.trace.TaskID != "" && task.ID != p.trace.TaskID {
			continue
		}
		if selected != nil {
			return nil, errors.New("music_audio_origin_ambiguous_task")
		}
		copy := copyTask(task)
		selected = &copy
	}
	if selected == nil || selected.State == "cancelled" || selected.Sender != p.trace.Sender && !p.privileged() || p.manager.validateTaskSource(*selected) != nil {
		return nil, errors.New("music_audio_origin_task_not_authorized")
	}
	want, _ := json.Marshal(spec)
	stored, _ := json.Marshal(selected.MusicSpec)
	if string(want) != string(stored) {
		return nil, errors.New("music_audio_origin_recipe_changed")
	}
	if !p.musicAudioVoiceMatches(*selected) {
		return nil, errors.New("music_audio_origin_voice_changed")
	}
	epoch, err := p.manager.memory.RecallEpoch(ctx)
	if err != nil {
		return nil, err
	}
	copyAudio := append([]byte(nil), audio...)
	sum := sha256.Sum256(copyAudio)
	o := &musicAudioOrigin{p: p, task: *selected, audio: copyAudio, audioHash: hex.EncodeToString(sum[:]), taskVersion: musicAudioTaskVersion(*selected), authorization: authorization, epoch: epoch, expected: expected, kind: kind, sources: map[string]int{}}
	addSource := func(source *companion.SourceRef, sender string) error {
		if source == nil {
			return nil
		}
		if source.Integrity == "conflicted" || source.Integrity == "quarantined" || source.Account != "" && source.Account != p.trace.Account || source.Thread != "" && source.Thread != p.trace.Thread || source.Sender != "" && source.Sender != sender {
			return errors.New("music_audio_origin_source_changed")
		}
		if source.EventID == "" {
			return nil
		}
		if source.Revision < 1 {
			return errors.New("music_audio_origin_source_changed")
		}
		if old, ok := o.sources[source.EventID]; ok && old != source.Revision {
			return errors.New("music_audio_origin_source_changed")
		}
		o.sources[source.EventID] = source.Revision
		o.refs = append(o.refs, musicAudioSource{ref: *source, sender: sender})
		return nil
	}
	addVersions := func(reply *companion.ReplyContext) error {
		if reply == nil {
			return nil
		}
		for id, revision := range reply.SourceVersions {
			if id == "" || revision < 1 {
				return errors.New("music_audio_origin_source_changed")
			}
			if old, ok := o.sources[id]; ok && old != revision {
				return errors.New("music_audio_origin_source_changed")
			}
			o.sources[id] = revision
		}
		return nil
	}
	if selected.Constraints != nil {
		if err = addSource(selected.Constraints.Source, selected.Sender); err != nil {
			return nil, err
		}
		if err = addVersions(selected.Constraints.ReplyContext); err != nil {
			return nil, err
		}
	}
	if err = addSource(p.trace.Source, p.trace.Sender); err != nil {
		return nil, err
	}
	if err = addVersions(p.trace.ReplyContext); err != nil {
		return nil, err
	}
	if err = o.Check(ctx); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *musicAudioOrigin) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if o.p.musicAudioAuthorization(o.kind) != o.authorization {
		return errors.New("music_audio_origin_authorization_changed")
	}
	current, ok := o.p.manager.agent.lookupTask(o.task.ID, o.task.Account, o.task.Thread)
	if !ok || current.State == "cancelled" || musicAudioTaskVersion(current) != o.taskVersion || current.Sender != o.p.trace.Sender && !o.p.privileged() || o.p.trace.TaskID != "" && o.p.trace.TaskID != current.ID || o.p.manager.validateTaskSource(current) != nil {
		return errors.New("music_audio_origin_task_changed")
	}
	if musicAudioExpectedGender(current.MusicSpec) != o.expected || !o.p.musicAudioVoiceMatches(current) {
		return errors.New("music_audio_origin_voice_changed")
	}
	epoch, err := o.p.manager.memory.RecallEpoch(ctx)
	if err != nil || epoch != o.epoch {
		return errors.New("music_audio_origin_source_changed")
	}
	scope := companion.ScopeID("wechat", o.task.Account, o.task.Thread)
	for id, revision := range o.sources {
		event, err := o.p.manager.memory.ReadEvent(ctx, []string{scope}, id)
		if err != nil || event.Revision != revision {
			return errors.New("music_audio_origin_source_changed")
		}
		for _, source := range o.refs {
			if source.ref.EventID == id && (event.Role != "user" || event.Sender != source.sender || source.ref.BodyHash != "" && source.ref.BodyHash != event.Hash || source.ref.MessageID != "" && source.ref.MessageID != event.Trace) {
				return errors.New("music_audio_origin_requester_changed")
			}
		}
	}
	return nil
}

const musicAudioSamplePrompt = "这段音频由同一首歌前、中、后三处各最多15秒片段按顺序拼接。仅依据实际提供的样本，判断各段主唱是男声、女声、混声还是无人声，是否为歌唱，是否明显失真。不要猜测目标、歌手或声纹身份，不要输出歌词。给出简短中文结论和置信度；无法确认须明确说明。这是抽样的模型意见，不是全曲人工验收或声线认证。"

func (o *musicAudioOrigin) observationPolicy(ctx context.Context, wav []byte, windows []musicAudioWindow) (*mediaAudioObservationPolicy, error) {
	if err := o.Check(ctx); err != nil {
		return nil, err
	}
	if len(windows) != 3 {
		return nil, errors.New("music_audio_sample_windows_invalid")
	}
	for i, window := range windows {
		if math.IsNaN(window.Offset) || math.IsInf(window.Offset, 0) || math.IsNaN(window.Seconds) || math.IsInf(window.Seconds, 0) || window.Offset < 0 || window.Seconds <= 0 || window.Seconds > 15 || i > 0 && window.Offset < windows[i-1].Offset {
			return nil, errors.New("music_audio_sample_windows_invalid")
		}
	}
	windowJSON, err := json.Marshal(windows)
	if err != nil {
		return nil, errors.New("music_audio_sample_windows_invalid")
	}
	source := "music-job:" + o.task.MusicJob + ":" + agentID(o.taskVersion, o.audioHash, string(windowJSON), o.expected, o.kind)
	sources := map[string]int{source: 1}
	for id, revision := range o.sources {
		sources[id] = revision
	}
	inv := pluginhost.Invocation{Account: o.task.Account, Thread: o.task.Thread, Requester: o.p.trace.Sender, Purpose: "observe generated song samples", RequestKey: source + ":" + agentID(o.authorization, o.epoch), SourceVersions: sources, EvidenceEpoch: o.epoch, Authorization: o.authorization}
	return o.p.manager.newBoundMediaAudioObservationPolicy(ctx, wav, inv, 1, o.Check, musicAudioSamplePrompt, 0, 90)
}
