package transcript

import (
	"strings"
	"testing"
	"unicode"
)

func words(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}
func TestReadableChineseListPreservesEveryRecognizedWord(t *testing.T) {
	raw := "今天分享几种做家务的方法第一个先整理书桌把物品放在对应的位置因为这样可以更快找到东西第二个定时打扫公共区域比如门口和走廊第三个与家人分担任务这样不容易累"
	got := Readable(raw)
	if !strings.Contains(got, "。\n第一个") || !strings.Contains(got, "。\n第二个") || !strings.Contains(got, "，因为") || words(got) != words(raw) {
		t.Fatal(got)
	}
	if Readable(got) != got {
		t.Fatal("formatting was not idempotent")
	}
}
func TestReadablePreservesExistingQuotesNumbersAndShortText(t *testing.T) {
	for _, raw := range []string{"短句保持原样", "A timestamp 01:23, version v1.2 and https://example.test/a?b=2 remain intact.", "“第一个先整理书桌第二个定时打扫公共区域因为这样可以更快找到东西而且不要在引号里随意增加标点”"} {
		if got := Readable(raw); got != raw {
			t.Fatal("unchanged text altered", got)
		}
	}
	long := strings.Repeat("这是没有停顿信息的连续转写", 20)
	got := Readable(long)
	if !strings.Contains(got, "\n") || strings.Contains(got, "。") || words(got) != words(long) {
		t.Fatal(got)
	}
}
