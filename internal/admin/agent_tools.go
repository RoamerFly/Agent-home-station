package admin

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

func agentTool(name, description, schema string) mcptools.Tool {
	return mcptools.NewTool(name, description, schema)
}
func (p *permittedTools) agentToolsAllowed() bool {
	if p.manager.agent == nil {
		return false
	}
	return p.manager.agent.config().Enabled && p.trace.Account == p.manager.native.Account() && !p.trace.Preview && !p.trace.ReportOnly && !p.trace.Surf && permittedRecipient(p.manager.Config().Config, p.trace.Thread)
}

func (p *permittedTools) agentTools() []mcptools.Tool {
	return append(p.workspaceAgentTools(), p.wechatCommunicationTools()...)
}

func (p *permittedTools) workspaceAgentTools() []mcptools.Tool {
	if p.manager.agent == nil {
		return nil
	}
	c := p.manager.agent.config()
	if !p.agentToolsAllowed() {
		return nil
	}
	tools := p.workspaceTools()
	tools = append(tools, p.artifactTools()...)
	tools = append(tools, p.documentTools()...)
	tools = append(tools, p.memoryTools()...)
	tools = append(tools, p.voiceTools()...)
	if p.workspaceExecutionAllowed() {
		tools = append(tools, diagnosticsTool())
	}
	if p.workspaceExecutionAllowed() {
		tools = append(tools, agentTool("station_comfy", "使用 ComfyUI：run 可连同 source_message_id、source_path 或 own_mini 一次提交图生视频，自动上传并填参考图；刚收到的参考图优先用其消息编号。own_mini 指 home/character/大肥鱼-mini可爱版.jpg。h3-image-to-video 用原图作首帧；h3-image-reference-to-video 用 R2V 让角色参与复杂场景。run 可传 duration_seconds，按24帧/秒设置时长。用户明确改用新参考图时，先以 job_action=cancel 取消原 job_id，再用新图片消息编号重新 run；不要发送旧成品。workflows 查看参数，job 查询或取消，attach 暂存微信附件。模型清单与本地路径仅向管理员披露。", `{"type":"object","properties":{"action":{"type":"string","enum":["workflows","models","author","upload","run","job","attach"]},"source_message_id":{"type":"string"},"source_path":{"type":"string"},"own_mini":{"type":"boolean"},"fish_home":{"type":"boolean"},"duration_seconds":{"type":"number","minimum":0.1},"workflow_id":{"type":"string"},"inputs":{"type":"object","additionalProperties":{"type":["string","integer","number","boolean"]}},"job_id":{"type":"string"},"job_action":{"type":"string","enum":["status","cancel"]},"artifact_id":{"type":"string"},"category":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":0},"goal":{"type":"string"}},"required":["action"],"additionalProperties":false}`))
	}
	if p.privileged() || p.workspaceExecutionAllowed() {
		tools = append(tools, agentTool("music_service_start", "启动本机 YuE/RVC 受管服务；查询 music_service_status 就绪后续办原任务。", `{"type":"object","properties":{},"additionalProperties":false}`))
		tools = append(tools, agentTool("station_tool_manage", "公共工具状态与恢复：status 列目录；ensure_ready 启动已启用工具；repair 在空闲时修复，不重做原任务；start/restart/stop/enable 为显式操作。音乐桥接用 tool_id=music-bridge。", `{"type":"object","properties":{"action":{"type":"string","enum":["status","ensure_ready","repair","start","restart","stop","enable"]},"tool_id":{"type":"string"}},"required":["action"],"additionalProperties":false}`))
	}
	projectTools := p.projectPluginTools(context.Background())
	tools = append(tools, projectTools...)
	projectWriteVisible := false
	for _, tool := range projectTools {
		projectWriteVisible = projectWriteVisible || tool.Name == "project_create"
	}
	if p.workspaceExecutionAllowed() && (c.Host.Mode == "files" || c.Host.Mode == "full") && projectWriteVisible {
		tools = append(tools, agentTool("project_checkout_plugin", "Create or reopen an editable source copy of an installed plugin in the fish's conversation workspace. Copies verified package files only and preserves existing edits. Input {plugin_id:string}.", `{"type":"object","properties":{"plugin_id":{"type":"string"}},"required":["plugin_id"],"additionalProperties":false}`))
	}
	for _, tool := range p.projectTools() {
		if tool.Name == "project_create" && (!p.workspaceExecutionAllowed() || (c.Host.Mode != "files" && c.Host.Mode != "full")) {
			continue
		}
		if tool.Name != "agent_global_search" || p.privileged() {
			tools = append(tools, tool)
		}
	}
	tools = append(tools, agentTool("agent_clock", "Read current local time and timezone before scheduling. Create reminders only for an explicit future time; immediate work continues in its current task. Input {}.", `{"type":"object","properties":{},"additionalProperties":false}`))
	if c.TasksEnabled {
		tools = append(tools, reminderTool())
		tools = append(tools, agentTool("agent_tasks", "查询持久任务：active（默认，含检查点）或 history（已结束，可搜索、分页）。续办旧主题时先查已有任务。主人可跨会话，其他人限本会话。", `{"type":"object","properties":{"status":{"type":"string","enum":["active","history"]},"query":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}},"additionalProperties":false}`), agentTool("agent_task", "用 key 创建或 id 更新持续任务；当前任务续办会沿用 ID。纠正旧请求可传原消息的 origin_message_id，更新已有任务。pending/waiting 按 delay_seconds 续办，completed/cancelled/needs_review 结束。跨会话主人任务传真实 source IDs 和 delivery_thread_id；target_task_id 用于取消旧任务。", `{"type":"object","properties":{"id":{"type":"string"},"key":{"type":"string"},"title":{"type":"string"},"checkpoint":{"type":"string"},"state":{"type":"string","enum":["pending","waiting","completed","cancelled","needs_review"]},"delay_seconds":{"type":"integer","minimum":0},"origin_message_id":{"type":"string"},"source_thread_id":{"type":"string"},"source_message_ids":{"type":"array","items":{"type":"string"},"maxItems":50},"delivery_thread_id":{"type":"string"},"target_task_id":{"type":"string"}},"required":["checkpoint","state"],"additionalProperties":false}`))
	}
	if !p.privileged() {
		return tools
	}
	tools = append(tools, agentTool("agent_owner_report", "将新的技术诊断私聊报告给账号主人；同一事实只报一次。", `{"type":"object","properties":{"text":{"type":"string","minLength":1}},"required":["text"],"additionalProperties":false}`))

	return tools
}
func (p *permittedTools) agentToolEnabled(name string) bool {
	// Already running conversations may still name a former lifecycle tool.
	// The new model catalogue exposes one manage entry instead.
	if strings.HasPrefix(name, "station_tool_") && name != "station_tool_manage" && p.agentToolsAllowed() && (p.privileged() || p.workspaceExecutionAllowed()) {
		switch name {
		case "station_tool_status", "station_tool_enable", "station_tool_start", "station_tool_stop", "station_tool_restart":
			return true
		}
	}
	tools := p.workspaceAgentTools
	if wechatapp.ToolName(name) {
		tools = p.wechatCommunicationTools
	}
	for _, t := range tools() {
		if t.Name == name {
			return true
		}
	}
	return false
}
func agentToolName(name string) bool {
	if strings.HasPrefix(name, "memory_") {
		return true
	}
	if name == "agent_owner_report" {
		return true
	}
	if name == "agent_reminder" {
		return true
	}
	return strings.HasPrefix(name, "voice_library_") || strings.HasPrefix(name, "artifact_") || strings.HasPrefix(name, "document_") || name == "music_service_start" || strings.HasPrefix(name, "station_tool_") || name == "station_comfy" || strings.HasPrefix(name, "project_") || name == "agent_diagnostics" || name == "agent_global_search" || strings.HasPrefix(name, "workspace_") || strings.HasPrefix(name, "server_") || wechatapp.ToolName(name) || name == "agent_task" || name == "agent_tasks" || name == "agent_clock"
}
func (p *permittedTools) callAgent(ctx context.Context, name string, args json.RawMessage) (result string, callErr error) {
	defer func() {
		if name == "agent_task" {
			callErr = localAgentError(callErr)
		}
	}()
	if !p.agentToolEnabled(name) {
		return "", toolInputError("当前用户、会话或预演没有此项权限")
	}
	if strings.HasPrefix(name, "server_") || strings.HasPrefix(name, "voice_library_") || name == "agent_owner_report" {
		p.privateDiagnostics = true
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if wechatapp.SubmissionTool(name) {
		if err := p.manager.checkWechatSubmission(ctx); err != nil {
			return "", err
		}
	}
	// Canonical JSON makes object property order irrelevant to operation identity.
	var canonical any
	if json.Unmarshal(args, &canonical) != nil {
		return "", toolInputError("参数不是有效JSON")
	}
	args, _ = json.Marshal(canonical)
	if isProjectPluginTool(name) {
		return p.callProjectPlugin(ctx, name, args)
	}
	encode := func(v any, e error) (string, error) {
		if e != nil {
			return "", e
		}
		b, e := json.Marshal(v)
		return string(b), e
	}
	m := p.manager
	if strings.HasPrefix(name, "memory_") {
		return p.callMemory(ctx, name, args)
	}
	if name == "music_service_start" {
		return p.controlMusicService(ctx, "start")
	}
	if strings.HasPrefix(name, "station_tool_") {
		return p.callStationTool(ctx, strings.TrimPrefix(name, "station_tool_"), args)
	}
	if name == "station_comfy" {
		return p.callStationComfy(ctx, args)
	}
	if name == "project_checkout_plugin" {
		return p.checkoutPluginSource(ctx, args)
	}
	if strings.HasPrefix(name, "voice_library_") {
		return p.callVoiceLibrary(ctx, name, args)
	}
	if name == "agent_diagnostics" {
		return p.callDiagnostics(args)
	}
	if strings.HasPrefix(name, "project_") || name == "agent_global_search" {
		result, err := p.callProject(ctx, name, args)
		if err == nil && name == "agent_global_search" {
			p.routeGlobalSearchResult(result)
		}
		return result, err
	}
	if strings.HasPrefix(name, "workspace_") {
		return p.callWorkspace(ctx, name, args)
	}
	if strings.HasPrefix(name, "artifact_") {
		return p.callArtifact(ctx, name, args)
	}
	if strings.HasPrefix(name, "document_") {
		switch name {
		case "document_inspect":
			return p.callDocumentWorkshop(ctx, "document.inspect", args)
		case "document_reconstruct":
			return p.callDocumentWorkshop(ctx, "document.reconstruct", args)
		case "document_export_pdf":
			return p.callDocumentWorkshop(ctx, "document.export_pdf", args)
		case "document_verify":
			return p.callDocumentWorkshop(ctx, "document.verify", args)
		default:
			return "", toolInputError("unknown_document_tool")
		}
	}
	switch name {
	case "wechat_recall":
		return p.recallMessage(ctx, args)
	case "agent_owner_report":
		var in struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(args, &in) != nil || strings.TrimSpace(in.Text) == "" || len(in.Text) > 12000 || !p.privileged() {
			return "", toolInputError("owner_report_not_authorized_or_invalid")
		}
		if err := p.manager.queueOwnerDetail(p.trace, in.Text); err != nil {
			return "", err
		}
		return encode(map[string]any{"state": "queued_private_report", "instruction": "技术报告已安排给主人私聊；群里只自然简短回应结果，不再复述技术内容。"}, nil)
	case "wechat_file_read":
		return p.readWechatAttachment(ctx, args)
	case "agent_reminder":
		return p.callReminder(ctx, args)
	case "agent_clock":
		now := time.Now()
		zone, offset := now.Zone()
		return encode(map[string]any{"now": now.Format(time.RFC3339Nano), "utc": now.UTC().Format(time.RFC3339Nano), "unix_seconds": now.Unix(), "timezone": zone, "utc_offset_seconds": offset}, nil)
	case "wechat_delivery_status", "wechat_delivery_retry":
		result, err := p.callDelivery(ctx, name, args)
		return result, localAgentError(err)
	case "wechat_read":
		native, _ := m.native.(wechatapp.CatalogReader)
		read, err := wechatapp.ReadCatalog(ctx, native, p.trace.Thread, args, func(thread string) bool {
			return p.agentToolEnabled(name) && p.conversationReadAllowed(thread)
		})
		if errors.Is(err, wechatapp.ErrReadDenied) || errors.Is(err, wechatapp.ErrReadUnavailable) {
			return "", toolInputError(err.Error())
		}
		if err != nil {
			return "", err
		}
		result, err := encode(read.Page, nil)
		if err == nil {
			if read.Kind == "messages" {
				p.recordCrossSourceMessages(read.Thread, result)
			}
			p.routeCrossConversationThread(read.Thread)
		}
		return result, err
	case "wechat_send_media":
		return p.sendMedia(ctx, args)
	case "wechat_forward_file":
		return p.forwardFile(ctx, args)
	case "wechat_conversations":
		return p.listAccountConversations(ctx, args)
	case "wechat_operation_status":
		return p.callWechatOperation(ctx, args)
	case "agent_tasks":
		var in struct {
			Status string `json:"status"`
			Query  string `json:"query"`
			Offset int    `json:"offset"`
			Limit  int    `json:"limit"`
		}
		if json.Unmarshal(args, &in) != nil || !contains([]string{"", "active", "history"}, in.Status) || in.Offset < 0 || in.Limit < 0 {
			return "", toolInputError("任务列表分页参数无效")
		}
		thread := p.trace.Thread
		if p.privileged() && m.agent.config().CrossConversation {
			thread = ""
		}
		if in.Status == "history" {
			requested := in.Limit
			if requested == 0 {
				requested = 20
			}
			limit := min(requested, 100)
			query := strings.ToLower(strings.TrimSpace(in.Query))
			matches := []map[string]any{}
			for _, task := range m.agent.list(p.trace.Account, thread) {
				if task.State == "pending" || task.State == "waiting" || task.State == "running" || task.State == "waiting_delivery" || query != "" && !strings.Contains(strings.ToLower(task.Title), query) {
					continue
				}
				if task.Reminder != nil && task.Sender != p.trace.Sender && !p.privileged() {
					continue
				}
				matches = append(matches, map[string]any{"id": task.ID, "title": task.Title, "state": task.State, "origin_message_id": task.OriginTrace, "thread_id": task.Thread, "updated": task.Updated, "result": logText(task.LastResult, 240)})
			}
			start := min(in.Offset, len(matches))
			end := start + min(limit, len(matches)-start)
			return encode(map[string]any{"items": matches[start:end], "total": len(matches), "next_offset": end, "has_more": end < len(matches), "requested_limit": requested, "effective_limit": limit}, nil)
		}
		tasks := m.agent.active(p.trace.Account, thread)
		visible := tasks[:0]
		for _, task := range tasks {
			if task.Reminder == nil || task.Sender == p.trace.Sender {
				visible = append(visible, task)
			}
		}
		tasks = visible
		for i := range tasks {
			tasks[i] = copyTask(tasks[i])
			if tasks[i].PluginJob != nil {
				// Origin is host recovery evidence, not conversation-visible data.
				tasks[i].PluginJob.Origin = nil
				tasks[i].PluginJob.PollState = nil
			}
			if tasks[i].OwnerRequested && !p.privileged() {
				tasks[i].Actions = nil
				tasks[i].Goal = ""
			}
			if tasks[i].MusicSpec != nil {
				tasks[i].MusicSpec.Style = ""
				tasks[i].MusicSpec.Lyrics = ""
			}
		}
		if in.Status == "active" || in.Query != "" || in.Offset != 0 || in.Limit != 0 {
			query := strings.ToLower(strings.TrimSpace(in.Query))
			matches := make([]agentTask, 0, len(tasks))
			for _, task := range tasks {
				if query == "" || strings.Contains(strings.ToLower(task.Title), query) {
					matches = append(matches, task)
				}
			}
			requested := in.Limit
			if requested == 0 {
				requested = 20
			}
			limit := min(requested, 100)
			start := min(in.Offset, len(matches))
			end := start + min(limit, len(matches)-start)
			return encode(map[string]any{"items": matches[start:end], "total": len(matches), "next_offset": end, "has_more": end < len(matches), "requested_limit": requested, "effective_limit": limit}, nil)
		}
		return encode(tasks, nil)
	case "agent_task":
		var in struct {
			ID               string   `json:"id"`
			Key              string   `json:"key"`
			Title            string   `json:"title"`
			Checkpoint       string   `json:"checkpoint"`
			State            string   `json:"state"`
			SourceThread     string   `json:"source_thread_id"`
			SourceMessageIDs []string `json:"source_message_ids"`
			DeliveryThread   string   `json:"delivery_thread_id"`
			TargetTaskID     string   `json:"target_task_id"`
			OriginMessageID  string   `json:"origin_message_id"`
			DelaySeconds     int      `json:"delay_seconds"`
		}
		if json.Unmarshal(args, &in) != nil || len(in.Checkpoint) > 6000 || len(in.Title) > 200 || !contains([]string{"pending", "waiting", "completed", "cancelled", "needs_review"}, in.State) {
			return "", toolInputError("任务参数无效")
		}
		if (in.State == "pending" || in.State == "waiting") && (in.DelaySeconds < 0 || int64(in.DelaySeconds) > int64((1<<63-1)/time.Second)) {
			return "", toolInputError("任务等待秒数无效")
		}
		origin := p.trace.OriginTraceID
		if origin == "" {
			origin = p.trace.TraceID
		}
		if in.OriginMessageID != "" && in.TargetTaskID != "" {
			return "", toolInputError("取消任务时不需要指定来源消息")
		}
		if in.TargetTaskID != "" {
			if in.ID != "" || in.State != "cancelled" || !p.privileged() || !p.manager.agent.config().CrossConversation {
				return "", toolInputError("跨会话任务只能由可信主人明确取消")
			}
			target, ok := m.agent.lookupTask(in.TargetTaskID, p.trace.Account, "")
			if !ok || target.Sender != p.trace.Sender || !p.conversationReadAllowed(taskDeliveryThread(target)) {
				return "", toolInputError("指定任务不存在、不是本人任务或投递目标已撤权")
			}
			if target.State == "cancelled" {
				return encode(target, nil)
			}
			target.State = "cancelled"
			target.LastResult = "已由原请求管理员取消；未提交部分停止，已提交部分仅核对回执"
			target.Checkpoint = strings.TrimSpace(in.Checkpoint)
			if target.Checkpoint == "" {
				target.Checkpoint = "原请求者已明确取消；不再提交剩余内容，已提交部分仅核对回执。"
			}
			if err := m.agent.task(target); err != nil {
				return "", err
			}
			m.record(bot.Record{Event: "agent_task_cancelled", Category: "decision", ThreadID: taskDeliveryThread(target), Sender: p.trace.Sender, MessageID: p.trace.TraceID, Content: target.Title, Status: "cancelled", Reason: "owner_cross_conversation_task_cancelled", Origin: "agent"})
			return encode(target, nil)
		}

		sourceThread := in.SourceThread
		if sourceThread == "" {
			sourceThread = p.trace.Thread
		}
		if !p.trustedTaskSource(sourceThread) {
			return "", mcptools.ArgumentError("$.source_thread_id", "省略则使用当前会话；其他来源会话须先通过wechat_read读取")
		}
		if !p.trustedTaskSourceMessages(sourceThread, in.SourceMessageIDs) {
			return "", mcptools.ArgumentError("$.source_message_ids", "当前请求会自动绑定原消息。需要引用其他消息时，使用wechat_read返回的原消息ID")
		}
		var selectedOriginText, selectedOriginServerID string
		if in.OriginMessageID != "" {
			trustedOrigin := p.trustedTaskSourceMessages(sourceThread, []string{in.OriginMessageID})
			if !trustedOrigin && p.trace.TaskID != "" {
				if current, ok := m.agent.lookupTask(p.trace.TaskID, p.trace.Account, p.trace.Thread); ok {
					trustedOrigin = current.OriginTrace == in.OriginMessageID && current.Sender == p.trace.Sender && taskSourceThread(current) == sourceThread
				}
			}
			if sourceThread != p.trace.Thread || !trustedOrigin {
				return "", mcptools.ArgumentError("$.origin_message_id", "先在当前会话读取真实原请求，再使用返回的消息ID")
			}
			message, err := m.native.Message(ctx, in.OriginMessageID)
			if err != nil || message.ID != in.OriginMessageID || message.AccountID != p.trace.Account || message.ThreadID != sourceThread || message.Direction != "incoming" || message.SenderID != p.trace.Sender && !p.privileged() {
				return "", mcptools.ArgumentError("$.origin_message_id", "原请求的会话或发言者与当前任务不符")
			}
			origin, selectedOriginServerID = message.ID, message.ServerID
			if message.Type == 1 || message.IsQuotedText() {
				selectedOriginText = logText(message.Body, 2000)
			}
			if !contains(in.SourceMessageIDs, origin) {
				if len(in.SourceMessageIDs) >= 50 {
					return "", mcptools.ArgumentError("$.source_message_ids", "原请求ID需要加入来源列表；请将显式来源消息缩减至49条后重试")
				}
				in.SourceMessageIDs = append([]string{origin}, in.SourceMessageIDs...)
			}
		}
		if len(in.SourceMessageIDs) == 0 && sourceThread == p.trace.Thread && p.trace.Source != nil && p.trace.Source.MessageID != "" {
			in.SourceMessageIDs = []string{p.trace.Source.MessageID}
		}
		deliveryThread := in.DeliveryThread
		if deliveryThread == "" {
			deliveryThread = p.trace.Thread
			if sourceThread != p.trace.Thread && p.trace.IsGroup && p.privileged() {
				deliveryThread = p.trace.Sender
			}
		}
		if !p.conversationReadAllowed(deliveryThread) || sourceThread != p.trace.Thread && !p.privileged() {
			return "", toolInputError("任务投递目标未获授权")
		}
		// A continuation already has a durable task identity. A fresh key or
		// title in that turn updates its checkpoint instead of creating a second
		// timer for the same work. Cross-thread tasks still get their own record.
		if in.ID == "" && p.trace.TaskID != "" {
			if current, ok := m.agent.lookupTask(p.trace.TaskID, p.trace.Account, deliveryThread); ok &&
				current.Sender == p.trace.Sender && taskSourceThread(current) == sourceThread &&
				current.Reminder == nil && current.PluginJob == nil {
				in.ID = current.ID
			}
		}
		if in.ID == "" && in.OriginMessageID != "" {
			// A corrected request may be handled in a later conversation turn.
			// Reuse its one live task when the caller cites the verified original
			// message; a changed key or title must not create another timer.
			candidate := ""
			for _, old := range m.agent.list(p.trace.Account, deliveryThread) {
				if old.OriginTrace != in.OriginMessageID || old.Sender != p.trace.Sender ||
					taskSourceThread(old) != sourceThread || old.Reminder != nil || old.PluginJob != nil ||
					!unfinishedExecutionTask(old.State) {
					continue
				}
				if candidate != "" {
					candidate = ""
					break
				}
				candidate = old.ID
			}
			in.ID = candidate
		}
		t := agentTask{ParentID: p.trace.TaskID, OriginTrace: origin, OwnerRequested: p.privileged(), ID: in.ID, Account: p.trace.Account, Thread: deliveryThread, RequestThread: p.trace.Thread, SourceThread: sourceThread, SourceMessageIDs: append([]string(nil), in.SourceMessageIDs...), DeliveryThread: deliveryThread, Sender: p.trace.Sender, Title: in.Title}
		if sourceThread == p.trace.Thread && p.trace.OriginalText != nil && !p.trace.BackgroundResult {
			t.RequestedText = *p.trace.OriginalText
		}
		if in.OriginMessageID != "" {
			t.RequestedText = selectedOriginText
			t.QuoteServerID = selectedOriginServerID
		}
		t.Constraints = p.taskConstraints("agent_task", "", "", "")
		t.Constraints.Thread = sourceThread
		t.Constraints.DeliveryThread = deliveryThread
		if in.OriginMessageID != "" && origin != p.trace.TraceID {
			// A later chat reply cannot attest the earlier request's source proof.
			// Preserve its verified ID and literal, then re-read on continuation.
			t.Constraints.Source, t.Constraints.ReplyContext = nil, nil
		}
		if sourceThread != p.trace.Thread {
			// Cross-session reads prove that a conversation was read, but do not
			// manufacture a user-event stamp or a group quote for a private task.
			t.Constraints.Source = nil
			t.Constraints.ReplyContext = nil
		}
		if sourceThread != deliveryThread {
			t.QuoteServerID = ""
		}
		if t.ID != "" {
			if old, ok := m.agent.lookupTask(t.ID, p.trace.Account, deliveryThread); ok && (old.State == "completed" || old.State == "cancelled") {
				return encode(old, nil)
			}
			found := false
			for _, old := range m.agent.list(p.trace.Account, deliveryThread) {
				if old.ID == t.ID {
					if old.Sender != p.trace.Sender && !strings.HasSuffix(p.trace.Thread, "@chatroom") {
						return "", toolInputError("任务会话与当前发言者不一致")
					}
					t = old
					found = true
					break
				}
			}
			if !found {
				return "", toolInputError("未找到本会话任务")
			}
		} else {
			if in.Key == "" || len(in.Key) > 128 || in.Title == "" {
				return "", toolInputError("新任务请同时传key和title；续办现有任务请传agent_tasks返回的id")
			}
			// The key is the model's stable task handle. The title and source
			// messages identify its input, so two requests that happen to reuse
			// the same key cannot silently inherit one another's music job.
			t.ID = agentID(p.trace.Account, sourceThread, deliveryThread, origin, in.Key, in.Title, strings.Join(in.SourceMessageIDs, "\x00"))
			legacyID := agentID(p.trace.Account, deliveryThread, origin, in.Key)
			if old, ok := m.agent.lookupTask(legacyID, p.trace.Account, deliveryThread); ok &&
				old.OriginTrace == origin && old.Sender == p.trace.Sender && old.Title == in.Title && taskSourceThread(old) == sourceThread &&
				(old.RequestThread == "" || old.RequestThread == p.trace.Thread) &&
				(slices.Equal(old.SourceMessageIDs, in.SourceMessageIDs) || len(old.SourceMessageIDs) == 0 && len(in.SourceMessageIDs) <= 1) &&
				(old.RequestedText == "" || t.RequestedText == "" || old.RequestedText == t.RequestedText) {
				// Preserve existing task IDs across the ID schema upgrade, but only
				// when the persisted source and input still match.
				t.ID = legacyID
			}
			if old, ok := m.agent.lookupTask(t.ID, p.trace.Account, deliveryThread); ok && (old.State == "completed" || old.State == "cancelled") {
				return encode(old, nil)
			}
			for _, old := range m.agent.list(p.trace.Account, deliveryThread) {
				if old.ID == t.ID {
					t = old
					break
				}
			}
		}
		if t.PluginJob != nil {
			if in.State == "cancelled" {
				updated, err := m.cancelPluginTask(ctx, t)
				return encode(updated, err)
			}
			return "", toolInputError("工具任务由其持久生命周期管理，请使用plugin_job_status或plugin_job_cancel；不改写为已完成")
		}
		if t.Reminder != nil {
			return "", toolInputError("这是独立提醒任务；请用agent_reminder查询或取消，不能改写为其他程序任务")
		}
		if in.ID != "" {
			for _, id := range in.SourceMessageIDs {
				if !contains(t.SourceMessageIDs, id) {
					t.SourceMessageIDs = append(t.SourceMessageIDs, id)
				}
			}
		}
		t.Checkpoint = in.Checkpoint
		t.State = in.State
		if t.DeliveryJob != "" && t.State == "completed" {
			native, ok := m.native.(nativeTools)
			if !ok {
				return "", toolInputError("请先核对原发送回执，未标记完成")
			}
			status, e := native.ToolSendStatus(ctx, t.DeliveryJob)
			if e != nil || status.ThreadID != t.ReportSourceThread || status.State != "accepted" {
				t.State = "waiting"
				if in.DelaySeconds < 30 {
					in.DelaySeconds = 120
				}
				t.Checkpoint += "\n原发送仍未确认完成，继续核对同群聊天、修复和重试；不能以已查询状态代替完成交付。"
			}
		}
		t.Next = time.Now().UTC().Add(time.Duration(in.DelaySeconds) * time.Second)
		if in.Title != "" {
			t.Title = in.Title
		}
		t, err := m.agent.saveTask(t)
		if err != nil {
			return "", err
		}
		m.record(bot.Record{Event: "agent_task_saved", Category: "decision", ThreadID: p.trace.Thread, Sender: p.trace.Sender, MessageID: p.trace.TraceID, Content: t.Title, Status: t.State, Reason: "persistent_checkpoint_saved", Origin: "agent"})
		if t.OriginTrace != "" && t.OriginTrace != p.trace.TraceID && t.Thread == p.trace.Thread {
			m.record(bot.Record{Event: "task_followup_attached", Category: "decision", ThreadID: p.trace.Thread, Sender: p.trace.Sender, MessageID: p.trace.TraceID, RelatedMessageID: t.OriginTrace, Status: t.State, Origin: "agent"})
		}
		requirements := "proactive permission, selected target, quiet hours and daily budget; pending is not delivery"
		if t.OwnerRequested {
			requirements = "continuation of an authenticated owner request; current owner and conversation authorization rechecked; pending is not delivery"
		}
		return encode(map[string]any{"task": t, "background_requires": requirements, "storage_file": m.agent.dir + "/agent-state.json", "due_at": t.Next}, nil)
	case "wechat_send_text":
		return wechatapp.SendText(ctx, args, &wechatTextHost{p: p, raw: args})
	case "wechat_account_action":
		return wechatapp.AccountAction(ctx, args, &wechatActionHost{p: p, tool: name, raw: args})
	}
	return "", toolInputError("未知Agent工具")
}

func (p *permittedTools) rememberAgentSend(thread, text, key string) {
	c := p.manager.Config().Companion
	if !c.Permissions.Remember {
		return
	}
	in := companion.Input{Channel: "wechat", Account: p.trace.Account, Thread: thread}
	without := c
	without.Permissions.Remember = false
	if err := p.manager.memory.Observe(in, without, time.Now()); err != nil {
		p.manager.log("agent_send_memory_failed")
		return
	}
	if err := p.manager.memory.AcceptedDelivery(in, text, key, c, time.Now()); err != nil {
		p.manager.log("agent_send_memory_failed")
	}
}
