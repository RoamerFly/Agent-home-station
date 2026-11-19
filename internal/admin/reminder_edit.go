package admin

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"wechat-bot/internal/mcptools"
)

type reminderEdit struct {
	Title           *string    `json:"title"`
	Text            *string    `json:"text"`
	DeliveryGroup   *string    `json:"delivery_group"`
	Delay           *int64     `json:"delay_seconds"`
	RunAt           string     `json:"run_at"`
	ExpectedUpdated *time.Time `json:"expected_updated"`
}

func reminderDue(runAt string, delay *int64) (time.Time, error) {
	if (delay == nil) == (runAt == "") {
		return time.Time{}, mcptools.ArgumentError("$.run_at", "请选择定时时间或延迟秒数其中一项")
	}
	now := time.Now()
	if delay != nil {
		if *delay < 1 || *delay > int64((1<<63-1)/time.Second) {
			return time.Time{}, mcptools.ArgumentError("$.delay_seconds", "延时需要是有效的正整数")
		}
		return now.Add(time.Duration(*delay) * time.Second), nil
	}
	due, err := time.Parse(time.RFC3339, runAt)
	if err != nil || !due.After(now) {
		return time.Time{}, mcptools.ArgumentError("$.run_at", "请选择包含时区的未来时间")
	}
	return due, nil
}

func reminderTitle(value string) (string, error) {
	title := strings.TrimSpace(value)
	if title == "" || utf8.RuneCountInString(title) > 120 {
		return "", mcptools.ArgumentError("$.title", "标题需为 1 至 120 字")
	}
	return title, nil
}

func (m *Manager) editReminder(ctx context.Context, t agentTask, edit reminderEdit) (agentTask, error) {
	if t.Reminder == nil || t.State != "waiting" && t.State != "pending" && t.State != "needs_review" ||
		t.Reminder.Job != "" || t.Reminder.FollowUpTaskID != "" || t.Delivery != nil || t.MusicJob != "" || len(t.PendingOperations) > 0 {
		return agentTask{}, toolInputError("只有尚未开始执行或投递的提醒可以修改")
	}
	if edit.ExpectedUpdated != nil && !t.Updated.Equal(*edit.ExpectedUpdated) {
		return agentTask{}, toolInputError("提醒已更新，请刷新后再修改")
	}
	if edit.Title == nil && edit.Text == nil && edit.DeliveryGroup == nil && edit.RunAt == "" && edit.Delay == nil {
		return agentTask{}, toolInputError("请至少填写一项要修改的字段")
	}
	t = copyTask(t)
	if edit.Title != nil {
		title, err := reminderTitle(*edit.Title)
		if err != nil {
			return agentTask{}, err
		}
		t.Title = title
	}
	if edit.Text != nil {
		text := strings.TrimSpace(*edit.Text)
		if text == "" || utf8.RuneCountInString(text) > 2000 {
			return agentTask{}, mcptools.ArgumentError("$.text", "正文需为 1 至 2000 字")
		}
		t.RequestedText = text
		t.Reminder.Text = "提醒你：" + text
		if t.Reminder.Kind != "execute" {
			if err := validateReminderSource(ctx, m, t); err != nil {
				return agentTask{}, mcptools.ArgumentError("$.text", "未通过原提醒会话的投递检查")
			}
		}
	}
	if edit.DeliveryGroup != nil {
		access := &permittedTools{manager: m, trace: reminderInput(t)}
		if t.Reminder.Kind != "execute" || !access.privileged() || !m.agent.config().CrossConversation {
			return agentTask{}, mcptools.ArgumentError("$.delivery_group", "只有获准的主人执行类提醒可修改目标群")
		}
		t.Reminder.ResultGroup = ""
		if group := strings.TrimSpace(*edit.DeliveryGroup); group != "" {
			id, err := access.resolveNamedGroup(ctx, group)
			if err != nil {
				return agentTask{}, mcptools.ArgumentError("$.delivery_group", err.Error())
			}
			t.Reminder.ResultGroup = id
		}
	}
	if edit.RunAt != "" || edit.Delay != nil {
		due, err := reminderDue(edit.RunAt, edit.Delay)
		if err != nil {
			return agentTask{}, err
		}
		t.Next, t.Reminder.Due = due, due
		t.Reminder.RunAt, t.Reminder.DelaySeconds = due.Format(time.RFC3339), 0
		t.State = "waiting"
	}
	t.LastResult = "已保存提醒修改"
	ok, err := m.agent.transitionSnapshot(t, false)
	if err != nil {
		return agentTask{}, err
	}
	if !ok {
		return agentTask{}, errors.New("提醒状态已改变，请刷新后修改")
	}
	m.wakeReminderSchedule()
	updated, _ := m.agent.lookupTask(t.ID, t.Account, t.Thread)
	return updated, nil
}
