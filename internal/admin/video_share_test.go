package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
)

func TestLongVideoStagesClickableCardWithoutUploadingFile(t *testing.T) {
	f := setup(t)
	native := &fakeTools{fakeNative: f.native}
	f.manager.native = native
	p, _, e := f.manager.toolAccess()
	if e != nil {
		t.Fatal(e)
	}
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper"})
	result, e := p.stageVideoShare("https://www.bilibili.com/video/BV1qP4y187Lf/", "动画片段", 107000)
	if e != nil {
		t.Fatal(e)
	}
	var output struct {
		Media    []companion.GeneratedMedia `json:"media"`
		LinkOnly bool                       `json:"link_only"`
	}
	_ = json.Unmarshal([]byte(result), &output)
	if !output.LinkOnly || len(output.Media) != 1 || output.Media[0].Kind != "share" || !strings.Contains(output.Media[0].Description, "1分47秒") {
		t.Fatal(result)
	}
	item, ok := p.Media(output.Media[0].ID)
	if !ok || item.URL == "" || native.submits.Load() != 0 {
		t.Fatal("card sent while preparing")
	}
}
