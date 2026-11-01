package mediaspec

import "errors"

type Failure struct {
	Status int
	Code   string
}

func (e *Failure) Error() string {
	if e.Code == "ASR_RESPONSE_HAVE_NO_WORDS" {
		return "音频中未识别到可转写的词；可能为音乐、环境声或不清晰人声"
	}
	return "百炼调用失败：" + e.Code + "（请检查地域、模型权限或额度；不会自动重复生成）"
}

// No recognized words is an observation outcome, not an authentication,
// transport, or decoder failure. Do not infer complete silence from this code.
func IsNoSpeech(err error) bool {
	var failure *Failure
	return errors.As(err, &failure) && failure.Code == "ASR_RESPONSE_HAVE_NO_WORDS"
}
