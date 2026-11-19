package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
)

type reminderSpec struct {
	Kind             string                  `json:"kind,omitempty"`
	ResultGroup      string                  `json:"result_group,omitempty"`
	GroundingChecked bool                    `json:"grounding_checked,omitempty"`
	Binding          *companion.ReplyContext `json:"reply_context,omitempty"`
	Due              time.Time               `json:"due_at"`
	Target           string                  `json:"target"`
	Text             string                  `json:"text"`
	QuoteMessageID   string                  `json:"quote_message_id,omitempty"`
	QuoteServerID    string                  `json:"quote_server_id,omitempty"`
	DelaySeconds     int64                   `json:"delay_seconds,omitempty"`
	RunAt            string                  `json:"requested_run_at,omitempty"`
	Job              string                  `json:"job_id,omitempty"`
	Attempts         int                     `json:"checks"`
	ServerID         string                  `json:"server_id,omitempty"`
	FollowUpTaskID   string                  `json:"follow_up_task_id,omitempty"`
}

func reminderTool() mcptools.Tool {
	return agentTool("agent_reminder", "kind=notify (default) sends the saved reminder text at the due time. kind=execute runs the requested tools silently and replies with results. Create, list, update or cancel YOUR OWN harmless reminder in this conversation. Available to ordinary friends/group members; no shell or arbitrary file permission needed. Creates a real JSON task file in a dedicated private reminders directory, resumes after restart, and when due while the workstation is running, appears as a reminder-class execution item for Agent processing without sending a separate reminder message or waiting for a WeChat receipt. If it became overdue while offline, it is marked needs_review for the owner to decide manually. Target=current conversation by default; destination=private may only send to the actual requester if present in the contact directory. notify only sends the reminder text. execute is owner-only and uses existing task/tool authorization. For a future action such as making a song and sending it to a group, use kind=execute with the full original request in text, and pass the exact WeChat group name in delivery_group; the host resolves the group independently and preserves its ID. No group messages are read to set the target. Use agent_clock for actual time; create requires text, request_key and exactly one of delay_seconds or run_at (RFC3339 with timezone). Return is scheduled, NOT processed. Always list reminders before creating another for an existing or referenced request. For a list, query filters titles and reminder text; follow next_offset while has_more. An inquiry about what another conversation said is read-only; report the saved reminder instead of recreating it. scope=account lets the trusted owner list or edit their own reminders across conversations. To edit an existing reminder, use action=update with the existing id and only changed fields (time, title, text, or delivery_group). An empty delivery_group returns an execute reminder to the source conversation. Ordinary members are restricted to their own reminders in the current conversation. Input {action:create|list|update|cancel,kind:notify|execute?,text?,request_key?,delay_seconds?,run_at?,destination:current|private?,delivery_group?:string,title?:string,scope:current|account?,query?:string,offset?:integer,limit?:integer,id?}.", `{"type":"object","properties":{"kind":{"type":"string","enum":["notify","execute"]},"action":{"type":"string","enum":["create","list","update","cancel"]},"text":{"type":"string","maxLength":2000},"request_key":{"type":"string","maxLength":128},"delay_seconds":{"type":"integer","minimum":1},"run_at":{"type":"string","maxLength":64},"destination":{"type":"string","enum":["current","private"]},"delivery_group":{"type":"string","maxLength":128},"title":{"type":"string","minLength":1,"maxLength":120},"scope":{"type":"string","enum":["current","account"]},"query":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1},"id":{"type":"string"}},"required":["action"],"additionalProperties":false}`)
}

func sameReminderFingerprint(t agentTask, account, sender, kind, target, resultGroup, text, runAt string, delay *int64) bool {
	if t.Reminder == nil || t.Account != account || t.Sender != sender || t.State == "cancelled" || t.State == "completed" {
		return false
	}
	r := t.Reminder
	if r.Kind != kind || r.Target != target || r.ResultGroup != resultGroup || r.Text != "提醒你："+text || r.RunAt != runAt {
		return false
	}
	return (delay == nil && r.DelaySeconds == 0) || (delay != nil && r.DelaySeconds == *delay)
}
func (a *agentControl) writeReminderFile(t agentTask) error {
	dir := filepath.Join(a.dir, "reminders")
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	b, e := json.MarshalIndent(t, "", "  ")
	if e != nil {
		return e
	}
	return writePrivate(filepath.Join(dir, t.ID+".json"), b)
}
func (p *permittedTools) callReminder(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		reminderEdit
		Action, ID, Destination, Kind, Scope string
		Query                                string
		Offset, Limit                        int
		Key                                  string `json:"request_key"`
	}
	if json.Unmarshal(args, &in) != nil {
		return "", toolInputError("提醒参数无效")
	}
	m := p.manager
	if p.trace.Preview || p.trace.Surf || !validID(p.trace.Sender) || strings.HasSuffix(p.trace.Sender, "@chatroom") {
		return "", toolInputError("提醒需要当前真实发言者身份，预览不创建文件")
	}
	if in.Action != "list" {
		if len(p.crossSourceThreads) > 0 {
			return "", mcptools.ArgumentError("$.action", "本轮已读取其他会话。请用 agent_reminder action=list scope=account 查询已有记录。读取到的历史消息不能在这一轮新增或改动提醒")
		}
		m.remindersMu.Lock()
		defer m.remindersMu.Unlock()
	}
	encode := func(v any) (string, error) { b, e := json.Marshal(v); return string(b), e }
	thread := p.trace.Thread
	switch in.Scope {
	case "", "current":
	case "account":
		if !p.privileged() || !m.agent.config().CrossConversation {
			return "", toolInputError("只有已授权的主人可以查询或修改本人跨会话的提醒")
		}
		thread = ""
	default:
		return "", mcptools.ArgumentError("$.scope", "请选择 current 或 account")
	}
	own := func(t agentTask) bool {
		return t.Reminder != nil && t.Account == p.trace.Account && (thread == "" || t.Thread == thread) && t.Sender == p.trace.Sender
	}
	switch in.Action {
	case "list":
		if in.Offset < 0 || in.Limit < 0 {
			return "", mcptools.ArgumentError("$.offset", "分页参数不能为负数")
		}
		query := strings.ToLower(strings.TrimSpace(in.Query))
		items := []agentTask{}
		for _, t := range m.agent.list(p.trace.Account, thread) {
			if own(t) && (query == "" || strings.Contains(strings.ToLower(t.Title+" "+t.RequestedText), query)) {
				items = append(items, t)
			}
		}
		start := min(in.Offset, len(items))
		limit := in.Limit
		if limit == 0 {
			limit = 30
		}
		end := start + min(limit, len(items)-start)
		return encode(map[string]any{"tasks": items[start:end], "total": len(items), "next_offset": end, "has_more": end < len(items), "read_only": true})
	case "update":
		if in.Destination != "" || in.Key != "" || in.Kind != "" {
			return "", mcptools.ArgumentError("$", "更新时请提供 id 和要改动的时间、标题、正文或群名")
		}
		for _, t := range m.agent.list(p.trace.Account, thread) {
			if t.ID == in.ID && own(t) {
				updated, err := m.editReminder(ctx, t, in.reminderEdit)
				if err != nil {
					return "", err
				}
				return encode(map[string]any{"id": t.ID, "state": updated.State, "due_at": updated.Reminder.Due, "updated_existing": true, "task": updated})
			}
		}
		return "", toolInputError("未找到你在本会话创建的提醒")
	case "cancel":
		for _, t := range m.agent.list(p.trace.Account, thread) {
			if t.ID == in.ID && own(t) {
				if t.State == "completed" {
					return "", toolInputError("这条提醒已发送，不能取消或撤回")
				}
				t.State = "cancelled"
				t.LastResult = "用户取消，停止尚未提交的提醒；已提交的消息不能保证撤回"
				if e := m.agent.task(t); e != nil {
					return "", e
				}
				return encode(map[string]any{"state": t.State, "id": t.ID})
			}
		}
		return "", toolInputError("未找到你在本会话创建的提醒")
	case "create":
		if thread == "" {
			return "", mcptools.ArgumentError("$.scope", "新提醒应绑定当前发言会话")
		}
		if in.Kind == "" {
			in.Kind = "notify"
		}
		if in.Kind != "notify" && in.Kind != "execute" {
			return "", toolInputError("kind需为notify或execute")
		}
		if in.Kind == "execute" && !contains(m.agent.config().OwnerIDs, p.trace.Sender) {
			return "", toolInputError("执行任务需要主人授权")
		}
		resultGroup := ""
		if in.DeliveryGroup != nil && strings.TrimSpace(*in.DeliveryGroup) != "" {
			if in.Kind != "execute" || (in.Destination != "" && in.Destination != "current") {
				return "", toolInputError("群名只用于执行类提醒的成品投递")
			}
			var e error
			resultGroup, e = p.resolveNamedGroup(ctx, *in.DeliveryGroup)
			if e != nil {
				return "", e
			}
		}

		if in.Text == nil || strings.TrimSpace(*in.Text) == "" || len(*in.Text) > 2000 || in.Key == "" || len(in.Key) > 128 {
			return "", toolInputError("创建提醒需要简短正文和稳定request_key")
		}
		due, err := reminderDue(in.RunAt, in.Delay)
		if err != nil {
			return "", err
		}
		target := p.trace.Thread
		if in.Destination == "private" {
			target = p.trace.Sender
			ok, e := m.authorizeConversation(ctx, target)
			if e != nil {
				return "", toolInputError("暂时无法核对你的好友身份，未创建提醒")
			}
			if !ok {
				return "", toolInputError("私聊提醒需要先成为微信联系人；也可选择在当前会话提醒")
			}
		}
		if in.Destination != "" && in.Destination != "current" && in.Destination != "private" {
			return "", toolInputError("提醒只能发到当前会话或你本人的私聊")
		}
		id := agentID(p.trace.Account, p.trace.Thread, p.trace.Sender, p.trace.TraceID, "reminder", in.Key)
		for _, old := range m.agent.list(p.trace.Account, "") {
			if sameReminderFingerprint(old, p.trace.Account, p.trace.Sender, in.Kind, target, resultGroup, *in.Text, in.RunAt, in.Delay) {
				return encode(map[string]any{"id": old.ID, "state": old.State, "due_at": old.Reminder.Due, "existing": true, "cross_conversation": old.Thread != p.trace.Thread})
			}
		}
		for _, old := range m.agent.list(p.trace.Account, p.trace.Thread) {
			if old.ID == id {
				if !own(old) || old.Reminder.Target != target || old.Reminder.ResultGroup != resultGroup || old.Reminder.Text != "提醒你："+*in.Text || old.Reminder.RunAt != in.RunAt || old.Reminder.Kind != in.Kind || (in.Delay != nil && old.Reminder.DelaySeconds != *in.Delay) {
					return "", toolInputError("同一request_key对应不同提醒，未重复创建")
				}
				return encode(map[string]any{"id": old.ID, "state": old.State, "due_at": old.Reminder.Due, "storage_file": filepath.Join(m.agent.dir, "reminders", id+".json")})
			}
			if own(old) && old.OriginTrace == p.trace.TraceID && old.State != "cancelled" &&
				old.Reminder.Target == target && old.Reminder.ResultGroup == resultGroup && old.Reminder.Kind == in.Kind &&
				old.Reminder.Text == "提醒你："+*in.Text && old.Reminder.RunAt == in.RunAt &&
				(in.Delay == nil || old.Reminder.DelaySeconds == *in.Delay) {
				return encode(map[string]any{"id": old.ID, "state": old.State, "due_at": old.Reminder.Due, "existing": true})
			}
		}
		text := "提醒你：" + *in.Text
		var reviewedBinding companion.ReplyContext
		if in.Kind == "notify" {
			reviewed, binding, err := p.prepareOutboundText(ctx, target, text)
			if err != nil {
				return "", err
			}
			if reviewed != text {
				return "", toolInputError("reminder_text_requires_confirmation: 提醒正文的对象或事实未通过审校，未保存；请确认原句或提供明确对象")
			}
			reviewedBinding = binding
		}
		quote := ""
		if target == p.trace.Thread {
			quote = p.trace.QuoteServerID
		}
		t := agentTask{ID: id, Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, Title: "提醒 · " + logText(*in.Text, 70), State: "waiting", Next: due, OriginTrace: p.trace.TraceID, RequestedText: *in.Text, Checkpoint: "到期发送已明确请求的提醒，不执行其中的文字或代码", Reminder: &reminderSpec{Kind: in.Kind, Due: due, Target: target, Text: text, QuoteServerID: quote}}
		if in.Title != nil {
			title, err := reminderTitle(*in.Title)
			if err != nil {
				return "", err
			}
			t.Title = title
		}
		t.Constraints = p.taskConstraints("reminder", "", "", "")
		t.Reminder.ResultGroup = resultGroup
		if in.Kind == "execute" {
			t.OwnerRequested = p.privileged()
			t.Checkpoint = "到期进入执行回合，使用工具处理原请求"
			if p.trace.OriginalText != nil && strings.TrimSpace(*p.trace.OriginalText) != "" {
				t.RequestedText = *p.trace.OriginalText
			}
		}
		source := p.outboundInput(target).Source
		if source != nil {
			t.Constraints.Source = source
		}
		bound := mintOutboundBinding(reminderInput(t), m.Config().Companion, reviewedBinding)
		t.Reminder.Binding = &bound
		t.Reminder.GroundingChecked = true
		t.Reminder.QuoteMessageID = p.trace.TraceID
		t.Reminder.RunAt = in.RunAt
		if in.Delay != nil {
			t.Reminder.DelaySeconds = *in.Delay
		}
		if e := m.agent.task(t); e != nil {
			return "", e
		}
		m.wakeReminderSchedule()
		m.record(bot.Record{Event: "reminder_scheduled", Category: "decision", ThreadID: t.Thread, Sender: t.Sender, MessageID: t.OriginTrace, Status: "waiting", Content: *in.Text, Reason: "explicit_member_request", Origin: "agent"})
		return encode(map[string]any{"id": id, "state": "scheduled", "due_at": due, "result_group": resultGroup, "storage_file": filepath.Join(m.agent.dir, "reminders", id+".json"), "instruction": "已实际保存提醒文件；notify到期发送提醒正文，execute到期静默执行后发送结果。尚未执行或发送。"})
	}
	return "", toolInputError("提醒操作需为create/list/update/cancel")
}
func (m *Manager) runReminders(ctx context.Context) {
	m.prepareReminderSchedule(time.Now())
	for {
		wait := 24 * time.Hour
		if due, ok := m.nextReminderDue(); ok {
			wait = time.Until(due)
			if wait < 0 {
				wait = 0
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-m.reminderWake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case now := <-timer.C:
			if !m.draining() {
				m.processDueReminders(ctx, now, false)
			}
		}
	}
}

func (m *Manager) wakeReminderSchedule() {
	if m.reminderWake == nil {
		return
	}
	select {
	case m.reminderWake <- struct{}{}:
	default:
	}
}

// prepareReminderSchedule runs once when the worker starts. Future reminders
// stay persisted and are represented only by the next in-memory timer. A
// reminder already past due while the workstation was offline is deliberately
// not executed: it is surfaced for the owner to decide manually.
func (m *Manager) prepareReminderSchedule(now time.Time) {
	for _, t := range m.agent.list(m.native.Account(), "") {
		if t.Reminder == nil || (t.State != "waiting" && t.State != "pending" && t.State != "waiting_delivery") || now.Before(t.Reminder.Due) {
			continue
		}
		t.State = "needs_review"
		t.Next = now
		t.LastResult = "提醒在工作站离线期间已到期，未自动执行；等待主人在执行回合中手动决定"
		_ = m.agent.task(t)
		m.record(bot.Record{Event: "reminder_overdue", Category: "decision", ThreadID: t.Thread, Sender: t.Sender, MessageID: t.OriginTrace, Status: "needs_review", Content: t.RequestedText, Reason: "reminder_overdue_while_offline", Origin: "reminder"})
	}
}

func (m *Manager) nextReminderDue() (time.Time, bool) {
	var next time.Time
	for _, t := range m.agent.list(m.native.Account(), "") {
		if t.Reminder == nil || (t.State != "waiting" && t.State != "pending") {
			continue
		}
		if next.IsZero() || t.Reminder.Due.Before(next) {
			next = t.Reminder.Due
		}
	}
	return next, !next.IsZero()
}

// processDueReminders turns a live reminder into an execution-round message;
// it never submits a native WeChat message or waits for a delivery receipt.
func (m *Manager) processDueReminders(ctx context.Context, now time.Time, _ bool) {
	m.remindersMu.Lock()
	defer m.remindersMu.Unlock()
	for _, t := range m.agent.list(m.native.Account(), "") {
		if t.Reminder == nil || (t.State != "waiting" && t.State != "pending") || now.Before(t.Reminder.Due) {
			continue
		}
		followUpID, err := m.scheduleReminderFollowUp(t)
		if err != nil {
			t.State = "needs_review"
			t.LastResult = "提醒已到期，但后续执行任务创建失败：" + err.Error()
			_ = m.agent.task(t)
			continue
		}
		if followUpID == "" {
			t.State = "needs_review"
			t.Next = now
			t.LastResult = "提醒已到期，等待主人手动决定；普通成员提醒不会自动启动后续任务"
			_ = m.agent.task(t)
			continue
		}
		t.Reminder.FollowUpTaskID = followUpID
		t.State = "completed"
		t.LastResult = "提醒已进入执行回合，等待 Agent 读取并处理"
		if err := m.agent.task(t); err != nil {
			m.log("reminder_due_state_save_failed")
			continue
		}
		m.record(bot.Record{Event: "reminder_due", Category: "decision", ThreadID: t.Thread, Sender: t.Sender, MessageID: t.OriginTrace, Status: "pending", Content: t.RequestedText, Reason: "reminder_due_to_execution_round", Origin: "reminder"})
	}
}
func (m *Manager) tickReminders(ctx context.Context, now time.Time) {
	cfg := m.Config().Config
	if !cfg.Enabled || !cfg.Companion.Enabled || !cfg.Companion.Permissions.ReplyText || !m.agent.config().Enabled || !m.agent.config().TasksEnabled || !m.native.Ready() {
		return
	}
	native, ok := m.native.(nativeTools)
	if !ok {
		return
	}
	for _, t := range m.agent.list(m.native.Account(), "") {
		if t.Reminder == nil || (t.State != "waiting" && t.State != "pending" && t.State != "waiting_delivery") || now.Before(t.Next) || now.Before(t.Reminder.Due) {
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.Behavior.DelayMaxSeconds+30)*time.Second)
		sourceAllowed, e1 := m.authorizeConversation(checkCtx, t.Thread)
		targetAllowed, e2 := m.authorizeConversation(checkCtx, t.Reminder.Target)
		if e1 != nil || e2 != nil {
			cancel()
			return
		}
		if !sourceAllowed || !targetAllowed {
			t.State = "cancelled"
			t.LastResult = "原会话或目标已不再允许接收提醒"
			_ = m.agent.task(t)
			cancel()
			continue
		}
		var status linuxnative.ToolStatus
		var err error
		if t.Reminder.Job == "" {
			// Recover a submission whose HTTP reply/local checkpoint was lost.
			// Receipt facts win even if its source was subsequently forgotten.
			if existing, lookupErr := native.LookupSend(checkCtx, "reminder-"+t.ID); lookupErr == nil {
				if existing.ThreadID != t.Reminder.Target || existing.ID == "" {
					cancel()
					return
				}
				status = existing
			} else if !errors.Is(lookupErr, linuxnative.ErrToolNotFound) {
				cancel()
				return
			} else {
				access := &permittedTools{manager: m, trace: reminderInput(t)}
				if sourceErr := validateReminderSource(checkCtx, m, t); sourceErr != nil {
					t.State = "needs_review"
					t.LastResult = "提醒来源已更正、遗忘或身份不再有效，未新增发送"
					_, _ = m.agent.reminderUpdate(t)
					cancel()
					continue
				}
				if err = access.deliveryDelay(checkCtx); err == nil {
					m.agent.mu.Lock()
					active := m.agent.data.Tasks[t.ID].State != "cancelled"
					m.agent.mu.Unlock()
					if !active {
						cancel()
						continue
					}
					if err = validateReminderSource(checkCtx, m, t, t.Reminder.Text); err != nil {
						cancel()
						return
					}
					// The same key survives crashes between native submission and local save.
					request := linuxnative.ToolSend{ThreadID: t.Reminder.Target, Kind: "text", Text: t.Reminder.Text, IdempotencyKey: "reminder-" + t.ID}
					if t.Reminder.QuoteServerID != "" && t.Reminder.QuoteMessageID != "" {
						request.Quote = &linuxnative.ToolQuote{MessageID: t.Reminder.QuoteMessageID, ServerID: t.Reminder.QuoteServerID}
					}
					if err = m.checkWechatSubmission(checkCtx); err != nil {
						cancel()
						return
					}
					status, err = native.Submit(checkCtx, request)
				}
			}
		} else {
			status, err = native.ToolSendStatus(checkCtx, t.Reminder.Job)
			if err == nil && status.ID == t.Reminder.Job && status.ThreadID == t.Reminder.Target && status.State == "failed" && status.Retryable && !status.CancelRequested {
				if sourceErr := validateReminderSource(checkCtx, m, t); sourceErr != nil {
					t.State = "needs_review"
					t.LastResult = "提醒来源已变更，停止原任务重试"
					_, _ = m.agent.reminderUpdate(t)
					cancel()
					continue
				}
				if retry, ok := m.native.(interface {
					RetryMediaJob(context.Context, string) (linuxnative.ToolStatus, error)
				}); ok {
					if err = (&permittedTools{manager: m}).deliveryDelay(checkCtx); err == nil {
						m.agent.mu.Lock()
						active := m.agent.data.Tasks[t.ID].State != "cancelled"
						m.agent.mu.Unlock()
						if active {
							if err = validateReminderSource(checkCtx, m, t, t.Reminder.Text); err == nil {
								if err = m.checkWechatSubmission(checkCtx); err != nil {
									cancel()
									return
								}
								status, err = retry.RetryMediaJob(checkCtx, t.Reminder.Job)
							}
						}
					}
				}
			}
		}
		cancel()
		t.Reminder.Attempts++
		t.State = "waiting_delivery"
		t.Next = time.Now().Add(bot.DeliveryRetryBackoff(t.Reminder.Attempts))
		t.LastResult = "提醒尚未取得可验证回执，保留原发送任务继续核对"
		if err == nil && status.ThreadID == t.Reminder.Target && status.ID != "" && (t.Reminder.Job == "" || status.ID == t.Reminder.Job) {
			t.Reminder.Job = status.ID
			if status.CancelRequested {
				t.State = "cancelled"
				t.LastResult = "原发送已取消，停止提醒"
			}
			if status.State == "accepted" && status.ServerID != "" {
				t.Reminder.ServerID = status.ServerID
				t.State = "completed"
				t.LastResult = "提醒已取得微信接受回执"
			}
		}
		var followUpID string
		if t.State == "completed" {
			followUpID, err = m.scheduleReminderFollowUp(t)
			if err != nil {
				m.log("reminder_followup_save_failed")
			}
			if followUpID != "" {
				t.Reminder.FollowUpTaskID = followUpID
			}
		}
		// Do not overwrite a concurrent user cancellation with a late receipt.
		if updated, e := m.agent.reminderUpdate(t); e != nil {
			m.log("reminder_state_save_failed")
			return
		} else if !updated {
			return
		}
		if t.State == "completed" {
			m.record(bot.Record{Event: "message_sent", Category: "sent", ThreadID: t.Reminder.Target, Sender: t.Sender, MessageID: "reminder-" + t.ID, SendID: t.Reminder.Job, Content: t.Reminder.Text, Status: "accepted", Reason: "explicit_reminder_accepted", Origin: "reminder"})
		}
		return // One paced submission/status check per tick; no invocation quota.
	}
}

// scheduleReminderFollowUp turns an owner reminder into a real resumable
// Agent task. The reminder remains a registration/due record; the follow-up
// is the normal model/tool entry point and is shown as a reminder-class
// execution item.
func (m *Manager) scheduleReminderFollowUp(t agentTask) (string, error) {
	if t.Reminder == nil || t.Account != m.native.Account() || (t.Reminder.Kind != "notify" && !contains(m.agent.config().OwnerIDs, t.Sender)) {
		return "", nil
	}
	if t.Reminder.Kind == "execute" {
		if err := m.validateTaskSource(t); err != nil {
			return "", err
		}
	}
	delivery := t.Thread
	if t.Reminder.ResultGroup != "" {
		access := &permittedTools{manager: m, trace: reminderInput(t)}
		if t.Reminder.Kind != "execute" || !access.crossAllowed(t.Thread, t.Reminder.ResultGroup) {
			return "", errors.New("原定群聊的跨会话授权已变更")
		}
		delivery = t.Reminder.ResultGroup
	}
	id := agentID("reminder-followup", t.ID)
	if existing, ok := m.agent.lookupTask(id, t.Account, t.Thread); ok {
		if existing.State != "cancelled" && existing.State != "failed" {
			return id, nil
		}
	}
	var source *companion.SourceRef
	var reply *companion.ReplyContext
	if t.Constraints != nil {
		if t.Constraints.Source != nil {
			copy := *t.Constraints.Source
			source = &copy
		}
		if t.Constraints.ReplyContext != nil {
			copy := *t.Constraints.ReplyContext
			reply = &copy
		}
	}
	follow := agentTask{
		ID:             id,
		Account:        t.Account,
		Thread:         t.Thread,
		RequestThread:  t.Thread,
		SourceThread:   t.Thread,
		DeliveryThread: delivery,
		Sender:         t.Sender,
		ParentID:       t.ID,
		OwnerRequested: true,
		ReminderDue:    true,
		State:          "pending",
		Next:           time.Now().UTC(),
		OriginTrace:    t.OriginTrace,
		RequestedText:  t.RequestedText,
		Title:          "提醒到期后处理 · " + logText(t.RequestedText, 70),
		Checkpoint:     "提醒已到期。请重新读取当前上下文，按原请求创建或继续真正的多步骤任务，调用所需工具并核对发送回执；不能只回复已记住。",
		Constraints: &TaskConstraints{
			Purpose:        "reminder_followup",
			Account:        t.Account,
			Thread:         t.Thread,
			DeliveryThread: delivery,
			Requester:      t.Sender,
			Source:         source,
			ReplyContext:   reply,
		},
	}
	if t.Reminder.Kind == "notify" {
		follow.NotificationText = t.Reminder.Text
	}
	if follow.RequestedText == "" {
		follow.RequestedText = strings.TrimPrefix(t.Reminder.Text, "提醒你：")
	}
	if err := m.agent.task(follow); err != nil {
		return "", err
	}
	m.record(bot.Record{Event: "agent_task_saved", Category: "decision", ThreadID: follow.Thread, Sender: follow.Sender, MessageID: follow.OriginTrace, Status: "pending", Content: follow.Title, Reason: "reminder_due_followup", Origin: "agent"})
	return id, nil
}

// Compare-and-swap prevents a late receipt from resurrecting a cancelled task.
func (a *agentControl) reminderUpdate(t agentTask) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	old, ok := a.data.Tasks[t.ID]
	if !ok || old.State == "cancelled" || !old.Updated.Equal(t.Updated) {
		return false, nil
	}
	t.Updated = time.Now().UTC()
	a.data.Tasks[t.ID] = copyTask(t)
	if e := a.writeReminderFile(t); e != nil {
		a.data.Tasks[t.ID] = old
		return false, e
	}
	if e := a.persist(); e != nil {
		a.data.Tasks[t.ID] = old
		_ = a.writeReminderFile(old)
		return false, e
	}
	return true, nil
}

// rescheduleTask updates the existing checkpoint atomically so stale workers
// cannot overwrite a newer schedule. No message is sent by this operation.
func (m *Manager) rescheduleTask(t agentTask, due time.Time) error {
	if !due.After(time.Now()) {
		return errors.New("请选择未来的时间")
	}
	if t.State != "waiting" && t.State != "pending" && t.State != "needs_review" {
		return errors.New("只有尚未执行的任务可以改期")
	}
	if t.Delivery != nil || t.PluginJob != nil || t.ProcessID != "" || t.MusicJob != "" || len(t.PendingOperations) > 0 {
		return errors.New("任务已进入工具执行或发送阶段，不能改期")
	}
	t.Next = due
	if t.Reminder != nil {
		spec := *t.Reminder
		if spec.Job != "" || spec.FollowUpTaskID != "" {
			return errors.New("提醒已进入处理阶段，不能改期")
		}
		spec.Due, spec.RunAt, spec.DelaySeconds = due, due.Format(time.RFC3339), 0
		t.Reminder = &spec
		t.State = "waiting"
	} else {
		t.State = "pending"
	}
	t.LastResult = "已修改计划时间：" + due.Format(time.RFC3339)
	saved, err := m.agent.transitionSnapshot(t, false)
	if err != nil {
		return err
	}
	if !saved {
		return errors.New("任务状态已变化，请重新读取后修改")
	}
	m.wakeReminderSchedule()
	return nil
}
