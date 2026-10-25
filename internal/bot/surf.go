package bot

import (
	"context"
	"strings"
	"time"
	"wechat-bot/internal/companion"
)

type SurfTask struct {
	Topic, Thread string
	PreferredKind string
	QuietDeadline time.Time
}

func (w *Worker) tickSurf(ctx context.Context) bool {
	if w.generation != nil && w.generation.occupied() > 0 {
		return false
	}
	c, ok := w.model.(*companion.Engine)
	if !ok || w.config.SurfTask == nil || ctx.Err() != nil {
		return false
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
	input.SurfPreferredKind = task.PreferredKind
	w.activity(Record{Event: "surf_started", Category: "tool", ThreadID: task.Thread, MessageID: trace, Content: task.Topic, Origin: "surf", Status: "running", Reason: "autonomous_surf"})
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
	input.Thread = "surf-observer"
	if w.generation != nil {
		w.startGeneration(ctx, preview, input, nil, task.QuietDeadline)
		return true
	}
	previewCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	d, err := preview.Respond(previewCtx, input)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return true
		}
		w.activity(Record{Event: "surf_review", Category: "error", MessageID: trace, Origin: "surf", Status: "failed", Reason: "surf_model_failed"})
		return true
	}
	w.activity(Record{Event: "surf_review", Category: "tool", MessageID: trace, Origin: "surf", Status: "not_shared", Content: strings.Join(d.Messages, "\n"), Reason: "surf_observe_only_or_no_share_target"})
	return true
}

func (t SurfTask) Prompt() string {
	return "这是获准的一次自主网上冲浪。发现有趣、适合当前群且未重复的内容，就是自然的分享理由；不必等群友提问或正在聊同一话题。围绕兴趣“" + t.Topic + "”找普通群友看得懂的轻松内容：可爱动物、日常趣事、动画短片、美食、好听音乐。不要默认找论文、量子理论、专业期刊或学术突破；除非群里当前明确在聊这些。像朋友分享喜欢的东西，直接说有意思在哪里，不用固定的“刚冲浪看到”“刚随手翻到”“群里有对X感兴趣的可以聊聊”开场结尾，也不要每条都反问或邀聊。注意最近已经分享过的内容和开头，别重复同一链接或话题。\n这次随机偏好形式：" + t.PreferredKind + "。video优先找真正可播放的视频，voice优先找可取得的音乐音频，image优先真实图片；应实际准备对应素材，不要只写文字说有视频或音乐。公开长视频优先可播放分享卡片。找不到合适素材可换候选或形式，不要把单个来源失败当作整次无法分享；确实无合适内容时仍可安静，不硬凑也不要求每次发。真实搜索并读取或下载核实，保留可靠的具体来源，不拿网站首页冒充文章或视频。不得把群聊、关系备注、私人身份或密钥放入搜索。"
}
