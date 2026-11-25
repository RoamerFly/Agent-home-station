package bot

import (
	"context"
	"strings"
	"time"
	"wechat-bot/internal/companion"
)

type SurfTask struct {
	// Issued only by the host scheduler. It is never restored from model/task
	// JSON and gives the observer its own narrowly scoped readonly tool adapter.
	PrivateBrowseTools func(context.Context, string) (companion.ToolAccess, error) `json:"-"`
	Topic, Thread      string
	PreferredKind      string
	ForceShare         bool
	Manual             bool
	QuietDeadline      time.Time
	// The private observer can use its host scheduler's own time zone and
	// quiet hours instead of inheriting the conversation policy.
	PrivateQuietHours *companion.Config `json:"-"`
	PrivateToolCalls  int               `json:"-"`
	PrivateMayShare   bool              `json:"-"`
	PrivateAppBrowse  bool              `json:"-"`
}

func (w *Worker) tickSurf(ctx context.Context) bool {
	if w.generation != nil && w.generation.occupied() > 0 {
		return false
	}
	c, ok := w.model.(*companion.Engine)
	if !ok || w.config.SurfTask == nil || ctx.Err() != nil {
		return false
	}
	if w.runtime != nil {
		for _, d := range w.runtime.Media {
			if d.Input.Surf && !d.terminal() {
				return false
			}
		}
		for _, r := range w.runtime.MediaReplies {
			if r.Input.Surf && !r.terminal() {
				return false
			}
		}
	}
	task, err := w.config.SurfTask(time.Now())
	if err != nil {
		w.config.Log("surf_schedule_failed")
		return false
	}
	if task == nil {
		return false
	}
	trace := "surf-" + time.Now().UTC().Format(time.RFC3339Nano)
	if !task.QuietDeadline.IsZero() && w.generation == nil {
		bounded, cancel := context.WithDeadline(ctx, task.QuietDeadline)
		defer cancel()
		parent := ctx
		ctx = bounded
		defer func() {
			if parent.Err() == nil && bounded.Err() != nil && !time.Now().Before(task.QuietDeadline) {
				w.activity(Record{Event: "surf_review", Category: "tool", ThreadID: task.Thread, MessageID: trace, Origin: "surf", Status: "blocked", Reason: "quiet_hours"})
			}
		}()
	}
	input := companion.Input{Channel: w.config.Channel, Account: w.source.Account(), Thread: task.Thread, Mode: "proactive", Surf: true, TraceID: trace, IsGroup: strings.HasSuffix(task.Thread, "@chatroom"), Text: task.Prompt()}
	input.SurfTopic = task.Topic
	input.SurfPreferredKind = task.PreferredKind
	input.SurfForceShare = task.ForceShare
	reason := "autonomous_surf"
	if task.Manual {
		reason = "owner_requested_surf"
	}
	w.activity(Record{Event: "surf_started", Category: "tool", ThreadID: task.Thread, MessageID: trace, Content: task.Topic, Origin: "surf", Status: "running", Reason: reason})
	if task.Thread != "" && w.allowed[task.Thread] {
		if w.generation != nil {
			w.startGeneration(ctx, c, input, nil, task.QuietDeadline)
		} else {
			w.respondCharacter(ctx, c, input)
		}
		return true
	}
	preview, err := c.IsolatedPreview()
	if err != nil {
		return true
	}
	input.Mode = "direct"
	input.Preview = true
	input.Channel = "host"
	input.Thread = "host:surf:" + input.Account
	input.SurfForceShare = false
	// An arbitrary isolated preview never inherits normal account tools. Only
	// this claimed host factory may supply the observer's readonly authority.
	preview.Tools = nil
	// This empty, nonpersistent observer may produce a private text decision.
	// Host sending permissions remain unchanged; Preview never enters delivery.
	preview.Config.Permissions = companion.Permissions{ReplyText: true}
	if quiet := task.PrivateQuietHours; quiet != nil {
		preview.Config.Timezone = quiet.Timezone
		preview.Config.QuietStart, preview.Config.QuietEnd = quiet.QuietStart, quiet.QuietEnd
	}
	if task.PrivateToolCalls > 0 {
		preview.MaxToolCalls = task.PrivateToolCalls
	}
	if task.PrivateBrowseTools != nil {
		preview.Tools, err = task.PrivateBrowseTools(ctx, trace)
		if err != nil {
			w.config.Log("surf_private_origin_failed")
			return true
		}
	}
	if w.generation != nil {
		w.startGeneration(ctx, preview, input, nil, task.QuietDeadline)
		return true
	}
	previewCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	if scoped, ok := preview.Tools.(interface {
		BindSurfBrowseContext(context.Context) (context.Context, context.CancelFunc, error)
	}); ok {
		cancel()
		previewCtx, cancel, err = scoped.BindSurfBrowseContext(ctx)
		if err != nil {
			w.config.Log("surf_private_origin_failed")
			return true
		}
	}
	d, err := preview.Respond(previewCtx, input)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return true
		}
		w.activity(Record{Event: "surf_review", Category: "error", MessageID: trace, Origin: "surf", Status: "failed", Reason: "surf_model_failed"})
		return true
	}
	status, reason := privateSurfReview(d)
	w.activity(Record{Event: "surf_review", Category: "tool", MessageID: trace, Origin: "surf", Status: status, Content: strings.Join(d.Messages, "\n"), Reason: reason})
	return true
}

func privateSurfReview(d companion.Decision) (string, string) {
	// Only the host review adapter can set this non-JSON field. A model's
	// reason string alone never proves delivery.
	switch d.HostSurfShareState {
	case "accepted", "prepared", "queued", "pending", "running", "unknown", "failed", "rejected", "cancelled":
		return d.HostSurfShareState, "surf_share_" + d.HostSurfShareState
	}
	return "not_shared", "surf_observe_only_or_no_share_target"
}

func (t SurfTask) Prompt() string {
	if t.Thread == "" {
		if t.PrivateAppBrowse {
			return "独立网上冲浪：围绕“" + t.Topic + "”探索公开网页或surf_app可用应用。先查看targets，再按需读取内容；值得互动时使用本轮工具，等待任务按原next_attempt_at续办。有微信分享目标时用reply、surf_item_id和surf_target_thread分享已读内容；否则silent。搜索与分享只使用公开内容。"
		}
		if t.PrivateMayShare {
			return "独立网上冲浪：围绕“" + t.Topic + "”发现并读取公开内容。值得分享时用reply、surf_item_id和surf_target_thread选择本轮目标，messages写有意思的地方；想收藏用surf_action=bookmark，不想分享用silent。搜索与分享只使用公开内容。"
		}
		return "私人冲浪与收藏，无分享目标。围绕“" + t.Topic + "”浏览公开来源，偏好" + t.PreferredKind + "。用web_discover、web_search和web_fetch按需发现并读取；感兴趣时用surf_action=bookmark和surf_item_id收藏。最终silent，搜索只使用公开内容。"
	}
	return "自主冲浪：围绕“" + t.Topic + "”寻找适合当前群的新内容，偏好" + t.PreferredKind + "。先挑候选，再读正文或准备媒体。分享时自然说出有意思的地方，附上具体来源；视频、音频、图片按实际可用素材选择。避免重复近期链接或话题；没有合适内容可安静结束。搜索只使用公开内容。"
}
