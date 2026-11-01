package admin

import (
	"context"
	"errors"
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

var maleSingingRequest = regexp.MustCompile(`(?i)(?:用|以|要|换成|改成|改用|采用|选择|希望|想听).{0,3}男声|男声(?:唱|演唱|来唱)|男生唱|男性演唱|^\s*男声\s*$|\bmale\s+(?:vocals?|singer|voice)\b`)
var compositionRequest = regexp.MustCompile(`原创|创作|写一首|写首|作词|作曲|\bcompose\b|\boriginal song\b`)
var quotedSongTitle = regexp.MustCompile(`《[^》]+》|“[^”]+”|"[^"]+"|「[^」]+」|『[^』]+』`)
var negatedRequest = regexp.MustCompile(`(?:不要|别|不用|不许|不准|禁止|不是|并非|无需|不需要|不).{0,3}$`)

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
		return toolInputError("当前是在要求演唱已有具名歌曲，默认用music_rvc_start给原录音直接换声；明确要求YuE参考重新合成时才用mode=cover、reference_asset_id和已核对的原歌词依据。不能用original重新作曲冒充原曲翻唱。尚未生成")
	}
	return nil
}

func (p *permittedTools) prepareSingingStyle(ctx context.Context, style, gender, mode string) (string, error) {
	var result struct {
		Style string `json:"style"`
	}
	err := p.prepareMusic(ctx, "/health", map[string]any{"action": "style", "style": style, "gender": gender, "mode": mode}, &result, func() error {
		if len(result.Style) > 4096 || strings.ContainsRune(result.Style, '\x00') {
			return errors.New("music_style_result_invalid")
		}
		return nil
	})
	return result.Style, err
}
