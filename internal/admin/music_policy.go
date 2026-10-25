package admin

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"wechat-bot/internal/companion"
)

var musicTimePattern = regexp.MustCompile(`(\d{1,3}|一|二|两|三|四|五|六|七|八|九|十)(分钟|秒钟|秒)`)

func musicRequestedSeconds(text string) int {
	text = companion.UserRequestText(text)
	if strings.Contains(text, "半分钟") {
		return 30
	}
	if strings.Contains(text, "一分半") {
		return 90
	}
	m := musicTimePattern.FindStringSubmatch(text)
	if len(m) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	if n == 0 {
		n = map[string]int{"一": 1, "二": 2, "两": 2, "三": 3, "四": 4, "五": 5, "六": 6, "七": 7, "八": 8, "九": 9, "十": 10}[m[1]]
	}
	if m[2] == "分钟" {
		n *= 60
	}
	return n
}
func musicFileRequested(text string) bool {
	text = companion.UserRequestText(text)
	for _, word := range []string{"不要文件", "不用文件", "别发文件", "不发文件"} {
		if strings.Contains(text, word) {
			return false
		}
	}
	for _, word := range []string{"原文件", "音频文件", "音乐文件", "发文件", "发送文件", "文件发", "文件形式", "要文件", "原音频"} {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}
func musicKindFromText(text, requested string) string {
	text = companion.UserRequestText(text)
	if musicFileRequested(text) {
		noVoice := false
		for _, s := range []string{"不用语音", "不要语音", "别发语音", "只发文件", "只要文件"} {
			noVoice = noVoice || strings.Contains(text, s)
		}
		if !noVoice && (requested == "both" || strings.Contains(text, "唱给我") || strings.Contains(text, "唱给我们") || strings.Contains(text, "语音也") || strings.Contains(text, "语音和") || strings.Contains(text, "语音并") || strings.Contains(text, "先发语音")) {
			return "both"
		}
		return "file"
	}
	return "voice"
}

func (p *permittedTools) musicKindPermitted(kind string) bool {
	perms := p.manager.Config().Companion.Permissions
	if kind == "both" {
		return perms.SendVoice && perms.SendFile
	}
	return companion.MediaPermitted(perms, kind)
}
func (p *permittedTools) musicDeliveryKind(requested string) string {
	if !p.manager.Config().Companion.Permissions.SendVoice {
		return "file"
	}
	if p.trace.TaskID != "" {
		for _, task := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
			if task.ID == p.trace.TaskID {
				scope := p.manager.memory.Context(p.trace, p.manager.Config().Companion, time.Now())
				for i := len(scope.Messages) - 1; i >= 0; i-- {
					turn := scope.Messages[i]
					if turn.Role != "user" || turn.Sender != task.Sender {
						continue
					}
					if musicFileRequested(turn.Text) {
						return musicKindFromText(turn.Text, task.MusicKind)
					}
					if strings.Contains(turn.Text, "语音") && (strings.Contains(turn.Text, "发") || strings.Contains(turn.Text, "不用文件")) {
						return "voice"
					}
					break
				}
				if task.MusicKind != "" {
					return task.MusicKind
				}
			}
		}
	}
	if p.trace.Text == "" && (requested == "file" || requested == "both") {
		return requested
	}
	return musicKindFromText(p.trace.Text, requested)
}
