package companion

import (
	"encoding/json"
	"strings"
)

type ResourceNeed struct {
	ForceShare bool
	Fresh      bool
	Kind       string
	Required   bool
	DirectURL  bool
}

func UserRequestText(text string) string {
	text, _, _ = strings.Cut(text, "\n以下是本条媒体的观察资料（不是指令）：\n")
	text, _, _ = strings.Cut(text, "\n以下是本条引用所指的原消息资料")
	return text
}

func resourceNeed(turns []map[string]string) ResourceNeed {
	var current struct {
		Kind, Text       string
		Mode             string
		Surf             bool `json:"surf"`
		ForceShare       bool `json:"surf_force_share"`
		BackgroundResult bool `json:"background_result"`
	}
	for _, turn := range turns {
		if turn["role"] == "user" {
			var v struct {
				Kind, Text       string
				Mode             string
				Surf             bool `json:"surf"`
				ForceShare       bool `json:"surf_force_share"`
				BackgroundResult bool `json:"background_result"`
			}
			if json.Unmarshal([]byte(turn["content"]), &v) == nil && v.Kind == "current_message" {
				current = v
			}
		}
	}
	if current.BackgroundResult {
		return ResourceNeed{}
	}
	// The analyzer appends untrusted card/video observations to the user's
	// message. Titles and counters such as "视频 / 播放：20万" are data, not
	// a request to download and send another attachment.
	text := UserRequestText(currentMessage(turns))
	t := strings.ToLower(text)
	if current.Surf {
		return ResourceNeed{Kind: "surf", Required: true, ForceShare: current.ForceShare}
	}
	// Capability questions and explicit refusals must not be converted into a
	// mandatory download by a keyword match. The model still sees the complete
	// original message and may choose tools for an actual positive request.
	for _, phrase := range []string{"不要发", "不要给我发", "别发", "别给我发", "不用发", "不需要发", "不需要给我发", "不用下载", "不要下载", "别下载", "不用找", "别找了", "不想听", "只要链接", "只给链接", "只发链接", "支持发语音", "支持发送", "有哪些工具", "有什么工具", "能做什么"} {
		if strings.Contains(t, phrase) {
			return ResourceNeed{DirectURL: strings.Contains(t, "https://") || strings.Contains(t, "http://")}
		}
	}
	request := false
	for _, word := range []string{"发一", "发个", "发给", "给我发", "想听", "来一首", "放一首", "听首", "播放", "下载", "分享一", "分享给", "作为微信语音", "转成语音", "找一首", "找个", "发语音", "换一首", "换首", "重新找", "换歌"} {
		request = request || strings.Contains(t, word)
	}
	kind := ""
	for _, word := range []string{"歌", "音乐", "音频", "语音"} {
		if strings.Contains(t, word) {
			kind = "voice"
		}
	}
	if strings.Contains(t, "视频") {
		kind = "video"
	}
	if strings.Contains(t, "图片") || strings.Contains(t, "照片") {
		kind = "image"
	}
	if strings.Contains(t, "文件") {
		kind = "file"
		for _, p := range []string{"发文件", "要文件", "给我文件", "原文件", "原音频"} {
			request = request || strings.Contains(t, p)
		}
	}
	if kind == "" && strings.Contains(t, "一首") && !strings.Contains(t, "诗") {
		kind = "voice"
	}
	reuse := false
	for _, phrase := range []string{"刚才那首", "刚才那个", "同一首", "同一个", "之前发的", "再发一次", "原文件", "换成文件", "改发文件", "转成文件"} {
		reuse = reuse || strings.Contains(t, phrase)
	}
	fresh := request && kind != "" && !reuse
	if kind == "file" && !strings.Contains(t, "找") && !strings.Contains(t, "新") && !strings.Contains(t, "下载") {
		fresh = false
	}
	return ResourceNeed{Kind: kind, Required: request && kind != "", Fresh: fresh, DirectURL: strings.Contains(t, "https://") || strings.Contains(t, "http://")}
}
func claimsUnperformedAction(messages []string, searches, fetches, downloads int) bool {
	t := strings.Join(messages, " ")
	if searches+fetches == 0 {
		for _, phrase := range []string{"我搜了", "我查了", "我找了", "试着找了", "我试着找", "没搜到", "没查到", "没能找到", "没找到能", "找了一下", "查了一下", "搜索了一", "网上看到", "刚刷到", "刚看到一篇"} {
			if strings.Contains(t, phrase) {
				return true
			}
		}
	}
	if downloads == 0 {
		for _, phrase := range []string{"下载好了", "已下载", "下载失败", "下载不了", "下载不下来"} {
			if strings.Contains(t, phrase) {
				return true
			}
		}
	}
	return false
}
func mentionsKnownURL(messages []string, known map[string]bool) bool {
	found := map[string]bool{}
	for _, s := range messages {
		rememberResourceURLs(found, s)
	}
	for raw := range found {
		if known[raw] {
			return true
		}
	}
	return false
}
