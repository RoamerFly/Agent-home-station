package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
		agentTool("music_local_start", "Create an original song with local YuE. Supply a meaningful song title chosen from the user intent, style and original [verse]/[chorus] lyrics fitting duration_seconds (10..600, default60). A one-minute request MUST set60 and use short lyrics. Optional reference_asset_id is downloaded audio in THIS conversation. Automatically persists completion/delivery; do not create another agent_task. Defaults to the COMPLETE song as voice notes of at most60 seconds each. Use both when the user requests singing AND the original file; use file for file-only requests; never ask whether user wants a file. Returns job_id and automatic_followup.", `{"type":"object","properties":{"title":{"type":"string","minLength":1,"maxLength":24},"style":{"type":"string"},"lyrics":{"type":"string"},"duration_seconds":{"type":"integer","minimum":10,"maximum":600},"kind":{"type":"string","enum":["voice","file","both"]},"reference_asset_id":{"type":"string"}},"required":["title","style","lyrics"],"additionalProperties":false}`),
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
		return nil, "", errors.New("本地YuE桥接无法连接；请检查本机服务")
	}
	defer response.Body.Close()
	data, e := io.ReadAll(io.LimitReader(response.Body, (64<<20)+1))
	if e != nil || len(data) > 64<<20 {
		return nil, "", errors.New("YuE响应超过限制")
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		if response.StatusCode == 401 || response.StatusCode == 403 {
			return nil, "", errors.New("YuE桥接鉴权失败，请检查桥接令牌")
		}
		if response.StatusCode == 503 {
			return nil, "", errors.New("YuE桥接在线，但ComfyUI生成后端不可用；未提交生成任务")
		}
		return nil, "", errors.New("YuE拒绝请求或任务未完成")
	}
	return data, response.Header.Get("Content-Type"), nil
}

type yueHealth struct {
	State   string `json:"state"`
	Message string `json:"message"`
	Ready   bool   `json:"ready"`
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
		return yueHealth{State: "bridge_unavailable", Message: err.Error()}
	}
	var reply struct {
		Ready bool `json:"ready"`
	}
	if json.Unmarshal(b, &reply) != nil {
		return yueHealth{State: "invalid_health", Message: "YuE桥接健康响应无效"}
	}
	if !reply.Ready {
		return yueHealth{State: "backend_unavailable", Message: "YuE桥接在线，ComfyUI生成后端尚未就绪；未提交任务"}
	}
	return yueHealth{State: "ready", Message: "YuE桥接与ComfyUI生成后端均在线", Ready: true}
}
func (p *permittedTools) callYuE(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	var in struct {
		Duration             int `json:"duration_seconds"`
		Title, Style, Lyrics string
		Reference            string `json:"reference_asset_id"`
		ID                   string `json:"job_id"`
		Kind                 string
	}
	if json.Unmarshal(raw, &in) != nil {
		return "", errors.New("YuE参数无效")
	}
	if name == "music_local_start" {
		if p.trace.TaskID != "" {
			for _, task := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
				if task.ID == p.trace.TaskID && task.MusicJob != "" {
					return "", errors.New("这是已有歌曲的结果交付，请查询原任务，不重新生成")
				}
			}
		}
		if !p.manager.agent.config().TasksEnabled {
			return "", errors.New("生成歌曲需启用Agent持续任务，以便完成后交付")
		}
		in.Kind = p.musicDeliveryKind(in.Kind)
		if in.Duration == 0 {
			in.Duration = 60
		}
		if in.Duration < 10 || in.Duration > 600 {
			return "", errors.New("歌曲时长须为10至600秒")
		}
		if !p.musicKindPermitted(in.Kind) {
			return "", errors.New("歌曲发送格式未授权")
		}
		if in.Style == "" || in.Lyrics == "" || len(in.Style) > 2000 || len(in.Lyrics) > 6000 {
			return "", errors.New("请提供风格和分段歌词")
		}
		in.Title = songTitle(in.Title)
		if in.Title == "" {
			return "", errors.New("请根据用户歌曲语义自拟具体歌名，不能使用通用占位名称")
		}
		body := map[string]any{"title": in.Title, "style": in.Style, "lyrics": in.Lyrics, "duration_seconds": in.Duration}
		raw, _ = json.Marshal(map[string]any{"title": in.Title, "style": in.Style, "lyrics": in.Lyrics, "kind": in.Kind, "duration_seconds": in.Duration, "reference_asset_id": in.Reference})
		if in.Reference != "" {
			m := p.manager
			m.assetMu.Lock()
			assets, e := m.loadAssets()
			m.assetMu.Unlock()
			if e != nil {
				return "", e
			}
			found := false
			for _, a := range assets {
				if a.ID != in.Reference || a.Account != p.trace.Account || a.Scope != assetScope(p.trace) {
					continue
				}
				if len(a.Hash) != 64 || !strings.HasPrefix(a.MIME, "audio/") {
					return "", errors.New("参考素材必须是当前会话已下载的原音频")
				}
				data, e := os.ReadFile(filepath.Join(m.dir, "media-assets", a.Hash))
				if e != nil || len(data) > 25<<20 {
					return "", errors.New("参考音频缓存不可用")
				}
				hash := sha256.Sum256(data)
				if hex.EncodeToString(hash[:]) != a.Hash {
					return "", errors.New("参考音频校验失败")
				}
				body["reference_base64"] = base64.StdEncoding.EncodeToString(data)
				found = true
				break
			}
			if !found {
				return "", errors.New("当前会话找不到该参考音频")
			}
		}
		// Store the remote job mapping under the private effect ledger.
		// Probe without side effects before claiming a create operation. An offline
		// bridge must not become a fictitious successful/uncertain generation job.
		if health := p.probeYuE(ctx); !health.Ready {
			return "", errors.New(health.Message)
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
			return "", errors.New("歌曲任务响应无效")
		}
		id, _ := job["job_id"].(string)
		if !validID(id) {
			return "", errors.New("YuE生成任务未取得有效任务编号；提交结果未确认，请核对任务记录，不重复提交")
		}
		taskID := agentID(p.trace.Account, p.trace.Thread, "yue", id)
		found := false
		for _, old := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
			found = found || old.ID == taskID
		}
		if !found {
			task := agentTask{ID: taskID, Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, Title: "原创歌曲 · " + in.Title, MusicTitle: in.Title, RequestedText: companion.UserRequestText(p.trace.Text), Checkpoint: "YuE已提交，等待真实生成结果；不重新生成", State: "waiting", Next: time.Now().Add(20 * time.Second), MusicDeadline: time.Now().Add(30 * time.Minute), MusicJob: id, MusicKind: in.Kind, OriginTrace: p.trace.TraceID, QuoteServerID: p.trace.QuoteServerID}
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
		job["automatic_followup"] = true
		job["task_id"] = taskID
		job["instruction"] = "已真实提交本地YuE，后续检查与交付已自动持久化。可以自然告知正在制作；不要说已生成，不必另建任务或本轮反复查询。"
		data, _ := json.Marshal(job)
		return string(data), nil
	}
	if !validID(in.ID) {
		return "", errors.New("任务ID无效")
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
	if !authorized {
		return "", errors.New("该生成任务不属于当前会话")
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
		return "", errors.New("发送格式未授权")
	}
	audio, typ, e := p.yueRequest(ctx, "GET", "/jobs/"+in.ID+"/audio", nil, "")
	if e != nil {
		return "", e
	}
	title := ""
	for _, t := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
		if t.MusicJob == in.ID {
			title = t.MusicTitle
			break
		}
	}
	result, e := p.stageSongOutput(ctx, audio, typ, title, in.Kind)
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
			task.LastResult = "暂时无法读取本地生成状态，稍后继续核对同一任务，不重新生成"
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
	in := &companion.Input{Channel: "wechat", Account: task.Account, Thread: task.Thread, Sender: task.Sender, Mode: "direct", IsGroup: strings.HasSuffix(task.Thread, "@chatroom"), Directed: true, TaskID: task.ID, TraceID: "music-" + task.ID + "-" + now.UTC().Format("150405"), QuoteServerID: task.QuoteServerID}
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
