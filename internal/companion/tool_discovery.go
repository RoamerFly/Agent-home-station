package companion

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	"wechat-bot/internal/mcptools"
)

func exposeTool(active []mcptools.Tool, t mcptools.Tool) []mcptools.Tool {
	for _, existing := range active {
		if existing.Name == t.Name {
			return active
		}
	}
	return append(active, t)
}

// An empty last page is not a cache miss when earlier pages contained results.
func emptyAssetSearch(result string) bool {
	if strings.TrimSpace(result) == "[]" {
		return true
	}
	var page struct {
		Total *int `json:"total"`
	}
	return json.Unmarshal([]byte(result), &page) == nil && page.Total != nil && *page.Total == 0
}

func mediaSearchContinuation(tool, result string, need ResourceNeed, turns []map[string]string) (string, string) {
	switch tool {
	case "media_recent":
		if emptyAssetSearch(result) && (need.Kind == "voice" || strings.Contains(UserRequestText(currentMessage(turns)), "翻唱")) {
			return "web_audio_search", "当前会话的素材目录没有匹配项。若要获取公开作品，可直接按曲名和歌手搜索音源。"
		}
	case "web_search", "web_audio_search":
		if tool == "web_audio_search" || need.Fresh && need.Kind == "voice" {
			urls := map[string]bool{}
			rememberResourceURLs(urls, result)
			for raw := range urls {
				if mcptools.IsPublicVideoPage(raw) {
					return "web_media_download", "本次结果中有公开视频页面。核对作品和版本后可用 web_media_download 获取其音轨。"
				}
			}
		}
	}
	return "", ""
}

// Rank only the tools already authorized for this turn. A tool mentioning a
// sibling in its instructions should not displace that sibling's exact name.
func discoverToolMatches(tools []mcptools.Tool, query string) []mcptools.Tool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return tools
	}
	type hit struct {
		tool  mcptools.Tool
		score int
	}
	terms := memoryTerms(query)
	var hits []hit
	for _, tool := range tools {
		name, description := strings.ToLower(tool.Name), strings.ToLower(tool.Description)
		score := 0
		switch {
		case name == query:
			score = 1000
		case strings.Contains(name, query):
			score = 100
		case strings.Contains(description, query):
			score = 10
		}
		for _, term := range terms {
			// Single characters from a longer Chinese phrase are too common to
			// distinguish actions. Adjacent character pairs work without spaces.
			if utf8.RuneCountInString(term) < 2 {
				continue
			}
			if strings.Contains(name, term) {
				score += 4
			} else if strings.Contains(description, term) {
				score++
			}
		}
		if score != 0 {
			hits = append(hits, hit{tool, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	matches := make([]mcptools.Tool, 0, len(hits))
	for _, h := range hits {
		matches = append(matches, h.tool)
	}
	return matches
}
