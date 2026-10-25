package companion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"wechat-bot/internal/mcptools"
)

type orderedTool struct {
	order *[]string
	calls int
}

func (*orderedTool) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "web_search"}, {Name: "web_media_download"}}, nil
}
func (t *orderedTool) Call(_ context.Context, name string, _ json.RawMessage) (string, error) {
	*t.order = append(*t.order, "tool:"+name)
	t.calls++
	return `{"candidates":[{"url":"https://example.org/real.mp3"}]}`, nil
}

type sequenceModel struct {
	answers []string
	turns   [][]map[string]string
	system  string
}

func (m *sequenceModel) Chat(_ context.Context, system string, turns []map[string]string) (string, error) {
	m.system = system
	m.turns = append(m.turns, append([]map[string]string{}, turns...))
	answer := m.answers[0]
	if len(m.answers) > 1 {
		m.answers = m.answers[1:]
	}
	return answer, nil
}

func TestProgressFollowsModelChoicePrecedesToolAndNeverLeaksIntoPreview(t *testing.T) {
	for _, preview := range []bool{false, true} {
		order := []string{}
		tools := &orderedTool{order: &order}
		model := &sequenceModel{answers: []string{`{"action":"tool","tool":"web_search","progress":"收到，我找找～","arguments":{"query":"song artist"}}`, `{"action":"reply","messages":["这个是真实链接 https://example.org/real.mp3"]}`}}
		store, _ := NewStore(t.TempDir())
		e, _ := New(enabledConfig(), store, model, "role")
		e.Tools = tools
		e.MaxToolCalls = 2
		ctx := WithProgress(context.Background(), func(context.Context, string) (bool, error) { order = append(order, "progress"); return true, nil })
		d, err := e.Respond(ctx, Input{Channel: "test", Account: "a", Thread: "friend", Mode: "direct", Text: "查一下这首歌的来源", Preview: preview})
		if err != nil || d.Action != "reply" || tools.calls != 1 {
			t.Fatal(d, err)
		}
		if preview && strings.Join(order, ",") != "tool:web_search" || !preview && strings.Join(order, ",") != "progress,tool:web_search" {
			t.Fatal(order)
		}
		if !strings.Contains(model.system, "先理解current_message") || strings.Contains(model.system, "不必先发") {
			t.Fatal("old blind-search/progress policy retained")
		}
	}
}

func TestGuessedDownloadURLIsNotExecutedAndDuplicateCallsAreNotReplayed(t *testing.T) {
	order := []string{}
	tools := &orderedTool{order: &order}
	m := &sequenceModel{answers: []string{`{"action":"tool","tool":"web_media_download","arguments":{"url":"https://guessed.invalid/secret.mp3","kind":"voice"}}`, `{"action":"tool","tool":"web_search","arguments":{"query":"song"}}`, `{"action":"tool","tool":"web_search","arguments":{"query":"song"}}`, `{"action":"reply","messages":["暂时没取得文件"]}`}}
	e, _ := NewPreview(enabledConfig(), m, "role")
	e.Tools = tools
	e.MaxToolCalls = 3
	_, err := e.Respond(context.Background(), Input{Mode: "direct", Text: "请查一下这首歌", Preview: true})
	if err != nil || tools.calls != 1 || strings.Join(order, ",") != "tool:web_search" {
		t.Fatal("guessed URL or duplicate executed", order, err)
	}
	urls := map[string]bool{}
	rememberResourceURLs(urls, `{"url":"https://example.org/real.mp3?x=1\u0026y=2"}`)
	if !urls["https://example.org/real.mp3?x=1&y=2"] {
		t.Fatal("JSON URL query corrupted", urls)
	}
}

func TestNegativeResourceRequestsAndCapabilityQuestionsDoNotForceDownloads(t *testing.T) {
	for _, text := range []string{"不要给我发语音，我只想知道你能做什么", "你支持发语音吗", "别找了，我现在不想听音乐", "只要链接，不用下载这首歌"} {
		order := []string{}
		tools := &orderedTool{order: &order}
		model := &sequenceModel{answers: []string{`{"action":"reply","messages":["明白～"]}`}}
		e, _ := NewPreview(enabledConfig(), model, "role")
		e.Tools = tools
		e.MaxToolCalls = 3
		d, err := e.Respond(context.Background(), Input{Mode: "direct", Text: text, Preview: true})
		if err != nil || d.Action != "reply" || len(model.turns) != 1 || tools.calls != 0 {
			t.Fatal("keyword forced an unwanted resource action", text, d, err)
		}
	}
}

func TestSemanticMediaIntentPrecedesToolsAndIsSuppressedInPreview(t *testing.T) {
	for _, preview := range []bool{false, true} {
		order := []string{}
		tools := &orderedTool{order: &order}
		model := &sequenceModel{answers: []string{`{"action":"tool","delivery_intent":"file","tool":"web_search","arguments":{"query":"song source"}}`, `{"action":"reply","delivery_intent":"file","messages":["这个是真实链接 https://example.org/real.mp3"]}`}}
		store, _ := NewStore(t.TempDir())
		e, _ := New(enabledConfig(), store, model, "role")
		e.Tools, e.MaxToolCalls = tools, 2
		ctx := WithMediaIntent(context.Background(), func(_ context.Context, intent string) error {
			order = append(order, "intent:"+intent)
			return nil
		})
		d, err := e.Respond(ctx, Input{Channel: "test", Account: "a", Thread: "friend", Mode: "direct", Text: "还是原版吧", Preview: preview})
		want := "intent:file,tool:web_search"
		if preview {
			want = "tool:web_search"
		}
		if err != nil || d.Action != "reply" || strings.Join(order, ",") != want {
			t.Fatal("intent late, repeated or leaked from preview", preview, order, d, err)
		}
	}
}

type discoveryChoiceTools struct{ calls int }

func (*discoveryChoiceTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "web_discover"}, {Name: "web_fetch"}}, nil
}
func (t *discoveryChoiceTools) Call(_ context.Context, name string, _ json.RawMessage) (string, error) {
	t.calls++
	return `{"candidates":[{"id":"one","title":"猫咪日常","url":"https://example.org/cat"}]}`, nil
}
func TestSurfDiscoversBeforeModelAndAllowsBrowsingWithoutMandatoryShare(t *testing.T) {
	tools := &discoveryChoiceTools{}
	m := &sequenceModel{answers: []string{`{"action":"silent","surf_action":"bookmark","surf_item_id":"one","reason":"今天只想收藏"}`}}
	e, _ := NewPreview(enabledConfig(), m, "role")
	e.Tools = tools
	e.MaxToolCalls = 1
	d, err := e.Respond(context.Background(), Input{Mode: "direct", Surf: true, Preview: true, Text: "看看有没有有趣内容"})
	if err != nil || d.Action != "silent" || tools.calls != 1 || len(m.turns) != 1 {
		t.Fatal("browse-only choice forced into more tools or a share", d, err, tools.calls)
	}
	if !strings.Contains(m.turns[0][len(m.turns[0])-1]["content"], "本次实际内容发现结果") {
		t.Fatal("model still starts with blank keyword search")
	}
}
