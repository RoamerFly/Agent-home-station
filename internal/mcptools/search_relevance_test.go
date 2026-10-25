package mcptools

import (
	"context"
	"strings"
	"testing"
)

func TestSearchUnrelatedSuccessFallsBackWithoutChangingSubject(t *testing.T) {
	calls := []string{}
	result, e := searchPublic(context.Background(), "超兽武装 动画 片段 视频", func(_ context.Context, raw string) ([]byte, error) {
		calls = append(calls, raw)
		if strings.Contains(raw, "bing.com") {
			return []byte(`<rss><channel><item><title>超星</title><link>https://example.org/a</link><description>汉字超</description></item></channel></rss>`), nil
		}
		return []byte(`<script><h3><a data-mdurl="http://127.0.0.1/x">假的超兽武装</a></h3></script><h3><a href="https://www.so.com/link?m=x" data-mdurl="https://www.bilibili.com/video/BV1qP4y187Lf/">【<em>超兽武装</em>】动画片段</a></h3>`), nil
	})
	if e != nil || len(calls) != 2 || !strings.Contains(result, "360 Search") || !strings.Contains(result, "bilibili.com/video/") || strings.Contains(result, "假的") {
		t.Fatal("relevance fallback failed", calls, result, e)
	}
}
func TestSearchRelevantPrimaryNeedsNoFallbackAndBadSecondaryFails(t *testing.T) {
	count := 0
	_, e := searchPublic(context.Background(), "超兽武装", func(context.Context, string) ([]byte, error) {
		count++
		return []byte(`<rss><channel><item><title>超兽武装动画</title><link>https://example.org/a</link></item></channel></rss>`), nil
	})
	if e != nil || count != 1 {
		t.Fatal("unnecessary fallback", e, count)
	}
	count = 0
	_, e = searchPublic(context.Background(), "超兽武装 视频", func(context.Context, string) ([]byte, error) {
		count++
		return []byte(`<rss><channel><item><title>无关内容</title><link>https://example.org/a</link></item></channel></rss>`), nil
	})
	if e == nil || count != 2 || !strings.Contains(FailureDescription(e), "无关") {
		t.Fatal("irrelevant result accepted", e, count)
	}
}

func TestEnglishSongTitleCannotMatchOnlyFirstWordDictionaryResults(t *testing.T) {
	for _, query := range []string{"Traveling Light song Joel Hanson audio", `"Traveling Light" Joel Hanson 在线试听`} {
		calls := 0
		result, err := searchPublic(context.Background(), query, func(_ context.Context, raw string) ([]byte, error) {
			calls++
			if strings.Contains(raw, "bing.com") {
				return []byte(`<rss><channel><item><title>Traveling 英语单词</title><link>https://example.org/dictionary</link></item></channel></rss>`), nil
			}
			return []byte(`<h3><a data-mdurl="https://example.org/traveling-light">Traveling Light - Joel Hanson</a></h3>`), nil
		})
		if err != nil || calls != 2 || strings.Contains(result, "dictionary") || !strings.Contains(result, "traveling-light") {
			t.Fatal(query, calls, result, err)
		}
	}
}

func TestSearchKeepsBothArtistAndSongInsteadOfOnlyArtistBiography(t *testing.T) {
	q := "Joel Hanson Traveling Light bilibili BV"
	if searchRelevant(q, []searchResult{{Title: "Joel Hanson", Description: "An American singer"}}) {
		t.Fatal("biography replaced the song request")
	}
	if !searchRelevant(q, []searchResult{{Title: "Traveling-Light", Description: "Joel Hanson & Sara Groves"}}) {
		t.Fatal("actual title and artist lost")
	}
}

func TestBroadAnimalDiscoveryDoesNotRequireLiteralCategoryPhrase(t *testing.T) {
	if !searchRelevant("可爱动物 日常趣事 2026年10月", []searchResult{{Title: "猫咪第一次看雪", Description: "宠物日常趣事"}}) {
		t.Fatal("animal example rejected because heading omitted broad category")
	}
	if searchRelevant("可爱动物 日常趣事 2026年10月", []searchResult{{Title: "债券市场与利率公告"}}) {
		t.Fatal("unrelated result admitted")
	}
}
