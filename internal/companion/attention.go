package companion

import "strings"

// NeedsQuestionReview only requests a model judgement. It does not establish a
// verified mention or force a reply: "你" can address another person in a group.
func NeedsQuestionReview(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	for _, prefix := range []string{"请问", "麻烦问下", "问一下", "问下", "所以", "那", "请", "麻烦"} {
		text = strings.TrimSpace(strings.TrimPrefix(text, prefix))
	}
	if !strings.HasPrefix(text, "你") || strings.HasPrefix(text, "你们") {
		return false
	}
	if strings.ContainsAny(text, "?？") {
		return true
	}
	for _, term := range []string{"什么", "怎么", "哪些", "如何", "是否", "多少", "是谁", "叫什么", "能不能", "能否", "可不可以", "吗", "呢", "怎样"} {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}
