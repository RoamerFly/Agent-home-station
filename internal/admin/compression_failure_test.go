package admin

import (
	"errors"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
)

func TestCompressionFailureExplainsTruncationAndNeverLeaksSourceError(t *testing.T) {
	err := &deepseek.RequestError{Diagnostics: deepseek.Diagnostics{Code: "model_output_token_limit", HTTPStatus: 200, Attempts: 1}}
	code, message, status := compressionFailure(err)
	if code != "model_output_token_limit" || status != 502 || !strings.Contains(message, "截断") {
		t.Fatal("truncation hidden", code, message)
	}
	code, message, _ = compressionFailure(errors.New("SENSITIVE-LOCAL-DIAGNOSTIC"))
	if strings.Contains(message, "SENSITIVE") || code == "" {
		t.Fatal("source failure leaked")
	}
	if code, _, status := compressionFailure(companion.ErrContextChanged); code != "context_changed_during_compression" || status != 409 {
		t.Fatal("snapshot cause lost")
	}
}
