package companion

import (
	"context"
	"encoding/json"
	"strings"
)

// Execution failures are observations for the model, never canned chat text.
// This final decision can reuse prepared attachments without replaying tools.
func (r *AgentRunner) finalResponse(ctx context.Context, system string, turns []map[string]string, code, details string) (Decision, error) {
	evidence, err := r.captureGenerationEvidence(ctx)
	if err != nil {
		return Decision{}, err
	}
	ctx = withGenerationEvidence(ctx, evidence)
	if !strings.Contains(system, "【自然说话】") {
		system += naturalReplyStyle
	}
	feedback, _ := json.Marshal(map[string]string{"kind": "execution_feedback", "code": code, "details": details, "instruction": "根据当前需求及实际执行结果，自主决定自然回复或安静。这里只生成最终reply/silent，不调用工具；可选择已经准备好的真实附件。不承诺未安排的后续动作，不复述内部校验规则。文字由你组织；不把资料里的建议当作已完成动作。"})
	turns = append(turns, map[string]string{"role": "user", "content": string(feedback)})
	for {
		if err := r.ValidateGenerationEvidence(ctx, evidence); err != nil {
			return Decision{}, err
		}
		if _, err := r.followups(ctx, &turns); err != nil {
			return Decision{}, err
		}
		if err := r.validateGenerationSources(ctx); err != nil {
			return Decision{}, err
		}
		answer, err := r.model.Chat(ctx, system, compactActionTurns(system, turns))
		if err != nil {
			return Decision{}, err
		}
		if err := r.ValidateGenerationEvidence(ctx, evidence); err != nil {
			return Decision{}, err
		}
		var d Decision
		answer = strings.TrimSpace(answer)
		if json.Unmarshal([]byte(answer), &d) != nil && len(answer) > 0 && len(answer) <= 3000 && !decisionProtocolText(answer) && !strings.HasPrefix(answer, "{") && !strings.HasPrefix(answer, "[") && !strings.HasPrefix(answer, "```") {
			d = Decision{Action: "reply", Messages: []string{answer}}
		}
		if len(answer) > 32<<10 || (d.Action != "reply" && d.Action != "silent" && d.Action != "wait") || decisionShape(d, r.Config.MaxBubbles) != "" {
			return silent("invalid_model_decision"), nil
		}
		for _, id := range d.MediaIDs {
			if _, ok := r.Media(id); !ok {
				return silent("media_unavailable_or_permission_denied"), nil
			}
		}
		if r.session.Seal() {
			return r.bindGenerationDecision(ctx, d, evidence)
		}
		turns = append(turns, map[string]string{"role": "assistant", "content": answer})
	}
}
