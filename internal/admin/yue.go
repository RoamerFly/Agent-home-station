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
	"math"
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
		agentTool("music_local_resume", "Recover the SAME interrupted/failed RVC or YuE computation owned by THIS conversation. Inspect status and fix its cause first. RVC revalidates source/recipe/voice. YuE reconnects to its original prompt; restart_missing=true permits the frozen graph/seed/reference to run again ONLY after verified backend absence; restart_failed=true permits retry after a verified backend execution error has been investigated/fixed. Prior prompt IDs remain recorded. Keeps job_id, reuses active/completed jobs, never revives cancelled jobs or sends media. Input {job_id:string,restart_missing?:boolean,restart_failed?:boolean}.", `{"type":"object","properties":{"job_id":{"type":"string"},"restart_missing":{"type":"boolean"},"restart_failed":{"type":"boolean"}},"required":["job_id"],"additionalProperties":false}`),
		agentTool("music_rvc_start", "Default for covering an existing song: preserve the original recording melody, lyrics, timing and accompaniment; locally separate vocals, convert to the configured character RVC voice and mix back. No YuE generation, no ASR/rewritten lyrics required. First find and cache the correct vocal recording in THIS conversation. reference_start_seconds/reference_seconds select a passage; omit both for the full actual recording (single processing window up to600s; longer songs use explicit windows). Default complete voice notes; file/both only if requested. Real job_id has durable follow-up via music_local_status. Select an existing public voice via voice_library_list/read and pass voice_profile_id from that record; omission uses current default. Never claim Manbo if no selected profile. A submission is not listening or delivery verification.", `{"type":"object","properties":{"title":{"type":"string","minLength":1,"maxLength":24},"voice_profile_id":{"type":"string"},"reference_asset_id":{"type":"string"},"reference_start_seconds":{"type":"number","minimum":0},"reference_seconds":{"type":"number","exclusiveMinimum":0,"maximum":600},"kind":{"type":"string","enum":["voice","file","both"]}},"required":["title","reference_asset_id"],"additionalProperties":false}`),
		agentTool("music_sampling_options", "Read live native YuE semantic, ABC score and diffusion sampling parameters: meanings, actual backend limits/enums, workflow defaults. Read this before tuning music_local_start.sampling or original planning_mode; defaults are not capability ceilings. No song created. Input {}.", `{"type":"object","properties":{},"additionalProperties":false}`),
		agentTool("music_service_status", "Read actual YuE bridge and generation backend health without creating or retrying a song. Use this for local health; web_fetch is public-only. When the managed local service is inactive, trusted owners can call music_service_start then recheck this status. Otherwise read shared project-yue.md and project startup docs, then use permitted server tools on the correct OS. Report diagnosis separately from verified repair. Input {}.", `{"type":"object","properties":{},"additionalProperties":false}`),
		agentTool("music_lyrics_compare", "Compare one actual ASR lyrics window with a version found on the web. First web_search for song/title/artist lyrics and web_fetch plausible versions; then supply asset_id, lyrics_evidence_id of ORIGINAL ASR, candidate_url and candidate_lyrics copied from that page. Reads the page with permitted web_fetch to verify real text. Aligns the corresponding passage, not the whole song. Only >=88% match and <=12%/12 edits permits minor correction, returning a new lyrics_evidence_id and exact lyrics. Large differences reject this version; compare another. Does not certify artist/version or human accuracy. Never replace uncertain ASR with invented text.", `{"type":"object","properties":{"asset_id":{"type":"string"},"lyrics_evidence_id":{"type":"string"},"candidate_url":{"type":"string","maxLength":2048},"candidate_lyrics":{"type":"string","maxLength":6000}},"required":["asset_id","lyrics_evidence_id","candidate_url","candidate_lyrics"],"additionalProperties":false}`),
		agentTool("music_local_start", "Start local YuE ORIGINAL or SIMILAR-song resynthesis and durable follow-up. Existing-song direct covers default to music_rvc_start; only use YuE reference resynthesis when the user wants a similar/new take or explicitly chooses YuE. Default native female; character/RVC only if explicitly requested. When the user explicitly wants YuE reference resynthesis, use cover with this conversation actual reference_asset_id, lyrics and lyrics_evidence_ids from music_reference_inspect/music_lyrics_compare. Keep original melody, rhythm, phrasing and lyrics; follow the music policy supplied in context. On reroll reuse the previously verified recording unless the user changes version; source choice and lyric/score alignment matter more than adding style prose. reference_mode=melody by default (full also available). reference_start_seconds and reference_seconds select the source passage; explicit0 retains intro. Supply words matching that exact passage, preserve musical line breaks/sections, never blindly concatenate overlapping ASR windows. duration_seconds10..600 default60 is a generation ceiling. min_duration_seconds defaults90% of target, explicit0 allows preview; do not pad/stretch. text_guidance_scale0..100 controls native style/lyrics CFG (cover2, original1); stronger is not necessarily more faithful. seed fixes a take; omit for new take. Default complete voice notes<=60s; both only if requested. Result job_id is submission evidence, not audio quality or delivery. Query music_local_status for actual duration and sampled listening; media_reuse resends exact originals.", extendMusicSamplingSchema(`{"type":"object","properties":{"singing_voice":{"type":"string","enum":["native","character"]},"vocal_gender":{"type":"string","enum":["female","male"]},"vocal_gender_request":{"type":"string","description":"Exact current-user quote explicitly requesting male singing, required for male"},"title":{"type":"string","minLength":1,"maxLength":24},"mode":{"type":"string","enum":["original","cover"]},"style":{"type":"string"},"lyrics":{"type":"string"},"seed":{"type":"integer","minimum":0,"maximum":2147483647},"reference_mode":{"type":"string","enum":["melody","full"]},"reference_start_seconds":{"type":"number","minimum":0},"reference_seconds":{"type":"number","exclusiveMinimum":0,"maximum":600},"duration_seconds":{"type":"integer","minimum":10,"maximum":600},"text_guidance_scale":{"type":"number","minimum":0,"maximum":100},"min_duration_seconds":{"type":"number","minimum":0},"kind":{"type":"string","enum":["voice","file","both"]},"reference_asset_id":{"type":"string"},"lyrics_evidence_ids":{"type":"array","items":{"type":"string"}}},"required":["title","style","lyrics"],"additionalProperties":false}`)),
		agentTool("music_reference_inspect", "Inspect exact cached audio from THIS conversation before covering, rerolling or recovering a take. Returns submitted style/lyrics/seed when this is a known generated song. Optional transcribe=true calls configured ASR for <=60 seconds at offset_seconds; candidate transcript is uncertain, not guaranteed lyrics. Pass lyrics_evidence_ids to inspect combined timing/order/duplicate boundaries BEFORE generating; coarse ASR equality is not word-to-note alignment. Returns lyrics_evidence_id; pass it in music_local_start.lyrics_evidence_ids and copy the transcript exactly (section labels allowed). supplied_lyrics may register words literally provided in the current user message, never invented words. Default original-recording covers use music_rvc_start, preserving the source without rewriting lyrics. YuE reference resynthesis is for a similar/new rendition or explicit YuE request, and then uses verified lyrics and source style. Use the exact quoted asset over another newer take. If only an original recording is available, listen/transcribe rather than inventing lyrics; if user demands unchanged accompaniment explain that YuE resynthesizes. Input {asset_id,transcribe?,offset_seconds?,seconds?}.", `{"type":"object","properties":{"asset_id":{"type":"string"},"transcribe":{"type":"boolean"},"offset_seconds":{"type":"integer","minimum":0,"maximum":600},"seconds":{"type":"integer","minimum":1,"maximum":60},"lyrics_evidence_ids":{"type":"array","items":{"type":"string"}},"supplied_lyrics":{"type":"string","maxLength":6000}},"required":["asset_id"],"additionalProperties":false}`),
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
	// Server-derived identity, independent of model parameters or a resumed
	// task's quoted account. Managed brokers reject a different account scope.
	accountScope := accountDirectory(p.manager.native.Account())
	req.Header.Set("X-Wechat-Agent-Account", accountScope)
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
	if bound := response.Header.Get("X-Wechat-Agent-Account"); bound != "" && bound != accountScope {
		return nil, "", yueError("yue_account_scope_mismatch")
	}
	data, e := io.ReadAll(io.LimitReader(response.Body, (64<<20)+1))
	if e != nil || len(data) > 64<<20 {
		return nil, "", yueError("yue_response_limit")
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		if response.StatusCode == http.StatusConflict {
			var detail struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(data, &detail) == nil && detail.Error == "music_account_scope_mismatch" {
				return nil, "", yueError("yue_account_scope_mismatch")
			}
			if strings.HasPrefix(detail.Error, "rvc_recovery_") {
				return nil, "", toolInputError("原RVC任务恢复校验失败：" + detail.Error + "；检查原文件、配方及声线配置，不新建重复歌曲")
			}
			if detail.Error == "yue_recovery_prompt_missing_explicit_restart_required" {
				return nil, "", toolInputError("YuE后端队列与历史中均未找到原生成任务；原配方和素材仍在。需要继续原需求时，以同一个job_id调用music_local_resume并设置restart_missing=true，按保存的工作流重算")
			}
			if detail.Error == "yue_recovery_prompt_failed_explicit_retry_required" {
				return nil, "", toolInputError("YuE原后端任务已确认执行失败；先读取并修复实际错误，再用同一个job_id调用music_local_resume并设置restart_failed=true，按保存的工作流重算。旧失败记录保留")
			}
			if strings.HasPrefix(detail.Error, "yue_recovery_") {
				return nil, "", toolInputError("原YuE任务恢复核对失败：" + detail.Error + "；读取原任务状态，核对保存的请求、参考音频、工作流及声线配置")
			}
		}
		if response.StatusCode == 400 {
			var detail struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(data, &detail) == nil && (strings.HasPrefix(detail.Error, "rvc_") || strings.HasPrefix(detail.Error, "sampling_") || strings.HasPrefix(detail.Error, "invalid_sampling_") || detail.Error == "invalid_planning_mode" || detail.Error == "score_sampling_requires_active_planning") {
				return nil, "", toolInputError("YuE原生参数校验失败：" + detail.Error + "；请查询music_sampling_options后调整")
			}
		}
		if response.StatusCode == 401 || response.StatusCode == 403 {
			return nil, "", yueError("yue_bridge_auth_failed")
		}
		if response.StatusCode == 503 {
			var detail struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(data, &detail) == nil && strings.HasPrefix(detail.Error, "rvc_") {
				return nil, "", toolInputError("RVC服务未就绪：" + detail.Error + "；请检查原任务和本机角色模型，不改为YuE冒充原曲翻唱")
			}
			if detail.Error == "music_recovery_unavailable" {
				return nil, "", toolInputError("原音乐任务恢复时未能核对生成后端或调度器；先检查music_service_status和本机桥接日志，原任务保留。连接失败不代表后端任务已丢失")
			}
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
		if err := in.Sampling.validate(); err != nil {
			return "", err
		}
		if in.PlanningMode != "" && in.PlanningMode != "off" && in.PlanningMode != "melody" && in.PlanningMode != "full" {
			return "", toolInputError("planning_mode参数无效")
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
	var listening map[string]any
	if spec != nil && spec.VocalGender != "" {
		listening = p.reviewSongAudio(ctx, audio, spec.VocalGender)
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
	p := &permittedTools{manager: m}
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
