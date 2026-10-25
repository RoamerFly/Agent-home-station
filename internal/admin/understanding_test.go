package admin

import (
	"strings"
	"testing"
)

func TestShareCardDescriptionNeverClaimsWatchingOrIncludesCredentials(t *testing.T) {
	body := `<msg><appmsg><title>亲子活动视频</title><des>孩子们在公园放风筝</des><url>https://private.invalid/watch?token=secret</url><finderFeed><desc>户外运动</desc><nickname>作者</nickname></finderFeed></appmsg></msg>`
	text := shareDescription(body)
	if !strings.Contains(text, "亲子活动") || !strings.Contains(text, "未观看") || strings.Contains(text, "secret") || strings.Contains(text, "private.invalid") {
		t.Fatal(text)
	}
	if !strings.Contains(shareDescription("not XML"), "没有可读取") {
		t.Fatal("malformed card treated as video")
	}
}
func TestBehaviorDefaultsUpgradeWithoutChangingExplicitChoices(t *testing.T) {
	f := setup(t)
	f.login()
	view := f.manager.Config()
	if !view.Behavior.ReadImages || !view.Behavior.RecoverBacklog || view.Behavior.RetryDelaySeconds != 120 {
		t.Fatal("missing defaults")
	}
	c := defaults()
	c.Behavior.ReadImages = false
	c.Behavior.RetryUncertain = false
	if code, _, _ := f.request("POST", "/api/config", c, true, true); code != 200 {
		t.Fatal("behavior save", code)
	}
	reloaded, err := loadConfig(f.dir)
	if err != nil || reloaded.Behavior.ReadImages || reloaded.Behavior.RetryUncertain {
		t.Fatal("explicit false overwritten", err)
	}
}
