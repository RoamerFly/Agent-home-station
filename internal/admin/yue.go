package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

func (p *permittedTools) yueTools() []mcptools.Tool {
	c := p.manager.speechConfig()
	if p.manager.agent == nil || !p.manager.agent.config().Enabled || !p.manager.agent.config().TasksEnabled {
		return nil
	}
	if !c.YuEEnabled || c.YuEEndpoint == "" || c.YuEToken == "" || p.trace.Preview || p.trace.Account != p.manager.native.Account() || !permittedRecipient(p.manager.Config().Config, p.trace.Thread) {
		return nil
	}
	perms := p.manager.Config().Companion.Permissions
	if !perms.SendVoice && !perms.SendFile {
		return nil
	}
	return []mcptools.Tool{
		agentTool("music_service_status", "Read actual YuE bridge and generation backend health without creating or retrying a song. Use this for local health; web_fetch is public-only. When offline, trusted owners can read shared project-yue.md and project startup docs, then use permitted server tools on the correct OS. Report diagnosis separately from verified repair. Input {}.", `{"type":"object","properties":{},"additionalProperties":false}`),
		agentTool("music_lyrics_compare", "Compare one actual ASR lyrics window with a version found on the web. First web_search for song/title/artist lyrics and web_fetch plausible versions; then supply asset_id, lyrics_evidence_id of ORIGINAL ASR, candidate_url and candidate_lyrics copied from that page. Reads the page with permitted web_fetch to verify real text. Aligns the corresponding passage, not the whole song. Only >=88% match and <=12%/12 edits permits minor correction, returning a new lyrics_evidence_id and exact lyrics. Large differences reject this version; compare another. Does not certify artist/version or human accuracy. Never replace uncertain ASR with invented text.", `{"type":"object","properties":{"asset_id":{"type":"string"},"lyrics_evidence_id":{"type":"string"},"candidate_url":{"type":"string","maxLength":2048},"candidate_lyrics":{"type":"string","maxLength":6000}},"required":["asset_id","lyrics_evidence_id","candidate_url","candidate_lyrics"],"additionalProperties":false}`),
		agentTool("music_local_start", "Generate with local YuE: ALL song creation and covering goes through YuE. mode=original writes a new composition; mode=cover preserves supplied source lyrics and uses matching source style plus actual reference audio, Native YuE female vocals by default. singing_voice=character uses RVC ONLY when the user explicitly asks for the character own voice; never infer this from the persona. vocal_gender=male only for an explicit male request. Requires lyrics_evidence_ids returned by music_reference_inspect and actual reference_asset_id from THIS conversation and supplied/verified lyrics, never invent replacement words for a cover. Reference workflow extracts melody/ABC and regenerates audio; it cannot preserve the exact backing track, timbre or guarantee faithful singing. reference_mode=melody (default) or full (melody+chords). Optional seed 0..2147483647 fixes a take; omit for a new seeded take. For exactly replaying a previous recording use media_reuse, not generation. Inspect source/previous parameters with music_reference_inspect. Supply title, style, lyrics and duration_seconds (10..600, default60), one minute=60. Automatically follows up; no extra agent_task. Default COMPLETE voice notes <=60 seconds each, both only if user also asks for file. Returns actual job_id and seed, not proof of listening or delivery.", `{"type":"object","properties":{"singing_voice":{"type":"string","enum":["native","character"]},"vocal_gender":{"type":"string","enum":["female","male"]},"title":{"type":"string","minLength":1,"maxLength":24},"mode":{"type":"string","enum":["original","cover"]},"style":{"type":"string"},"lyrics":{"type":"string"},"seed":{"type":"integer","minimum":0,"maximum":2147483647},"reference_mode":{"type":"string","enum":["melody","full"]},"duration_seconds":{"type":"integer","minimum":10,"maximum":600},"kind":{"type":"string","enum":["voice","file","both"]},"reference_asset_id":{"type":"string"},"lyrics_evidence_ids":{"type":"array","items":{"type":"string"},"maxItems":10}},"required":["title","style","lyrics"],"additionalProperties":false}`),
		agentTool("music_reference_inspect", "Inspect exact cached audio from THIS conversation before covering, rerolling or recovering a take. Returns submitted style/lyrics/seed when this is a known generated song. Optional transcribe=true calls configured ASR for <=60 seconds at offset_seconds; candidate transcript is uncertain, not guaranteed lyrics. Returns lyrics_evidence_id; pass it in music_local_start.lyrics_evidence_ids and copy the transcript exactly (section labels allowed). supplied_lyrics may register words literally provided in the current user message, never invented words. Always use YuE cover with original lyrics and a style faithful to the source unless the user requests a changed arrangement. Use the exact quoted asset over another newer take. If only an original recording is available, listen/transcribe rather than inventing lyrics; if user demands unchanged accompaniment explain that YuE resynthesizes. Input {asset_id,transcribe?,offset_seconds?,seconds?}.", `{"type":"object","properties":{"asset_id":{"type":"string"},"transcribe":{"type":"boolean"},"offset_seconds":{"type":"integer","minimum":0,"maximum":600},"seconds":{"type":"integer","minimum":1,"maximum":60},"supplied_lyrics":{"type":"string","maxLength":6000}},"required":["asset_id"],"additionalProperties":false}`),
		agentTool("music_local_status", "Read a local job owned by THIS conversation. Completed audio defaults to COMPLETE voice notes (each at most60 seconds), not a preview or file. User can request original file later via media_reuse. Never ask whether a file is wanted. Put real media[].id in final media_ids. No resubmission. Input {job_id:string,kind?:voice|file}.", `{"type":"object","properties":{"job_id":{"type":"string"},"kind":{"type":"string","enum":["file","voice","both"]}},"required":["job_id"],"additionalProperties":false}`),
	}
}
func (p *permittedTools) yueRequest(ctx context.Context, method, path string, body any, key string) ([]byte, string, error) {
	c := p.manager.speechConfig()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.YuEEndpoint, "/")+path, r)
	if e != nil {
		return nil, "", e
	}
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+c.YuEToken)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	client := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect denied") }}
	defer client.CloseIdleConnections()
	response, e := client.Do(req)
	if e != nil {
		return nil, "", yueError("yue_bridge_unavailable")
	}
	defer response.Body.Close()
	data, e := io.ReadAll(io.LimitReader(response.Body, (64<<20)+1))
	if e != nil || len(data) > 64<<20 {
		return nil, "", yueError("yue_response_limit")
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		if response.StatusCode == 401 || response.StatusCode == 403 {
			return nil, "", yueError("yue_bridge_auth_failed")
		}
		if response.StatusCode == 503 {
			return nil, "", yueError("yue_backend_unavailable")
		}
		if response.StatusCode == 404 {
			return nil, "", yueError("yue_job_not_found")
		}
		return nil, "", yueError("yue_request_rejected")
	}
	return data, response.Header.Get("Content-Type"), nil
}

type yueHealth struct {
	SingingVoiceProfile string `json:"singing_voice_profile,omitempty"`
	BridgeVersion       int    `json:"bridge_version"`
	State               string `json:"state"`
	Message             string `json:"message"`
	Ready               bool   `json:"ready"`
}

func (p *permittedTools) probeYuE(ctx context.Context) yueHealth {
	c := p.manager.speechConfig()
	if !c.YuEEnabled {
		return yueHealth{State: "disabled", Message: "本地YuE未启用"}
	}
	if c.YuEEndpoint == "" || c.YuEToken == "" {
		return yueHealth{State: "unconfigured", Message: "YuE桥接地址或令牌尚未配置"}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	b, _, err := p.yueRequest(ctx, "GET", "/health", nil, "")
	if err != nil {
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
		message += "；可选角色歌声已配置（仅明确指定时使用）：" + reply.SingingVoiceProfile
	}
	return yueHealth{BridgeVersion: reply.Version, SingingVoiceProfile: reply.SingingVoiceProfile, State: "ready", Message: message, Ready: true}
}
func (p *permittedTools) callYuE(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	if name == "music_service_status" {
		result, err := json.Marshal(p.probeYuE(ctx))
		return string(result), err
	}
	var in struct {
		SingingVoice         string   `json:"singing_voice"`
		VocalGender          string   `json:"vocal_gender"`
		Mode                 string   `json:"mode"`
		ReferenceMode        string   `json:"reference_mode"`
		LyricEvidence        []string `json:"lyrics_evidence_ids"`
		Seed                 *int64   `json:"seed"`
		Duration             int      `json:"duration_seconds"`
		Title, Style, Lyrics string
		Reference            string `json:"reference_asset_id"`
		ID                   string `json:"job_id"`
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
			canonical, _ := json.Marshal(map[string]any{"title": in.Title, "mode": in.Mode, "style": in.Style, "lyrics": in.Lyrics, "duration": in.Duration, "reference": in.Reference, "reference_mode": in.ReferenceMode, "singing_voice": in.SingingVoice, "vocal_gender": in.VocalGender, "kind": in.Kind})
			sum := sha256.Sum256(append([]byte(p.trace.Account+"|"+p.trace.Thread+"|"+p.trace.TraceID+"|"), canonical...))
			seed := int64(binary.LittleEndian.Uint32(sum[:4]) & 0x7fffffff)
			in.Seed = &seed
		}
		spec := &songSpec{Mode: in.Mode, Style: in.Style, Lyrics: in.Lyrics, Seed: *in.Seed, Duration: in.Duration, Reference: in.Reference, ReferenceMode: in.ReferenceMode, SingingVoice: in.SingingVoice, VocalGender: in.VocalGender, LyricEvidence: in.LyricEvidence}
		body := map[string]any{"title": in.Title, "mode": in.Mode, "style": in.Style, "lyrics": in.Lyrics, "duration_seconds": in.Duration, "seed": *in.Seed, "reference_mode": in.ReferenceMode, "singing_voice": in.SingingVoice, "vocal_gender": in.VocalGender}
		raw, _ = json.Marshal(map[string]any{"title": in.Title, "style": in.Style, "lyrics": in.Lyrics, "mode": in.Mode, "kind": in.Kind, "duration_seconds": in.Duration, "seed": *in.Seed, "reference_asset_id": in.Reference, "reference_mode": in.ReferenceMode, "singing_voice": in.SingingVoice, "vocal_gender": in.VocalGender, "lyrics_evidence_ids": in.LyricEvidence})
		if in.Reference != "" {
			a, data, err := p.referenceAsset(in.Reference)
			if err != nil {
				return "", err
			}
			if in.Mode == "cover" {
				if err := p.checkLyricEvidence(a, in.Lyrics, in.LyricEvidence); err != nil {
					return "", err
				}
			}
			body["reference_base64"] = base64.StdEncoding.EncodeToString(data)
		}
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
		if !health.Ready {
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
		job["title"] = in.Title
		job["mode"], job["seed"], job["reference_asset_id"], job["reference_mode"] = in.Mode, *in.Seed, in.Reference, in.ReferenceMode
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
		if op.Thread != p.trace.Thread || op.Tool != "music_local_start" {
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
	_ = json.Unmarshal(data, &status)
	if status["state"] != "completed" {
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
	result, e := p.stageSongOutputSpec(ctx, audio, typ, title, in.Kind, spec)
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
	p := &permittedTools{manager: m}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	data, _, err := p.yueRequest(ctx, "GET", "/jobs/"+task.MusicJob, nil, "")
	var status struct{ State, Reason string }
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
		return nil, m.agent.task(task)
	}
	if task.Runs >= 3 {
		task.State = "needs_review"
		task.LastResult = "歌曲交付多次未完成，请检查"
		return nil, m.agent.task(task)
	}
	task.State = "running"
	task.Runs++
	if err := m.agent.task(task); err != nil {
		return nil, err
	}
	in := &companion.Input{Channel: "wechat", Account: task.Account, Thread: task.Thread, Sender: task.Sender, Mode: "direct", IsGroup: strings.HasSuffix(task.Thread, "@chatroom"), Directed: true, TaskID: task.ID, OriginTraceID: task.OriginTrace, RequestedText: task.RequestedText, TraceID: "music-" + task.ID + "-" + now.UTC().Format("150405"), QuoteServerID: task.QuoteServerID}
	if status.State == "completed" {
		in.Text = "[已请求音乐任务的结果交付，不是新用户请求] music_job=" + task.MusicJob + "；原始需求资料：" + task.RequestedText + "；歌曲：" + task.Title + "。本地模型已经完成，调用music_local_status取得真实附件，kind=" + task.MusicKind + "；结合最新会话，如对方已取消则取消任务，如改要文件则遵从。最终用media_ids交付实际结果，介绍在附件被接受后发送。不要重新生成，也不要只说稍等。"
	} else {
		in.Text = "[已请求音乐任务的失败通知，不是新用户请求] 本地YuE任务已终止，状态=" + status.State + "。如实告知这次歌曲没有生成完成，请检查本地服务；不要说还在制作，不自动重新生成。"
		task.MusicFailure = true
		task.MusicPrepared = true // A failure notice is the terminal outcome, with no media.
		_ = m.agent.task(task)
	}
	m.record(bot.Record{Event: "music_generation_finished", Category: "tool", ThreadID: task.Thread, MessageID: task.OriginTrace, Status: status.State, Reason: "music_result_ready_for_delivery", Origin: "agent"})
	return in, nil
}
