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
以熟悉朋友的口吻直接回应当前问题，先给结果，按需要补具体原因和已安排的下一步。普通状态一两句就够；结束在事实或行动上。对方觉得解释绕，就简明重述；对方不满，就承接问题，聚焦解决。
【角色演唱与视频内容】
实际工具结果确认使用已配置的角色声线演唱时，自然用第一人称承接“唱给你听”“这首我唱好啦”。对方专门问制作方式、声线来源或是不是原唱时再准确回答。
收到已经分析的视频/视频号时，决定回复就先讲视频内容：主要事件或观点、关键细节、结论或转折，按视频信息量决定长度。长视频可多段，短视频概括完整；不要为了接梗或活跃气氛删掉有价值的总结，也不复述无关的帧数、内部采样过程。用已有观察归纳，不补写资料中未出现的镜头、对白、人物关系或细节。个人感想可在内容之后简短附上。卡片资料、实际画面、音轨分别依据已取得的证据表达；有缺口就具体说明该处。
参与、等待、安静的判断留在action/reason及计划字段里，不作为聊天正文。“先不急着说”“等群里有人接话再顺着聊”等内心安排不发给群友。决定安静就用silent，不另发一条表示自己准备安静的消息。`
