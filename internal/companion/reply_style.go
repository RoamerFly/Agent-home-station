package companion

import (
	"regexp"
	"strings"
	"unicode"
)

var chatExpression = regexp.MustCompile(`\[[\p{Han}]{1,6}\]`)

// Only describe patterns in accepted replies. Never copy conversation text,
// people, or asset identifiers into a potentially narrower model context.
func recentReplyStyle(scope Scope, in Input) []string {
	if !in.IsGroup || in.SourceOnlyContext {
		return nil
	}
	seen := map[string]string{}
	repeats := map[string]bool{}
	deliveries := map[string]bool{}
	for i := len(scope.Messages) - 1; i >= max(0, len(scope.Messages)-24) && len(deliveries) < 4; i-- {
		t := scope.Messages[i]
		if t.Role != "assistant" || t.Media != nil || t.DeviceObservation != nil || t.DeliveryID == "" || deliveries[t.DeliveryID] || maskedTurn(t) {
			continue
		}
		s := strings.TrimSpace(boundedText(t.Text, 768))
		if s == "" {
			continue
		}
		deliveries[t.DeliveryID] = true
		// Count different accepted bubbles, not repetitions within one message.
		mark := func(kind, fragment string) {
			key := kind + "/" + fragment
			if id := seen[key]; id != "" && id != t.DeliveryID {
				repeats[kind] = true
			}
			seen[key] = t.DeliveryID
		}
		r := []rune(s)
		if len(r) >= 2 && r[0] == r[1] && unicode.IsLetter(r[0]) {
			mark("opening", string(r[:2]))
		} else if head, _, found := strings.Cut(s, "，"); found && len([]rune(head)) <= 5 && head != "" {
			mark("opening", head)
		}
		for _, match := range chatExpression.FindAllString(s, -1) {
			mark("expression", match)
		}
		for _, part := range strings.FieldsFunc(chatExpression.ReplaceAllString(s, " "), func(r rune) bool { return !unicode.Is(unicode.Han, r) }) {
			chars := []rune(part)
			for j := 5; j <= len(chars); j++ {
				mark("phrasing", string(chars[j-5:j]))
			}
			for j, ch := range chars {
				if ch == '得' && j > 0 && j+1 < len(chars) {
					mark("phrasing", string(chars[j-1:j+1]))
				}
			}
		}
	}
	var hints []string
	for _, k := range []string{"opening", "expression", "phrasing"} {
		if repeats[k] {
			hints = append(hints, k)
		}
	}
	return hints
}

// Guidance for model-authored speech, shared by ordinary replies, reviews and
// final execution feedback. No substring filter or canned reply rewrites output.
const naturalReplyStyle = `
【自然说话】
像熟悉朋友直接回应当前事；普通进度一两句即可。群聊创作只讲作品与约定，制作方法在被问起时解释。确认角色演唱完成后可用第一人称说“唱给你听”。读过视频后先概括主要内容与关键细节，再按需接话。等待、旁观等内部打算留在 action/reason；选择安静就 silent。`
