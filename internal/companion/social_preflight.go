package companion

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"
	"wechat-bot/internal/deepseek"
)

const socialPreflightPolicy = "判断这条普通群聊是否需要你参与。优先遵守 conversation_preferences；结合最近发言判断是否在回应你尚未完成的问题。闲聊、简短确认或别人的对话可以 silent；有你的待办、偏好要求或拿不准时选 continue。只返回 JSON：{\"action\":\"silent\"} 或 {\"action\":\"continue\"}。"

// A preflight never discards a message by matching its words. These checks
// select only ordinary social text; every selected message is still judged by
// the model. Requests and rich/quoted messages use the full agent directly.
func (e *Engine) socialPreflightEligible(input Input, scope Scope) bool {
	// The regular Chat interface may have a large output budget. Use this
	// extra call only when the provider can accept a request-scoped small one.
	capable, ok := e.model.(interface{ SupportsTaskReasoning() bool })
	if !ok || !capable.SupportsTaskReasoning() {
		return false
	}
	if input.Preview || input.Mode != "social" || !input.IsGroup || input.Directed || input.MustReply || input.ReviewRequired || input.GroupShareSummary ||
		input.HistoricalReview || input.BackgroundResult || input.ConversationDelivery || input.SourceOnlyContext || input.Surf || input.ReportOnly || input.SilentExecution ||
		input.ReplyContext != nil || input.QuotedSource != nil || input.QuoteSharedMessage || input.PrivateQuote != "" || input.QuotedLiteral != "" ||
		len(input.MentionedIDs) != 0 || len(input.Addressees) != 0 || len(input.IncomingMedia) != 0 || len(input.UnreadMessages) != 0 ||
		len(input.RecoveryToolResults) != 0 || input.RecoveryEvidence != nil || len(input.PlannedReviews) != 0 ||
		input.TaskID != "" || input.OriginTraceID != "" || input.RequestedText != "" || input.NotificationText != "" {
		return false
	}
	if input.Source != nil && input.Source.MessageType != 0 && input.Source.MessageType != 1 {
		return false
	}
	text := strings.TrimSpace(input.Text)
	if text == "" || looksLikeSocialRequest(text) {
		return false
	}
	// The next short answer may be the answer to the character's own question.
	// Let the full agent see it without an additional model call.
	for i := len(scope.Messages) - 1; i >= 0; i-- {
		turn := scope.Messages[i]
		if currentSocialTurn(turn, input) {
			continue
		}
		if turn.Role == "assistant" && looksLikeSocialRequest(turn.Text) {
			return false
		}
		break
	}
	return true
}

func looksLikeSocialRequest(text string) bool {
	text = strings.TrimSpace(text)
	if strings.ContainsAny(text, "?？") || NeedsQuestionReview(text) {
		return true
	}
	for _, term := range []string{"请问", "想问", "为什么", "怎么", "如何", "什么", "哪里", "哪儿", "谁", "多少", "几时", "几号", "是不是", "有没有", "能不能", "能否", "可不可以", "帮我", "给我", "发我", "提醒我", "继续做", "重试", "取消任务"} {
		if strings.Contains(text, term) {
			return true
		}
	}
	for _, prefix := range []string{"请", "帮", "查", "搜", "做", "唱", "画", "发", "看", "试", "生成", "转发", "总结", "修复", "处理", "继续", "停止", "取消", "提醒"} {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

func currentSocialTurn(turn Turn, input Input) bool {
	if turn.Role != "user" || turn.Sender != input.Sender {
		return false
	}
	if input.TraceID != "" && turn.TraceID == input.TraceID {
		return true
	}
	return input.TraceID == "" && turn.Text == input.Text
}

func socialPreflightRecent(scope Scope, input Input) []map[string]any {
	recent := make([]map[string]any, 0, 4)
	for i := len(scope.Messages) - 1; i >= 0 && len(recent) < 4; i-- {
		turn := scope.Messages[i]
		if currentSocialTurn(turn, input) || turn.RequestScoped || turn.Media != nil || len(turn.IncomingMedia) != 0 || turn.Role != "assistant" && turn.Role != "user" {
			continue
		}
		row := map[string]any{"role": turn.Role, "text": boundedText(turn.Text, 160)}
		if turn.Role == "user" {
			row["same_sender"] = turn.Sender != "" && turn.Sender == input.Sender
		}
		recent = append(recent, row)
	}
	for i, j := 0, len(recent)-1; i < j; i, j = i+1, j-1 {
		recent[i], recent[j] = recent[j], recent[i]
	}
	return recent
}

// socialPreflight returns decided=true only for a verified silent judgement.
// Continue, malformed output, provider failure, and changed evidence all use
// the original full decision path.
func (e *Engine) socialPreflight(ctx context.Context, input Input, scope Scope) (Decision, bool) {
	logOutcome := func(outcome string) {
		if e.ToolLog != nil {
			e.ToolLog("social_preflight_" + outcome)
		}
	}
	payload, err := json.Marshal(map[string]any{
		"conversation_preferences": scope.Preferences,
		"recent":                   socialPreflightRecent(scope, input),
		"current":                  input.Text,
	})
	if err != nil {
		logOutcome("fallback")
		return Decision{}, false
	}
	evidence, err := e.captureGenerationEvidence(ctx)
	if err != nil || e.validateGenerationInputSource(ctx, input) != nil {
		logOutcome("fallback")
		return Decision{}, false
	}
	preflightCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	preflightCtx = deepseek.WithPurpose(e.modelRequestContext(preflightCtx, evidence), "social_attention_preflight")
	preflightCtx, err = deepseek.WithTaskReasoning(preflightCtx, deepseek.TaskReasoning{Mode: "off", MaxTokens: 256})
	if err != nil {
		logOutcome("fallback")
		return Decision{}, false
	}
	answer, err := e.model.Chat(preflightCtx, socialPreflightPolicy, []map[string]string{{"role": "user", "content": string(payload)}})
	if err != nil || e.ValidateGenerationEvidence(ctx, evidence) != nil || e.validateGenerationInputSource(ctx, input) != nil {
		logOutcome("fallback")
		return Decision{}, false
	}
	var verdict struct {
		Action   string   `json:"action"`
		Messages []string `json:"messages,omitempty"`
		Reason   string   `json:"reason,omitempty"`
	}
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(answer)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&verdict) != nil || len(verdict.Messages) != 0 || decoder.Decode(new(any)) != io.EOF {
		logOutcome("fallback")
		return Decision{}, false
	}
	switch verdict.Action {
	case "silent":
		logOutcome("silent")
		return silent("social_attention_silent"), true
	case "continue":
		logOutcome("continue")
	default:
		logOutcome("fallback")
	}
	return Decision{}, false
}
