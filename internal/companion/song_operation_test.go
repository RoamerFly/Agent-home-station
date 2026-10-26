package companion

import (
	"context"
	"encoding/json"
	"testing"
	"wechat-bot/internal/mcptools"
)

type songOperationModel struct{ step, checks int }

func (m *songOperationModel) ClassifySongOperation(context.Context, string) (string, error) {
	m.checks++
	return "cover", nil
}
func (m *songOperationModel) Chat(context.Context, string, []map[string]string) (string, error) {
	m.step++
	switch m.step {
	case 1:
		return `{"action":"tool","tool":"music_local_start","arguments":{"mode":"original","title":"wrong"}}`, nil
	case 2:
		return `{"action":"tool","tool":"music_local_start","arguments":{"mode":"cover","title":"birthday"}}`, nil
	}
	return `{"action":"reply","messages":["歌曲任务已安排"]}`, nil
}

type songOperationTools struct {
	calls int
	mode  string
}

func (t *songOperationTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "music_local_start", InputSchema: json.RawMessage(`{}`)}}, nil
}
func (t *songOperationTools) Call(_ context.Context, _ string, b json.RawMessage) (string, error) {
	var v struct{ Mode string }
	json.Unmarshal(b, &v)
	t.mode = v.Mode
	t.calls++
	return `{"automatic_followup":true,"job_id":"synthetic"}`, nil
}
func TestSongOperationUsesPrimaryModelWithoutSecondaryClassifier(t *testing.T) {
	model := &songOperationModel{}
	tools := &songOperationTools{}
	e, err := NewPreview(enabledConfig(), model, "test")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools = tools
	e.MaxToolCalls = 4
	_, err = e.Respond(context.Background(), Input{Mode: "direct", Text: "唱个生日歌", Preview: true})
	if err != nil || tools.calls != 2 || tools.mode != "cover" || model.checks != 0 {
		t.Fatal(err, tools, model)
	}
}
