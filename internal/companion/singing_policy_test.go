package companion

import (
	"context"
	"encoding/json"
	"testing"
)

type singingPolicyModel struct {
	step   int
	permit bool
	checks int
}

func (m *singingPolicyModel) AllowCharacterSinging(context.Context, string) (bool, error) {
	m.checks++
	return m.permit, nil
}
func (m *singingPolicyModel) Chat(context.Context, string, []map[string]string) (string, error) {
	m.step++
	if m.step == 1 {
		return `{"action":"tool","tool":"music_local_start","arguments":{"mode":"original","singing_voice":"character"}}`, nil
	}
	if m.step == 2 && !m.permit {
		return `{"action":"tool","tool":"music_local_start","arguments":{"mode":"original","singing_voice":"native"}}`, nil
	}
	return `{"action":"reply","messages":["已开始制作"]}`, nil
}

type singingPolicyTools struct {
	songOperationTools
	voice string
}

func (t *singingPolicyTools) Call(ctx context.Context, name string, b json.RawMessage) (string, error) {
	var in struct {
		Voice string `json:"singing_voice"`
	}
	_ = json.Unmarshal(b, &in)
	t.voice = in.Voice
	return t.songOperationTools.Call(ctx, name, b)
}
func TestSingingParametersStayWithPrimaryModelWithoutSecondaryClassifier(t *testing.T) {
	for _, permit := range []bool{false, true} {
		m := &singingPolicyModel{permit: permit}
		tools := &singingPolicyTools{}
		e, _ := NewPreview(enabledConfig(), m, "test")
		e.Tools = tools
		e.MaxToolCalls = 4
		_, err := e.Respond(context.Background(), Input{Mode: "direct", Text: "唱下班的歌", Preview: true})
		want := "native"
		if permit {
			want = "character"
		}
		if err != nil || tools.calls != map[bool]int{false: 2, true: 1}[permit] || tools.voice != want || m.checks != 0 {
			t.Fatal(err, tools, m)
		}
	}
}
