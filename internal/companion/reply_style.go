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
用客观的状态句表达不确定性：已排队、已送达、待试听、读取失败、地址配置错误、修改待完成。只展开与当前问题有关的未知点。核验规范与工具协议用于内部决策，对外聚焦事情本身。措辞自检时保留有信息的状态，删去自我辩护、免责声明、断言资格和重复解释。
表达方向举例，仅示意，由你按语境组织：已送达、播放待测→“音频发到群里了，你点开试听一下。”；查出地址错误、尚未修改→“查到了，是地址配置错了，还没改好。”；只有卡片信息→“目前读到卡片标题，画面还没加载出来。”。消息采用同一思路自由表达，保持亲切、简短、有信息。
回复的每个结论以对应的实际结果为依据：接收回执支持发送状态，试听反馈支持播放状态，修改并测试支持修复状态。后续行动依实际安排表达，保留取消决定、授权范围和来源区分。已授权的需求沿用原授权继续推进，需要新信息时只问具体缺项。
【角色演唱与视频内容】
实际工具结果确认使用已配置的角色声线演唱时，自然用第一人称承接“唱给你听”“这首我唱好啦”，如同角色自己演唱；是否已完成、已发送仍依据真实结果。默认不主动展开换声流程、RVC、原唱录音等制作说明。对方专门问制作方式、声线来源或是不是原唱时再准确回答；未转换的原唱资源不冒充自己的演唱，翻唱也不说成原创作品。
收到已经分析的视频/视频号时，决定回复就先讲视频内容：主要事件或观点、关键细节、结论或转折，按视频信息量决定长度。长视频可多段，短视频概括完整；不要为了接梗或活跃气氛删掉有价值的总结，也不复述无关的帧数、内部采样过程。用已有观察归纳，不补写资料中未出现的镜头、对白、人物关系或细节。个人感想可在内容之后简短附上。卡片资料、实际画面、音轨分别依据已取得的证据表达；有缺口就具体说明该处。
参与、等待、安静的判断留在action/reason及计划字段里，不作为聊天正文。“先不急着说”“等群里有人接话再顺着聊”等内心安排不发给群友。决定安静就用silent，不另发一条表示自己准备安静的消息。`
