package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

// Direct conversion consumes an actual cached recording. It never rewrites
// lyrics or submits a YuE prompt. Delivery uses the existing durable song path.
func (p *permittedTools) startRVCCover(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Title        string   `json:"title"`
		Reference    string   `json:"reference_asset_id"`
		Start        *float64 `json:"reference_start_seconds"`
		Seconds      *float64 `json:"reference_seconds"`
		Kind         string   `json:"kind"`
		VoiceProfile string   `json:"voice_profile_id"`
		VoiceID      string   `json:"voice_id"`
	}
	if json.Unmarshal(raw, &in) != nil || songTitle(in.Title) == "" || in.Reference == "" {
		return "", toolInputError("RVC翻唱需要具体title和本会话的原录音reference_asset_id")
	}
	voices, voiceErr := p.accessibleVoices()
	if voiceErr != nil {
		return "", voiceErr
	}
	// A remembered/requested name is not a model selection. Require an actual
	// registered identity when a known voice is named; never infer a new model
	// from the song title and silently run the character's default weights.
	request := p.trace.RequestedText
	if request == "" {
		request = companion.UserRequestText(p.trace.Text)
	}
	if err := validateRequestedRVCVoice(request+" "+in.Title, in.VoiceID, in.VoiceProfile, voices); err != nil {
		return "", err
	}
	if in.VoiceID != "" {
		voices, e := p.accessibleVoices()
		if e != nil {
			return "", e
		}
		found := false
		for _, v := range voices {
			if v.ID != in.VoiceID {
				continue
			}
			found = true
			if !strings.Contains(strings.ToLower(v.Engine), "rvc") || v.ProfileID == "" {
				return "", toolInputError("selected_voice_engine_not_rvc: " + v.Engine + "；已能访问声线，但需对应的歌声转换模型，未提交默认声线替代")
			}
			if in.VoiceProfile != "" && in.VoiceProfile != v.ProfileID {
				return "", toolInputError("voice_id_profile_mismatch")
			}
			in.VoiceProfile = v.ProfileID
		}
		if !found {
			return "", toolInputError("selected_voice_not_found_or_not_authorized")
		}
	}
	if p.trace.TaskID != "" {
		for _, task := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
			if task.ID == p.trace.TaskID && task.MusicJob != "" {
				return "", toolInputError("已有歌曲任务请查询原job_id，不重新转换")
			}
		}
	}
	if in.VoiceProfile != "" {
		if strings.HasPrefix(in.VoiceProfile, "private-") && !p.privileged() {
			return "", toolInputError("private_voice_requires_verified_owner")
		}
		voices, e := p.accessibleVoices()
		if e != nil {
			return "", e
		}
		known := false
		for _, v := range voices {
			known = known || v.ProfileID == in.VoiceProfile && strings.Contains(strings.ToLower(v.Engine), "rvc")
		}
		if !known {
			return "", toolInputError("voice_profile_not_registered_use_voice_library_list_and_read")
		}
	}
	if err := p.bindSelectedVoice(in.VoiceID, in.VoiceProfile); err != nil {
		return "", err
	}
	in.Title = songTitle(in.Title)
	in.Kind = p.musicDeliveryKind(in.Kind)
	if !p.musicKindPermitted(in.Kind) {
		return "", toolInputError("翻唱发送格式未授权")
	}
	asset, data, err := p.referenceAsset(in.Reference)
	if err != nil {
		return "", err
	}
	duration, err := probeRVCAudioDuration(ctx, p.manager.dir, data)
	if err != nil {
		return "", err
	}
	start, seconds := 0.0, duration
	if in.Start != nil {
		start = *in.Start
		seconds = duration - start
	}
	if in.Seconds != nil {
		seconds = *in.Seconds
	}
	if math.IsNaN(start) || math.IsInf(start, 0) || start < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds > 600 || start+seconds > duration+.1 {
		return "", toolInputError("RVC范围须位于原录音内，当前单段处理不超过600秒；长曲可显式分段转换，不静默缩短")
	}
	clip, actual, err := prepareReferenceClip(ctx, p.manager.dir, data, start, seconds)
	if err != nil {
		return "", err
	}
	health, _, err := p.yueRequest(ctx, "GET", "/rvc-health", nil, "")
	if err != nil {
		return "", err
	}
	body, err := p.prepareRVCRequest(ctx, health, in.Title, in.VoiceProfile, asset.Hash, start, seconds, clip)
	if err != nil {
		return "", err
	}
	operationData := map[string]any{"title": in.Title, "reference_asset_id": in.Reference, "reference_sha256": asset.Hash, "reference_start_seconds": start, "reference_seconds": seconds, "kind": in.Kind, "engine": "rvc"}
	if in.VoiceProfile != "" {
		operationData["voice_profile_id"] = in.VoiceProfile
	}
	operation, _ := json.Marshal(operationData)
	result, err := p.privilegedCall(ctx, "music_rvc_start", operation, func() (any, error) {
		data, _, e := p.yueRequest(ctx, "POST", "/rvc-jobs", body, p.operationID("music_rvc_start", operation))
		if e != nil {
			return nil, e
		}
		var reply map[string]any
		e = json.Unmarshal(data, &reply)
		if id, ok := reply["id"].(string); ok {
			reply["job_id"] = id
		}
		return reply, e
	})
	if err != nil {
		return "", err
	}
	var job map[string]any
	if json.Unmarshal([]byte(result), &job) != nil {
		return "", toolInputError("RVC任务响应无效")
	}
	id, _ := job["job_id"].(string)
	if !validID(id) {
		return "", yueError("yue_submission_unknown")
	}
	taskID := agentID(p.trace.Account, p.trace.Thread, "yue", id)
	found := false
	for _, task := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
		found = found || task.ID == taskID
	}
	if !found {
		spec := &songSpec{VoiceID: in.VoiceID, VoiceProfile: in.VoiceProfile, Engine: "rvc", Mode: "cover", SingingVoice: "character", VocalGender: "female", Duration: int(math.Ceil(actual)), Reference: asset.ID, ReferenceHash: asset.Hash, ReferenceStart: start, ReferenceSeconds: seconds, Style: "RVC原曲人声换声；保留原词、旋律、节奏与伴奏"}
		task := agentTask{ID: taskID, Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, MusicSpec: spec, MusicTitle: in.Title, MusicJob: id, MusicKind: in.Kind, OriginTrace: p.trace.TraceID, QuoteServerID: p.trace.QuoteServerID, Title: "RVC翻唱 · " + in.Title, RequestedText: companion.UserRequestText(p.trace.Text), Checkpoint: "查询原RVC任务结果，不重写歌词、不重生成旋律、不重复转换", State: "waiting", Next: time.Now().Add(20 * time.Second)}
		task.OwnerRequested = p.privileged()
		task.Constraints = p.taskConstraints("rvc_cover", in.VoiceID, in.VoiceProfile, in.Reference)
		if err := p.manager.agent.task(task); err != nil {
			return "", err
		}
		p.manager.record(bot.Record{Event: "music_generation_queued", Category: "tool", ThreadID: p.trace.Thread, MessageID: p.trace.TraceID, Tool: "music_rvc_start", Content: in.Title, Status: "waiting", Reason: "rvc_direct_conversion_followup_registered", Origin: "agent"})
	}
	job["task_id"], job["automatic_followup"], job["engine"], job["voice_profile_id"] = taskID, true, "rvc", in.VoiceProfile
	job["instruction"] = "已提交原录音直接换声，分离人声→RVC→混回原伴奏；没有调用YuE作曲。完成后查询music_local_status取得完整附件并按当前需求发送；以实际音频检验音色，不重复提交。"
	output, _ := json.Marshal(job)
	return string(output), nil
}

// The package owns bridge negotiation and request construction. Only the host
// can materialize this exact audio reference, and it rejects any change to the
// authorized title, selected voice, source hash or requested recording range.
func (p *permittedTools) prepareRVCRequest(ctx context.Context, health []byte, title, profile, sourceHash string, start, seconds float64, clip []byte) (any, error) {
	var observed map[string]json.RawMessage
	if json.Unmarshal(health, &observed) != nil {
		return nil, toolInputError("rvc_health_invalid")
	}
	safeHealth := map[string]json.RawMessage{}
	for _, name := range []string{"ready", "bridge_version", "voice_profiles", "private_voice_profiles"} {
		if value, ok := observed[name]; ok {
			safeHealth[name] = value
		}
	}
	ref := localMusicBodyRef{Ref: "reference_0", SHA256: namedSpeechSHA(clip), Bytes: int64(len(clip))}
	var prepared struct {
		Method string `json:"method"`
		Path   string `json:"path"`
		Body   struct {
			Title     string          `json:"title"`
			Reference json.RawMessage `json:"reference_base64"`
			Hash      string          `json:"reference_source_sha256"`
			Start     float64         `json:"reference_start_seconds"`
			Seconds   float64         `json:"reference_seconds"`
			Precut    bool            `json:"reference_pretrimmed"`
			Profile   string          `json:"voice_profile_id,omitempty"`
		} `json:"body"`
	}
	input := map[string]any{"action": "rvc_request", "health": safeHealth, "title": title, "voice_profile_id": profile, "reference_sha256": sourceHash, "reference_start_seconds": start, "reference_seconds": seconds, "audio_ref": ref}
	err := p.prepareMusic(ctx, "/rvc-health", input, &prepared, func() error {
		var reference struct {
			Ref localMusicBodyRef `json:"$music_ref"`
		}
		b := prepared.Body
		if prepared.Method != "POST" || prepared.Path != "/rvc-jobs" {
			return errors.New("music_preparation_route_changed")
		}
		if b.Title != title || b.Profile != profile {
			return errors.New("music_preparation_voice_or_title_changed")
		}
		if b.Hash != sourceHash {
			return errors.New("music_preparation_source_changed")
		}
		if b.Start != start || b.Seconds != seconds || !b.Precut {
			return errors.New("music_preparation_range_changed")
		}
		if musicStrictJSON(b.Reference, &reference) != nil || reference.Ref != ref {
			return errors.New("music_preparation_audio_reference_changed")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	prepared.Body.Reference, _ = json.Marshal(base64.StdEncoding.EncodeToString(clip))
	return prepared.Body, nil
}

func validateRequestedRVCVoice(request, voiceID, profileID string, voices []voiceEntry) error {
	named := []voiceEntry{}
	for _, v := range voices {
		if len([]rune(v.Name)) < 2 {
			continue
		}
		pattern := `(?i)(^|[^a-z0-9_])` + regexp.QuoteMeta(v.Name) + `($|[^a-z0-9_])`
		if regexp.MustCompile(pattern).MatchString(request) {
			named = append(named, v)
		}
	}
	if len(named) == 0 {
		return nil
	}
	for _, v := range named {
		if voiceID != "" && voiceID == v.ID || profileID != "" && profileID == v.ProfileID {
			return nil
		}
	}
	return toolInputError("named_voice_requires_matching_selection: 请求或标题指定了登记声线，请先voice_library_list/read，再传匹配的voice_id或voice_profile_id；没有兼容歌声模型时先准备模型，不提交默认声线冒充")
}

func probeRVCAudioDuration(ctx context.Context, root string, data []byte) (float64, error) {
	dir, err := os.MkdirTemp(root, "rvc-source-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	source := filepath.Join(dir, "source.audio")
	if err = os.WriteFile(source, data, 0600); err != nil {
		return 0, err
	}
	output, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", source).Output()
	seconds, parseErr := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if err != nil || parseErr != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return 0, toolInputError("无法读取原录音的实际时长；未提交RVC转换")
	}
	return seconds, nil
}
