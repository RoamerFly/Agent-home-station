package companion

import (
	"errors"
	"strings"
	"wechat-bot/internal/deepseek"
)

// ReviewFailure is a deliberately small, operator-facing account of why a
// protected reply was stopped. It never contains a draft, evidence text or a
// provider response body.
type ReviewFailure struct {
	Code           string                `json:"code"`
	Stage          string                `json:"stage"`
	ReviewState    string                `json:"review_state"`
	Detail         string                `json:"detail"`
	Request        *deepseek.Diagnostics `json:"request,omitempty"`
	UnitIndex      int                   `json:"unit_index,omitempty"`
	AtomIndex      int                   `json:"atom_index,omitempty"`
	ViolationKinds []string              `json:"violation_kinds,omitempty"`
}

type groundingRejectedError struct{ verdict GroundingVerdict }

func (*groundingRejectedError) Error() string { return "reply_grounding_final_check_rejected" }

// ReviewError keeps the underlying error available to internal callers while
// making the stable, safe diagnostic available to activity logging.
type ReviewError struct {
	Failure ReviewFailure
	cause   error
}

func (e *ReviewError) Error() string { return e.Failure.Code }
func (e *ReviewError) Unwrap() error { return e.cause }

func ReviewFailureFromError(err error) (ReviewFailure, bool) {
	var review *ReviewError
	if errors.As(err, &review) {
		return review.Failure, true
	}
	return ReviewFailure{}, false
}

func reviewFailure(state string, err error) ReviewFailure {
	f := ReviewFailure{Stage: "grounding_review", ReviewState: state, Code: "grounding_review_failed", Detail: "复核未完成，已停止发送未验证的回复"}
	// Only fixed host error codes may be exposed. Never surface arbitrary I/O,
	// provider or plugin error strings as an operator-facing review detail.
	if err != nil {
		hostDetails := map[string]string{
			"reply_grounding_evidence_identity_conflict":   "复核材料包含相同消息编号下不同的来源身份",
			"reply_grounding_evidence_version_conflict":    "复核材料包含同一来源的不同版本",
			"reply_grounding_evidence_projection_conflict": "复核材料中同一来源的正文不一致",
			"reply_grounding_candidate_empty":              "没有生成可发送的回复正文",
			"reply_context_count_mismatch":                 "回复与逐条对象的数量不匹配",
			"reply_source_identity_unavailable":            "原消息身份暂时无法核验",
			"reply_source_identity_conflict":               "原消息身份发生冲突",
			"reply_context_not_verified":                   "回复上下文尚未核验",
			"reply_context_fingerprint_mismatch":           "回复与已核验的上下文不匹配",
			"reply_source_proof_missing":                   "缺少原消息的来源证明",
			"reply_target_missing":                         "缺少明确的回复对象",
			"reply_target_not_grounded":                    "回复对象没有对应来源",
			"reply_retarget_requires_quote":                "切换回复对象时缺少必要引用",
			"reply_source_unavailable":                     "回复引用的来源不可用或已遗忘",
			"reply_source_revision_changed":                "回复引用的来源版本已变化",
			"assistant_statement_is_not_fact_source":       "不能将助手旧回复作为用户事实的依据",
			"reply_source_personal_media_requires_link":    "人物与素材之间缺少明确的来源关系",
			"reply_media_owner_or_source_mismatch":         "回复使用的素材与来源或上传者不匹配",
			"reply_grounding_repair_invalid":               "改稿返回不符合回复协议",
			"reply_grounding_repair_empty_message":         "改稿没有有效回复正文",
			"reply_route_unknown":                          "改稿选择了未经授权的回复路径",
			"reply_route_duplicate":                        "回复路径标识发生冲突",
		}
		if detail := hostDetails[err.Error()]; detail != "" {
			f.Code, f.Stage, f.Detail = err.Error(), "source_validation", detail+"，已停止发送"
		}
		if strings.HasPrefix(err.Error(), "model_context_window_exceeded:") {
			f.Code, f.Stage, f.Detail = "model_context_window_exceeded", "input_validation", "复核输入与输出预留超过已配置的模型窗口，尚未调用API；请分页或分批处理，正文没有被截断"
		}
	}
	if state == "final_check_rejected" || state == "repair_no_supported_reply" {
		f.Code, f.Detail = "grounding_review_rejected", "复核未通过：现有证据不足以支持该回复，已停止发送"
	}
	var rejected *groundingRejectedError
	if errors.As(err, &rejected) {
		for _, bubble := range rejected.verdict.Bubbles {
			for _, violation := range bubble.Violations {
				// Fixed host classifications only; never copy a model's prose.
				switch violation.Kind {
				case "invalid_evidence_quote", "invalid_nonfactual_classification", "unsupported_fact", "presupposition", "unsupported_relation", "media_process_commentary":
					f.ViolationKinds = append(f.ViolationKinds, violation.Kind)
				}
			}
		}
		f.ViolationKinds = uniqueSortedEvidence(f.ViolationKinds)
		for _, kind := range f.ViolationKinds {
			if kind == "media_process_commentary" {
				f.Detail = "改稿仍在说明读取或核验过程，没有形成自然的内容回复；本次总结未完成，未发送"
			}
			if kind == "invalid_evidence_quote" {
				f.Detail = "旧格式复核引用的原文与实际来源不匹配，检查未通过；不表示视频读取失败"
			}
			if kind == "invalid_nonfactual_classification" {
				f.Detail = "改稿后的复核仍有不一致的非事实标记，该句段未通过核验，已停止发送"
			}
		}
	}
	var changed *GenerationEvidenceChangedError
	if errors.As(err, &changed) || (err != nil && (err.Error() == "reply_grounding_evidence_changed_during_snapshot" || err.Error() == "reply_evidence_epoch_changed" || err.Error() == "reply_evidence_authorization_changed")) {
		f.Code, f.Detail = "grounding_review_context_invalidated", "复核期间来源、权限或上下文发生变化，已停止发送"
	}
	if d, ok := deepseek.ErrorDiagnostics(err); ok {
		copy := d
		f.Request = &copy
		f.Stage = "model_" + d.Stage
		f.Code = d.Code
		f.Detail = deepseek.UserMessage(err) + "；复核未完成，已停止发送"
	}
	var protocol interface{ ReviewProtocolCode() string }
	if errors.As(err, &protocol) && protocol.ReviewProtocolCode() != "" {
		f.Stage, f.Code = "review_protocol", protocol.ReviewProtocolCode()
		f.Detail = reviewProtocolDetail(f.Code)
	}
	var position *GroundingProtocolError
	if errors.As(err, &position) {
		f.UnitIndex, f.AtomIndex = position.UnitIndex, position.AtomIndex
	}
	return f
}

func reviewProtocolDetail(code string) string {
	details := map[string]string{
		"review_response_too_large": "复核响应超过安全大小限制，已停止发送", "review_invalid_encoding": "复核响应编码无效，已停止发送", "review_json_invalid": "复核响应不是有效 JSON，已停止发送", "review_schema_invalid": "复核响应字段不符合协议，已停止发送", "review_units_missing": "复核响应缺少逐句核验结果，已停止发送", "review_units_count_mismatch": "复核响应的句段数量不匹配，已停止发送", "review_unit_unknown": "复核响应引用了未知句段，已停止发送", "review_unit_duplicate": "复核响应重复核验句段，已停止发送", "review_atoms_missing": "复核响应缺少原子命题，已停止发送", "review_atom_span_invalid": "复核响应的文本范围无效，已停止发送", "review_atom_reason_missing": "复核响应缺少判定依据，已停止发送", "review_atom_supports_missing": "复核响应缺少证据字段，已停止发送", "review_atom_mode_invalid": "复核响应的命题类型无效，已停止发送", "review_support_reference_invalid": "复核响应引用了不存在的证据，已停止发送", "review_support_quote_invalid": "旧格式复核未正确提供来源原文，检查未完成；不表示视频读取失败", "review_nonfactual_invalid": "复核响应的非事实标记无效，已停止发送", "review_atom_claim_invalid": "复核响应的事实命题无效，已停止发送", "review_supported_without_evidence": "复核响应将命题标为已支持但未给证据，已停止发送", "review_atom_status_invalid": "复核响应的命题状态无效，已停止发送", "review_text_coverage_incomplete": "复核响应没有覆盖全部有效文本，已停止发送",
	}
	if detail := details[code]; detail != "" {
		return detail
	}
	return "复核响应不符合协议，已停止发送"
}
