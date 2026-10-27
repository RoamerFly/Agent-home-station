package admin

import (
	"regexp"
	"strings"
	"wechat-bot/internal/agentrun"
	"wechat-bot/internal/companion"
)

func (p *permittedTools) ObserveFollowups(events []agentrun.Event) {
	for _, event := range events {
		if event.Thread == p.trace.Thread && event.Sender == p.trace.Sender && event.Kind == 1 {
			p.songFollowups = append(p.songFollowups, event.Text)
		}
	}
}

func (p *permittedTools) singingRequestText() string {
	text := p.trace.RequestedText
	if text == "" {
		text = companion.UserRequestText(p.trace.Text)
	}
	return strings.Join(append([]string{text}, p.songFollowups...), "\n")
}

var vocalGenderTag = regexp.MustCompile(`(?i)\b(?:fe)?male\s+(?:vocals?|singer|voice)\b|男声|女声`)
var maleSingingRequest = regexp.MustCompile(`(?i)(?:用|以|要|换成|改成|改用|采用|选择|希望|想听).{0,3}男声|男声(?:唱|演唱|来唱)|男生唱|男性演唱|^\s*男声\s*$|\bmale\s+(?:vocals?|singer|voice)\b`)
var compositionRequest = regexp.MustCompile(`原创|创作|写一首|写首|作词|作曲|\bcompose\b|\boriginal song\b`)
var quotedSongTitle = regexp.MustCompile(`《[^》]+》|“[^”]+”|"[^"]+"|「[^」]+」|『[^』]+』`)
var negatedRequest = regexp.MustCompile(`(?:不要|别|不用|不许|不准|禁止|不是|并非|无需|不需要|不).{0,3}$`)

const faithfulCoverStyle = "保持原曲调和原歌词 (preserve original melody and lyrics; follow the reference melody, rhythm and phrasing; no new melody or rewritten lyrics)"

func affirmativeRequest(pattern *regexp.Regexp, text string) bool {
	for _, match := range pattern.FindAllStringIndex(text, -1) {
		prefix := []rune(text[:match[0]])
		if len(prefix) > 8 {
			prefix = prefix[len(prefix)-8:]
		}
		if !negatedRequest.MatchString(string(prefix)) {
			return true
		}
	}
	return false
}

// Reject unsupported deviations instead of silently rewriting explicit tool
// parameters. The Agent receives the reason and can prepare a correct request.
func validateSingingRequest(text, mode, gender, maleQuote string) error {
	if gender == "male" {
		if maleQuote == "" || !strings.Contains(text, maleQuote) || !affirmativeRequest(maleSingingRequest, maleQuote) || !affirmativeRequest(maleSingingRequest, text) {
			return toolInputError("默认必须女声；原唱为男性不等于用户要求男声。仅用户明确要男声时填写vocal_gender_request逐字引用要求，否则改用vocal_gender=female后再提交；尚未生成")
		}
	}
	if mode == "original" && (strings.Contains(text, "唱") || strings.Contains(strings.ToLower(text), "cover")) && (quotedSongTitle.MatchString(text) || strings.Contains(text, "生日歌") || strings.Contains(text, "生日快乐")) && !affirmativeRequest(compositionRequest, text) {
		return toolInputError("当前是在要求演唱已有具名歌曲，不能用original重新作曲。请查找并下载对应原录音，以mode=cover、reference_asset_id和已核对的原歌词依据提交；风格须保持原曲调和原歌词。尚未生成")
	}
	return nil
}

func singingGenerationStyle(style, gender, mode string) string {
	if mode == "cover" && !strings.Contains(style, faithfulCoverStyle) {
		style = strings.TrimSpace(style) + ", " + faithfulCoverStyle
	}
	return normalizeSingingStyle(style, gender)
}

// Gender is a generation condition, not a guarantee about the rendered singer.
func normalizeSingingStyle(style, gender string) string {
	style = strings.Trim(vocalGenderTag.ReplaceAllString(style, ""), " ,;，；\n\r\t")
	if style == "" {
		return ""
	}
	return style + ", " + gender + " vocal"
}
