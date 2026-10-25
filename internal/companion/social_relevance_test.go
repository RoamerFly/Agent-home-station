package companion

import (
	"context"
	"testing"
)

func TestSocialReplyOrSilenceIsChosenByModelWithoutMandatoryLabels(t *testing.T) {
	for _, tc := range []struct{ mode, answer, want string }{
		{"social", `{"action":"reply","messages":["娱乐兴趣才是正经兴趣"]}`, "reply"},
		{"social", `{"action":"reply","messages":["哈哈没事"],"engagement":"just_chat"}`, "reply"},
		{"social", `{"action":"silent","messages":[]}`, "silent"},
		{"social", `{"action":"reply","messages":["可以，我能帮你翻译"],"engagement":"contextual_question"}`, "reply"},
		{"direct", `{"action":"reply","messages":["在呢"]}`, "reply"},
	} {
		config := enabledConfig()
		config.Permissions.ObserveGroups = true
		config.Permissions.AutonomousGroups = true
		e, _ := NewPreview(config, &sequenceModel{answers: []string{tc.answer}}, "role")
		d, err := e.Respond(context.Background(), Input{Mode: tc.mode, Preview: true, IsGroup: true, Text: "对，这叫娱乐兴趣"})
		if err != nil || d.Action != tc.want {
			t.Fatal(tc, d, err)
		}
	}
}
