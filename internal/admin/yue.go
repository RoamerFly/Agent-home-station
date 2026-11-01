package admin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

func (p *permittedTools) yueTools() []mcptools.Tool {
	if p.manager.agent == nil || !p.manager.agent.config().Enabled || !p.manager.agent.config().TasksEnabled {
		return nil
	}
	if !p.manager.localMusicPluginConfigured(context.Background()) || p.trace.Preview || p.trace.Account != p.manager.native.Account() || !permittedRecipient(p.manager.Config().Config, p.trace.Thread) {
		return nil
	}
	perms := p.manager.Config().Companion.Permissions
	if !perms.SendVoice && !perms.SendFile {
		return nil
	}
	tools, err := p.manager.musicToolCatalog(context.Background())
	if err != nil {
		return nil
	}
	return tools
}
func (p *permittedTools) yueRequest(ctx context.Context, method, path string, body any, key string) ([]byte, string, error) {
	policy, err := p.newLocalMusicPluginPolicy(ctx, method, path, body, key)
	if err != nil {
		return nil, "", err
	}
	data, mime, _, err := policy.invoke(ctx)
	return data, mime, err
}

type yueHealth struct {
	SingingVoiceProfile string `json:"singing_voice_profile,omitempty"`
	BridgeVersion       int    `json:"bridge_version"`
	State               string `json:"state"`
	Message             string `json:"message"`
	Ready               bool   `json:"ready"`
}

func (p *permittedTools) probeYuE(ctx context.Context) yueHealth {
	if !p.manager.localMusicPluginConfigured(ctx) {
		return yueHealth{State: "migration_required", Message: "请安装、授权并配置音乐插件的本地桥接；旧版语音配置不再直接授权音乐请求"}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	b, _, err := p.yueRequest(ctx, "GET", "/health", nil, "")
	if err != nil {
		if _, code := safeToolFailure(err); code == "yue_account_scope_mismatch" {
			return yueHealth{State: "account_scope_mismatch", Message: err.Error()}
		}
		if _, code := safeToolFailure(err); code == "yue_bridge_auth_failed" {
			return yueHealth{State: "bridge_auth_failed", Message: err.Error()}
		}
		if _, code := safeToolFailure(err); code == "yue_backend_unavailable" {
			return yueHealth{State: "backend_unavailable", Message: err.Error()}
		}
		return yueHealth{State: "bridge_unavailable", Message: err.Error()}
	}
	var reply struct {
		Ready               bool   `json:"ready"`
		Version             int    `json:"bridge_version"`
		SingingVoiceProfile string `json:"singing_voice_profile"`
	}
	if json.Unmarshal(b, &reply) != nil {
		return yueHealth{State: "invalid_health", Message: "YuE桥接健康响应无效"}
	}
	if !reply.Ready {
		return yueHealth{State: "backend_unavailable", Message: "YuE生成后端未就绪，未提交歌曲任务"}
	}
	message := "YuE桥接与ComfyUI生成后端均在线"
	if reply.SingingVoiceProfile != "" {
		message += "；原曲翻唱默认RVC角色声线：" + reply.SingingVoiceProfile
	}
	return yueHealth{BridgeVersion: reply.Version, SingingVoiceProfile: reply.SingingVoiceProfile, State: "ready", Message: message, Ready: true}
}
func (p *permittedTools) callYuE(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	if name == "music_rvc_start" {
		return p.startRVCCover(ctx, raw)
	}
	if name == "music_sampling_options" {
		data, _, err := p.yueRequest(ctx, "GET", "/parameters", nil, "")
		return string(data), err
	}
	if name == "music_service_status" {
		result, err := json.Marshal(p.probeYuE(ctx))
		return string(result), err
	}
	var in struct {
		Sampling             musicSampling `json:"sampling"`
		PlanningMode         string        `json:"planning_mode"`
		SingingVoice         string        `json:"singing_voice"`
		VocalGender          string        `json:"vocal_gender"`
		VocalGenderRequest   string        `json:"vocal_gender_request"`
		Mode                 string        `json:"mode"`
		ReferenceMode        string        `json:"reference_mode"`
		ReferenceStart       *float64      `json:"reference_start_seconds"`
		ReferenceSeconds     *float64      `json:"reference_seconds"`
		TextGuidance         *float64      `json:"text_guidance_scale"`
		MinimumDuration      *float64      `json:"min_duration_seconds"`
		LyricEvidence        []string      `json:"lyrics_evidence_ids"`
		Seed                 *int64        `json:"seed"`
		Duration             int           `json:"duration_seconds"`
		Title, Style, Lyrics string
		Reference            string `json:"reference_asset_id"`
		ID                   string `json:"job_id"`
		RestartMissing       bool   `json:"restart_missing"`
		RestartFailed        bool   `json:"restart_failed"`
		Kind                 string
	}
	if json.Unmarshal(raw, &in) != nil {
		return "", toolInputError("YuE参数无效")
	}
	if name == "music_reference_inspect" {
		return p.inspectMusicReference(ctx, raw)
	}
	if name == "music_lyrics_compare" {
		return p.compareMusicLyrics(ctx, raw)
	}
	if name == "music_local_start" {
		if err := p.validateMusicSampling(ctx, in.Sampling, in.PlanningMode); err != nil {
			return "", err
		}
		if p.trace.TaskID != "" {
			for _, task := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
				if task.ID == p.trace.TaskID && task.MusicJob != "" {
					return "", toolInputError("这是已有歌曲的结果交付，请查询原任务，不重新生成")
				}
			}
		}
		if !p.manager.agent.config().TasksEnabled {
			return "", toolInputError("生成歌曲需启用Agent持续任务，以便完成后交付")
		}
		if in.SingingVoice == "" {
			in.SingingVoice = "native"
		}
		if in.SingingVoice != "native" && in.SingingVoice != "character" {
			return "", toolInputError("歌声模式应为native或character")
		}
		if in.VocalGender == "" {
			in.VocalGender = "female"
		}
		if in.VocalGender != "female" && in.VocalGender != "male" {
			return "", toolInputError("演唱性别应为female或male")
		}
		in.Style = normalizeSingingStyle(in.Style, in.VocalGender)
		in.Kind = p.musicDeliveryKind(in.Kind)
		if in.Duration == 0 {
			in.Duration = 60
		}
		if in.Duration < 10 || in.Duration > 600 {
			return "", toolInputError("歌曲时长须为10至600秒")
		}
		if !p.musicKindPermitted(in.Kind) {
			return "", toolInputError("歌曲发送格式未授权")
		}
		if in.Style == "" || in.Lyrics == "" || len(in.Style) > 2000 || len(in.Lyrics) > 6000 {
			return "", toolInputError("请提供风格和分段歌词")
		}
		in.Title = songTitle(in.Title)
		if in.Title == "" {
			return "", toolInputError("请根据用户歌曲语义自拟具体歌名，不能使用通用占位名称")
		}
		if in.Mode == "" {
			if in.Reference != "" {
				in.Mode = "cover"
			} else {
				in.Mode = "original"
			}
		}
		if in.Mode != "original" && in.Mode != "cover" {
			return "", toolInputError("歌曲模式应为original或cover")
		}
		requestText := p.singingRequestText()
		if err := validateSingingRequest(requestText, in.Mode, in.VocalGender, in.VocalGenderRequest); err != nil {
			return "", err
		}
		in.Style = singingGenerationStyle(in.Style, in.VocalGender, in.Mode)
		if in.Mode == "cover" && in.Reference == "" {
			return "", toolInputError("翻唱必须提供本会话真实原音频reference_asset_id；先读取引用素材或下载原曲，不能改编歌词冒充原曲")
		}
		if in.Mode == "original" && in.Reference != "" {
			return "", toolInputError("已提供参考音频，请使用cover模式明确按参考重生成")
		}

		if in.ReferenceMode == "" {
			in.ReferenceMode = "melody"
		}
		if in.ReferenceMode != "melody" && in.ReferenceMode != "full" {
			return "", toolInputError("参考模式只能为melody或full")
		}
		if in.Seed != nil && (*in.Seed < 0 || *in.Seed > 2147483647) {
			return "", toolInputError("种子范围应为0至2147483647")
		}
		// A missing seed selects a fresh take for a new request, but retries of
		// this same request keep the same seed and idempotency identity.
		if in.Seed == nil {
			canonical, _ := json.Marshal(map[string]any{"title": in.Title, "mode": in.Mode, "style": in.Style, "lyrics": in.Lyrics, "duration": in.Duration, "reference": in.Reference, "reference_mode": in.ReferenceMode, "sampling": in.Sampling, "planning_mode": in.PlanningMode, "reference_start_seconds": in.ReferenceStart, "reference_seconds": in.ReferenceSeconds, "singing_voice": in.SingingVoice, "vocal_gender": in.VocalGender, "kind": in.Kind})
			sum := sha256.Sum256(append([]byte(p.trace.Account+"|"+p.trace.Thread+"|"+p.trace.TraceID+"|"), canonical...))
			seed := int64(binary.LittleEndian.Uint32(sum[:4]) & 0x7fffffff)
			in.Seed = &seed
		}
		spec := &songSpec{Sampling: in.Sampling, PlanningMode: in.PlanningMode, Mode: in.Mode, Style: in.Style, Lyrics: in.Lyrics, Seed: *in.Seed, Duration: in.Duration, Reference: in.Reference, ReferenceMode: in.ReferenceMode, SingingVoice: in.SingingVoice, VocalGender: in.VocalGender, LyricEvidence: in.LyricEvidence}
		spec.MinimumDuration = float64(in.Duration) * .9
		spec.TextGuidance = 1
		if in.Mode == "cover" {
			spec.TextGuidance = 2
		}
		if in.TextGuidance != nil {
			spec.TextGuidance = *in.TextGuidance
		}
		if in.MinimumDuration != nil {
			spec.MinimumDuration = *in.MinimumDuration
		}
		if math.IsNaN(spec.TextGuidance) || math.IsInf(spec.TextGuidance, 0) || spec.TextGuidance < 0 || spec.TextGuidance > 100 || math.IsNaN(spec.MinimumDuration) || math.IsInf(spec.MinimumDuration, 0) || spec.MinimumDuration < 0 || spec.MinimumDuration > float64(in.Duration) {
			return "", toolInputError("text_guidance_scale须为0至100；min_duration_seconds须为0至目标时长，0表示允许短预览；本次未提交生成")
		}
		body := map[string]any{"title": in.Title, "mode": in.Mode, "style": in.Style, "lyrics": in.Lyrics, "duration_seconds": in.Duration, "seed": *in.Seed, "reference_mode": in.ReferenceMode, "singing_voice": in.SingingVoice, "vocal_gender": in.VocalGender}
		if len(in.Sampling) > 0 {
			body["sampling"] = in.Sampling
		}
		if in.PlanningMode != "" {
			body["planning_mode"] = in.PlanningMode
		}
		body["text_guidance_scale"], body["min_duration_seconds"] = spec.TextGuidance, spec.MinimumDuration
		var lyricTiming map[string]any

		if in.Reference != "" {
			a, data, err := p.referenceAsset(in.Reference)
			if err != nil {
				return "", err
			}
			spec.ReferenceHash = a.Hash
			lyricTiming = p.lyricTimingFor(a, in.LyricEvidence)
			if in.Mode == "cover" {
				if err := p.checkLyricEvidence(a, in.Lyrics, in.LyricEvidence); err != nil {
					return "", err
				}
				start, seconds, err := p.referenceWindow(a, in.LyricEvidence, in.Duration, in.ReferenceStart, in.ReferenceSeconds)
				if err != nil {
					return "", err
				}
				spec.ReferenceStart, spec.ReferenceSeconds = start, seconds
				body["reference_start_seconds"], body["reference_seconds"] = start, seconds
				clip, actual, err := prepareReferenceClip(ctx, p.manager.dir, data, start, seconds)
				if err != nil {
					return "", err
				}
				data = clip
				body["reference_pretrimmed"], body["reference_source_sha256"], body["reference_prepared_seconds"] = true, a.Hash, actual
			}
			body["reference_base64"] = base64.StdEncoding.EncodeToString(data)
		}
		raw, _ = json.Marshal(map[string]any{"title": in.Title, "style": in.Style, "lyrics": in.Lyrics, "mode": in.Mode, "kind": in.Kind, "duration_seconds": in.Duration, "seed": *in.Seed, "reference_asset_id": in.Reference, "reference_mode": in.ReferenceMode, "singing_voice": in.SingingVoice, "vocal_gender": in.VocalGender, "lyrics_evidence_ids": in.LyricEvidence, "reference_start_seconds": spec.ReferenceStart, "reference_seconds": spec.ReferenceSeconds})
		var operation map[string]any
		_ = json.Unmarshal(raw, &operation)
		operation["sampling"], operation["planning_mode"] = in.Sampling, in.PlanningMode
		operation["text_guidance_scale"], operation["min_duration_seconds"] = spec.TextGuidance, spec.MinimumDuration
		raw, _ = json.Marshal(operation)
		// Store the remote job mapping under the private effect ledger.
		// Probe without side effects before claiming a create operation. An offline
		// bridge must not become a fictitious successful/uncertain generation job.
		health := p.probeYuE(ctx)

		if health.Ready && health.BridgeVersion < 4 && in.SingingVoice != "character" && health.SingingVoiceProfile != "" {
			return "", yueError("yue_bridge_upgrade_required")
		}
		if health.Ready && health.BridgeVersion < 2 {
			return "", yueError("yue_bridge_upgrade_required")
		}
		if health.Ready && health.BridgeVersion < 8 && (len(in.Sampling) > 0 || in.PlanningMode != "") {
			return "", toolInputError("采样参数需YuE桥接v8；本次未提交生成")
		}
		if health.Ready && health.BridgeVersion < 7 {
			return "", toolInputError("当前YuE桥接尚不支持文本引导和实际时长验收；请升级到v7，避免把短成品当成完整歌曲；本次未提交生成")
		}
		if !health.Ready {
			if health.State == "account_scope_mismatch" {
				return "", yueError("yue_account_scope_mismatch")
			}
			if health.State == "backend_unavailable" {
				return "", yueError("yue_backend_unavailable")
			}
			if health.State == "bridge_auth_failed" {
				return "", yueError("yue_bridge_auth_failed")
			}
			return "", yueError("yue_bridge_unavailable")
		}
		result, callErr := p.privilegedCall(ctx, name, raw, func() (any, error) {
			data, _, e := p.yueRequest(ctx, "POST", "/jobs", body, p.operationID(name, raw))
			if e != nil {
				return nil, e
			}
			var result map[string]any
			e = json.Unmarshal(data, &result)
			if id, ok := result["id"].(string); ok {
				result["job_id"] = id
			}
			return result, e
		})
		if callErr != nil {
			return "", callErr
		}
		var job map[string]any
		if json.Unmarshal([]byte(result), &job) != nil {
			return "", toolInputError("歌曲任务响应无效")
		}
		for _, field := range []string{"effective_parameters", "recipe"} {
			if effective, ok := job[field].(map[string]any); ok {
				if values, ok := effective["sampling"]; ok {
					data, _ := json.Marshal(values)
					_ = json.Unmarshal(data, &spec.Sampling)
				}
				if value, ok := effective["planning_mode"].(string); ok {
					spec.PlanningMode = value
				}
			}
		}
		id, _ := job["job_id"].(string)
		if !validID(id) {
			return "", yueError("yue_submission_unknown")
		}
		taskID := agentID(p.trace.Account, p.trace.Thread, "yue", id)
		found := false
		for _, old := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
			found = found || old.ID == taskID
		}
		if !found {
			task := agentTask{MusicSpec: spec, ID: taskID, Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, Title: map[bool]string{true: "参考翻唱 · ", false: "原创歌曲 · "}[in.Mode == "cover"] + in.Title, MusicTitle: in.Title, RequestedText: companion.UserRequestText(p.trace.Text), Checkpoint: "歌曲任务已提交，等待真实结果；不重新生成", State: "waiting", Next: time.Now().Add(20 * time.Second), MusicDeadline: time.Now().Add(30 * time.Minute), MusicJob: id, MusicKind: in.Kind, OriginTrace: p.trace.TraceID, QuoteServerID: p.trace.QuoteServerID}
			task.OwnerRequested = p.privileged()
			task.Constraints = p.taskConstraints("music", "", spec.VoiceProfile, spec.Reference)
			if len(task.Title) > 200 {
				task.Title = "原创歌曲生成"
			}
			if e := p.manager.agent.task(task); e != nil {
				return "", e
			}
			p.manager.record(bot.Record{Event: "music_generation_queued", Category: "tool", ThreadID: p.trace.Thread, MessageID: p.trace.TraceID, Tool: name, Content: in.Style, Status: "waiting", Reason: "automatic_music_followup_registered", Origin: "agent"})
		}
		if p.trace.TaskID != "" {
			for _, parent := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
				if parent.ID == p.trace.TaskID && parent.RecoveryMessageID != "" {
					parent.State = "completed"
					parent.LastResult = "已提交真实生成任务并转交自动交付"
					if err := p.manager.agent.task(parent); err != nil {
						return "", err
					}
				}
			}
		}
		job["sampling"], job["planning_mode"] = spec.Sampling, spec.PlanningMode
		job["title"] = in.Title
		job["mode"], job["seed"], job["reference_asset_id"], job["reference_mode"] = in.Mode, *in.Seed, in.Reference, in.ReferenceMode
		job["reference_start_seconds"], job["reference_seconds"] = spec.ReferenceStart, spec.ReferenceSeconds
		job["lyric_timing_review"] = lyricTiming
		job["automatic_followup"] = true
		job["task_id"] = taskID
		job["instruction"] = "已真实提交本地YuE，后续检查与交付已自动持久化。可以自然告知正在制作；不要说已生成，不必另建任务或本轮反复查询。"
		data, _ := json.Marshal(job)
		return string(data), nil
	}
	if !validID(in.ID) {
		return "", toolInputError("任务ID无效")
	}
	authorized := false
	p.manager.agent.mu.Lock()
	for _, op := range p.manager.agent.data.Operations {
		if op.Thread != p.trace.Thread || op.Tool != "music_local_start" && op.Tool != "music_rvc_start" {
			continue
		}
		var result struct {
			ID string `json:"id"`
		}
		if json.Unmarshal([]byte(op.Result), &result) == nil && result.ID == in.ID {
			authorized = true
			break
		}
	}
	p.manager.agent.mu.Unlock()
	// Administrator-adopted completed jobs have a verified scoped task rather
	// than a model-created operation. The persisted account AND room still bind
	// access; a job ID by itself never grants another conversation access.
	if !authorized {
		for _, task := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
			if task.MusicJob == in.ID && task.RecoveryMessageID != "" && task.MusicSpec != nil {
				authorized = true
				break
			}
		}
	}
	if !authorized {
		return "", yueError("yue_job_scope_denied")
	}
	data, _, e := p.yueRequest(ctx, "GET", "/jobs/"+in.ID, nil, "")
	if e != nil {
		return "", e
	}
	var status map[string]any
	if json.Unmarshal(data, &status) != nil || name == "music_local_resume" && status["id"] != in.ID {
		return "", toolInputError("原音乐任务身份或状态响应无效")
	}
	if name == "music_local_resume" {
		return p.resumeMusicCompute(ctx, in.ID, status, in.RestartMissing, in.RestartFailed)
	}
	if status["state"] != "completed" {
		if (status["engine"] == "rvc" || status["engine"] == "yue") && (status["state"] == "failed" || status["state"] == "needs_review") {
			status["recovery_tool"] = "music_local_resume"
			status["recovery_instruction"] = "先核查实际失败原因，再用music_local_resume继续原job_id；YuE优先接回已提交任务，后端确认丢失时可选择restart_missing，已确认执行失败且修复原因后可选择restart_failed，均沿用原配方和歌曲编号。恢复不会自行发送微信。"
		}
		if status["state"] == "queued" || status["state"] == "running" {
			for _, task := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
				if task.MusicJob == in.ID && (task.State == "waiting" || task.State == "running" || task.State == "pending") {
					status["automatic_followup"] = true
					status["job_id"] = in.ID
				}
			}
		}
		data, _ = json.Marshal(status)
		return string(data), nil
	}
	in.Kind = p.musicDeliveryKind(in.Kind)
	if !p.musicKindPermitted(in.Kind) {
		return "", toolInputError("发送格式未授权")
	}
	audio, typ, e := p.yueRequest(ctx, "GET", "/jobs/"+in.ID+"/audio", nil, "")
	if e != nil {
		return "", e
	}
	title := ""
	var spec *songSpec
	for _, t := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
		if t.MusicJob == in.ID {
			title = t.MusicTitle
			spec = t.MusicSpec
			break
		}
	}
	if err := validateMusicVoiceEvidence(status, spec); err != nil {
		return "", err
	}
	var listening map[string]any
	var listeningAuthority func(context.Context) error
	if spec != nil && spec.VocalGender != "" {
		listening, listeningAuthority = p.reviewSongAudio(ctx, audio, spec.VocalGender, in.ID, spec, in.Kind)
	} else {
		origin, err := p.newMusicAudioOrigin(ctx, audio, "", in.ID, spec, in.Kind)
		if err != nil {
			return "", err
		}
		listeningAuthority = origin.Check
	}
	if listeningAuthority != nil {
		if err := listeningAuthority(ctx); err != nil {
			return "", err
		}
		previousAuthority := ctx
		ctx = withOutputAuthority(ctx, func(current context.Context) error {
			if err := checkOutputAuthority(previousAuthority); err != nil {
				return err
			}
			return listeningAuthority(current)
		})
	}
	result, e := p.stageSongOutputSpec(ctx, audio, typ, title, in.Kind, spec)
	if e == nil {
		var staged map[string]any
		if json.Unmarshal([]byte(result), &staged) == nil {
			staged["generation_result"] = map[string]any{"actual_seconds": status["duration_seconds"], "requested_seconds": status["requested_seconds"], "quality": status["quality"], "audio_observation": listening, "instruction": "按实测时长及实际音频观察判断交付；vocal_gender是提交要求，不能作为实际女声的证明。若听感明显不符，保留原件并决定后续处理，不把不符成品说成成功。"}
			b, _ := json.Marshal(staged)
			result = string(b)
		}
	}
	if e == nil && p.trace.TaskID != "" {
		for _, task := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
			if task.ID == p.trace.TaskID && task.MusicJob == in.ID {
				task.MusicPrepared = true
				if saveErr := p.manager.agent.task(task); saveErr != nil {
					return "", saveErr
				}
			}
		}
	}
	return result, e
}

// Requested song completion is a reply continuation, not an unsolicited greeting.
// Poll read-only state before invoking the model; never resubmit generation.
func (m *Manager) claimMusicTask(task agentTask, now time.Time) (*companion.Input, error) {
	previousResult := task.LastResult
	p := &permittedTools{manager: m, trace: *taskResultInput(task, &companion.Input{Channel: "wechat", Account: task.Account, Thread: task.Thread, Sender: task.Sender, TraceID: task.OriginTrace, TaskID: task.ID})}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	data, _, err := p.yueRequest(ctx, "GET", "/jobs/"+task.MusicJob, nil, "")
	var status struct {
		State, Reason string
		Duration      float64 `json:"duration_seconds"`
		Requested     float64 `json:"requested_seconds"`
	}
	if err == nil && json.Unmarshal(data, &status) != nil {
		err = errors.New("歌曲状态响应无效")
	}
	if !task.MusicDeadline.IsZero() && !now.Before(task.MusicDeadline) {
		err = nil
		status.State = "deadline_exceeded"
	}
	if err != nil || status.State == "queued" || status.State == "running" {
		task.Next = now.Add(20 * time.Second)
		task.LastResult = "本地模型正在排队或生成，完成后交付原会话，不重新生成"
		if err != nil {
			description, code := safeToolFailure(err)
			if description == "" {
				description = "本地歌曲状态读取失败，稍后核对同一任务，不重新生成"
				code = "yue_status_unavailable"
			}
			task.LastResult = description
			if task.LastResult != previousResult {
				m.record(bot.Record{Event: "music_status_unavailable", Category: "error", ThreadID: task.Thread, MessageID: task.OriginTrace, Tool: "music_local_status", ToolCode: code, Content: description, Status: "waiting", Origin: "agent"})
			}
		}
		_, err := m.agent.updateSnapshot(task)
		return nil, err
	}
	next := "调用music_local_status取得真实附件和generation_result，kind=" + task.MusicKind + "；根据实测时长和音频观察决定交付，保留原job_id，不重新生成。"
	if status.State != "completed" {
		task.MusicFailure = true
		task.MusicPrepared = true
		next = "任务已终止，根据真实原因检查服务或参数并验证；保留原任务和音频，不把未达要求成品说成完整。原RVC任务修复后可用music_local_resume继续同一个job_id，不能新建重复歌曲；YuE任务先核查原后端执行记录。"
	}
	facts := map[string]any{"music_job": task.MusicJob, "title": task.Title, "state": status.State, "reason": status.Reason, "actual_seconds": status.Duration, "requested_seconds": status.Requested}
	in, err := m.claimTaskResult(task, now, facts, next)
	if err != nil || in == nil {
		return nil, err
	}
	m.record(bot.Record{Event: "music_generation_finished", Category: "tool", ThreadID: task.Thread, MessageID: task.OriginTrace, Status: status.State, Reason: "music_result_ready_for_delivery", Origin: "agent"})
	return in, nil
}
