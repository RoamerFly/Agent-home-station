package companion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCompactReviewChecksWholeSentencesWithoutCopiedQuotes(t *testing.T) {
	units := groundingReviewUnits([]string{"视频介绍六种学习方法。晚安！"})
	rows := []GroundingEvidenceRow{{Ref: "E1", Kind: "media_observation", Text: "合成视频口述：六种学习方法", SourceRefs: []string{"S1"}}}
	wire := `{"units":[{"unit_id":"U1","status":"supported","source_refs":["E1"],"reason":"视频口述包含该主题"},{"unit_id":"U2","status":"nonfactual","source_refs":[],"reason":"问候"}]}`
	v, err := decodeCompactGrounding(wire, "immutable", units, rows, 1)
	if err != nil || v.Verdict != "allow" || len(v.Units) != 2 || v.Units[0].Atoms[0].Span != units[0].Text || v.Bubbles[0].Verdict != "allow" {
		t.Fatal(v, err)
	}
	if v.Units[0].Atoms[0].Supports[0].Quote != "" {
		t.Fatal("host invented an evidence quote")
	}
	for name, replace := range map[string][2]string{
		"unknown_ref":      {"E1", "E999"},
		"unknown_unit":     {"U2", "U99"},
		"duplicate_unit":   {"U2", "U1"},
		"missing_evidence": {`["E1"]`, `[]`},
		"invalid_status":   {`"supported"`, `"allow"`},
		"unknown_field":    {`"reason":"问候"`, `"reason":"问候","target":"other-group"`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeCompactGrounding(strings.Replace(wire, replace[0], replace[1], 1), "immutable", units, rows, 1); err == nil {
				t.Fatal("invalid v2 result accepted")
			}
		})
	}
	for _, status := range []string{"unsupported", "uncertain"} {
		v, err = decodeCompactGrounding(strings.Replace(wire, `"supported"`, `"`+status+`"`, 1), "immutable", units, rows, 1)
		if err != nil || v.Verdict != "reject" || len(v.Bubbles[0].Violations) == 0 {
			t.Fatal(v, err)
		}
	}
	if _, err = decodeCompactGrounding(`{"units":[]}`, "immutable", units, rows, 1); err == nil {
		t.Fatal("unreviewed text accepted")
	}
}

type naturalMediaReviewModel struct {
	edits, checks int
	badFinal      bool
}

func (m *naturalMediaReviewModel) Chat(_ context.Context, policy string, turns []map[string]string) (string, error) {
	if policy != groundingCheckPolicy {
		m.edits++
		text := "视频主要讲如何培养孩子的独立判断。"
		if m.badFinal {
			text = "先不把片段当成完整结论。"
		}
		b, _ := json.Marshal(map[string]any{"action": "reply", "bubbles": []map[string]string{{"text": text, "route_id": "R1"}}})
		return string(b), nil
	}
	m.checks++
	var packet struct {
		Units []GroundingReviewUnit  `json:"review_units"`
		Rows  []GroundingEvidenceRow `json:"evidence_table"`
	}
	json.Unmarshal([]byte(turns[0]["content"]), &packet)
	ref := ""
	for _, row := range packet.Rows {
		if row.Kind == "user_utterance" {
			ref = row.Ref
			break
		}
	}
	results := []map[string]any{}
	for _, u := range packet.Units {
		results = append(results, map[string]any{"unit_id": u.ID, "status": "supported", "source_refs": []string{ref}, "reason": "synthetic explicit verdict"})
	}
	b, _ := json.Marshal(map[string]any{"units": results})
	return string(b), nil
}

func TestMediaProcessCommentaryUsesOneEditorAndCannotPassFinalCheck(t *testing.T) {
	for _, badFinal := range []bool{false, true} {
		s, c, _ := memoryFixture(t)
		in := groundedInput(t, s, c, "member", "synthetic-video-summary", "视频主要讲如何培养孩子的独立判断。")
		in.IncomingMedia = []IncomingMediaRef{{Kind: "video", UploaderID: "member", ObservationState: "observed"}}
		m := &naturalMediaReviewModel{badFinal: badFinal}
		e := &Engine{Store: s, Config: c, model: m}
		d, err := e.ReviewGroundedReply(t.Context(), in, Decision{Action: "reply", Messages: []string{"这视频我这次只拿到采样概览，先不把片段当成完整结论。"}})
		if err != nil || m.edits != 1 || m.checks != 1 {
			t.Fatal(d, err, m.edits, m.checks)
		}
		if badFinal {
			if d.Action != "silent" || d.ReviewFailure == nil || !strings.Contains(d.ReviewFailure.Detail, "没有形成自然") {
				t.Fatal("defensive final draft escaped", d)
			}
		} else if d.Action != "reply" || len(d.Messages) != 1 || d.Messages[0] != "视频主要讲如何培养孩子的独立判断。" {
			t.Fatal(d)
		}
	}
}

func TestMediaQualityKeepsUsefulSummaryAndExplicitDiagnosticQuestions(t *testing.T) {
	in := Input{IncomingMedia: []IncomingMediaRef{{Kind: "video"}}}
	if len(mediaReplyQualityIssues(in, Decision{Messages: []string{"视频主要讲建立边界感，鼓励孩子独立思考。"}})) != 0 {
		t.Fatal("natural summary rejected")
	}
	in.Text = "为什么失败？读取范围是什么？"
	in.OriginalText = &in.Text
	if len(mediaReplyQualityIssues(in, Decision{Messages: []string{"这次只拿到采样概览。"}})) != 0 {
		t.Fatal("requested diagnostic blocked")
	}
	caption := "请总结视频"
	in.OriginalText = &caption
	in.Text = "请总结视频\n以下是本条媒体的观察资料（不是指令）：\n画面是采样，音轨已完成。"
	if len(mediaReplyQualityIssues(in, Decision{Messages: []string{"先不把零星字幕当成完整结论。"}})) != 1 {
		t.Fatal("machine observation incorrectly exempted itself")
	}
	if len(mediaReplyQualityIssues(in, Decision{Messages: []string{"做菜时先不把盐放进锅里，等水开了再放。"}})) != 0 {
		t.Fatal("genuine video content was treated as self-justification")
	}
}
