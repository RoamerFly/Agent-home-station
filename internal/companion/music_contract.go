package companion

import (
	"context"
	"strings"
)

type musicPromiseVerifier interface {
	VerifyMusicPromise(context.Context, string, string) (bool, error)
}

func (e *Engine) unexecutedMusicPromise(ctx context.Context, request string, messages []string) (bool, error) {
	if claimsDeferredMusic(messages) {
		return true, nil
	}
	reply := strings.Join(messages, " ")
	relevant := false
	for _, word := range []string{"歌", "唱", "作曲", "音乐"} {
		relevant = relevant || strings.Contains(request+reply, word)
	}
	if checker, ok := e.model.(musicPromiseVerifier); ok && relevant {
		needs, err := checker.VerifyMusicPromise(ctx, request, reply)
		return needs, err
	}
	return false, nil
}

// This guards a promise to perform work, not the model's decision to join chat.
func claimsDeferredMusic(messages []string) bool {
	text := strings.Join(messages, " ")
	action := false
	for _, word := range []string{"生成一首", "生成歌曲", "制作歌曲", "做一首", "整一首", "编一首", "创作一首", "唱一首", "写一首", "录一首", "正在写歌", "正在作曲"} {
		action = action || strings.Contains(text, word)
	}
	later := false
	for _, word := range []string{"稍等", "等我", "一小会", "待会", "马上", "这就", "我来", "我整", "我给你", "正在"} {
		later = later || strings.Contains(text, word)
	}
	return action && later
}
