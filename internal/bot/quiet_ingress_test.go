package bot

import (
	"context"
	"testing"
	"time"

	"wechat-bot/internal/companion"
)

func quietAroundNow(c *companion.Config) {
	loc, _ := time.LoadLocation(c.Timezone)
	now := time.Now().In(loc)
	c.QuietStart = now.Add(-time.Hour).Format("15:04")
	c.QuietEnd = now.Add(time.Hour).Format("15:04")
}

func TestQuietHoursFilterOnlyUnmentionedIncomingGroupMessages(t *testing.T) {
	w, c, _, _ := planWorker(t, &planReviewModel{})
	w.config.GroupRules = map[string]GroupRule{"room@chatroom": {Mode: "all"}}
	quietAroundNow(&c.Config)
	m := fixtureMessage("ordinary", "room@chatroom", "今天做什么", "incoming")
	if _, ok := w.prompt(m, time.Now()); ok || w.skipReason(m, time.Now()) != "quiet_unmentioned_group_message" {
		t.Fatal("non-@ group message entered during quiet hours")
	}
	mentioned := true
	m.Mentions.State, m.Mentions.Self = "known", &mentioned
	if text, ok := w.prompt(m, time.Now()); !ok || text != "今天做什么" {
		t.Fatal("verified @ message was blocked during quiet hours", text, ok)
	}
	c.Config.QuietStart, c.Config.QuietEnd = "00:00", "00:00"
	m.Mentions.State, m.Mentions.Self = "", nil
	if text, ok := w.prompt(m, time.Now()); !ok || text != "今天做什么" {
		t.Fatal("ordinary group message stayed blocked after quiet hours", text, ok)
	}
}

func TestAcceptedGroupReplyPlanSurvivesQuietBoundary(t *testing.T) {
	w, c, n, in := planWorker(t, &planReviewModel{})
	in.Mode = "social"
	w.queueUtterancePlan(c, in, companion.Decision{Action: "reply", Messages: []string{"已经接下的回复"}})
	quietAroundNow(&c.Config)
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	if len(n.submissions) != 1 || w.runtime.Plans[0].State != "accepted" {
		t.Fatal("accepted reply was discarded when quiet hours began", len(n.submissions), w.runtime.Plans[0].State)
	}
}

func TestAcceptedMediaCanFinishAfterQuietBoundary(t *testing.T) {
	w, _, c, in := mediaWorker(t)
	quietAroundNow(&c.Config)
	in.Mode = "social"
	d := &mediaDelivery{Input: in, Kind: "video"}
	if !w.mediaAllowed(d) {
		t.Fatal("received media was blocked after quiet hours began")
	}
	d.Input.Mode = "proactive"
	if w.mediaAllowed(d) {
		t.Fatal("new autonomous media bypassed quiet hours")
	}
}
