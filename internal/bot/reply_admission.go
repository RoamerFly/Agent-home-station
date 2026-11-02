package bot

import (
	"strings"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/sending"
)

// Called only before a native Submit. A local proof failure is not an unknown
// network outcome. Publish fixed safe codes and preserve errors.As/Is causes.
func replyAdmissionFailure(err error) error {
	code := "reply_admission_failed"
	if f, ok := companion.ReviewFailureFromError(err); ok {
		code = f.Code
	} else if err != nil {
		candidate := err.Error()
		if len(candidate) <= 100 && (strings.HasPrefix(candidate, "reply_") || strings.HasPrefix(candidate, "legacy_reply_") || strings.HasPrefix(candidate, "ephemeral_reply_") || strings.HasPrefix(candidate, "private_reply_") || candidate == "generation_evidence_changed") && strings.IndexFunc(candidate, func(r rune) bool { return r != '_' && (r < 'a' || r > 'z') }) < 0 {
			code = candidate
		}
	}
	return &sending.OutcomeError{Cause: &replyAdmissionError{cause: err}, Reason: code}
}

type replyAdmissionError struct{ cause error }

func (*replyAdmissionError) Error() string        { return "reply_admission_failed" }
func (e *replyAdmissionError) Unwrap() error      { return e.cause }
func (*replyAdmissionError) Is(target error) bool { return target == sending.ErrRejected }
