package companion

import (
	"encoding/json"
	"strings"
	"testing"
)

func atomicTestWire(units []GroundingReviewUnit, makeAtoms func(GroundingReviewUnit) []GroundingAtom) string {
	results := []GroundingUnitVerdict{}
	for _, unit := range units {
		results = append(results, GroundingUnitVerdict{UnitID: unit.ID, Atoms: makeAtoms(unit)})
	}
	raw, _ := json.Marshal(map[string]any{"units": results})
	return string(raw)
}

func atomicNonfactual(unit GroundingReviewUnit) []GroundingAtom {
	return []GroundingAtom{{Span: unit.Text, Mode: "nonfactual", Status: "nonfactual", NonfactualCategory: "acknowledgment", Supports: []GroundingSupport{}, Reason: "Synthetic protocol fixture; no semantic quality claim."}}
}

func atomicTypedRows(state map[string]any, kind string) []GroundingEvidenceRow {
	raw, _ := json.Marshal(state["evidence_table"])
	var rows []GroundingEvidenceRow
	_ = json.Unmarshal(raw, &rows)
	out := []GroundingEvidenceRow{}
	for _, row := range rows {
		if row.Kind == kind {
			out = append(out, row)
		}
	}
	return out
}

func TestAtomicGroundingUnitsPreserveWholeUnicodeAndQuotedSentences(t *testing.T) {
	text := "她说“等我。先别走！”。\n想聊什么？🙂 " + strings.Repeat("完整原文", 2000)
	units := groundingReviewUnits([]string{text, "晚安！"})
	joined := ""
	for _, unit := range units {
		if unit.BubbleIndex == 0 {
			joined += unit.Text
		}
	}
	if strings.ReplaceAll(joined, "\n", "") != strings.ReplaceAll(text, "\n", "") || units[0].Text != "她说“等我。先别走！”。" || units[len(units)-1].BubbleIndex != 1 {
		t.Fatal("partition changed, cropped or split a quotation", units[0], len(joined), len(text))
	}
	wire := atomicTestWire(units, atomicNonfactual)
	verdict, err := decodeGroundingUnits(wire, "host-bound", units, nil, 2)
	if err != nil || verdict.CandidateID != "host-bound" || verdict.Verdict != "allow" || len(verdict.Units) != len(units) || len(verdict.Bubbles) != 2 {
		t.Fatal(verdict, err)
	}
}

func TestAtomicGroundingRequiresEveryUnitAndEveryClaimSpan(t *testing.T) {
	units := groundingReviewUnits([]string{"通话结束，内容未知。晚安！"})
	good := atomicTestWire(units, atomicNonfactual)
	var parsed map[string]any
	json.Unmarshal([]byte(good), &parsed)
	for name, mutate := range map[string]func(map[string]any){
		"missing_unit":   func(v map[string]any) { v["units"] = v["units"].([]any)[:1] },
		"duplicate_unit": func(v map[string]any) { v["units"].([]any)[1].(map[string]any)["unit_id"] = "U1" },
		"unknown_unit":   func(v map[string]any) { v["units"].([]any)[0].(map[string]any)["unit_id"] = "U999" },
		"empty_atoms":    func(v map[string]any) { v["units"].([]any)[0].(map[string]any)["atoms"] = []any{} },
		"null_atoms":     func(v map[string]any) { v["units"].([]any)[0].(map[string]any)["atoms"] = nil },
		"omitted_half_sentence": func(v map[string]any) {
			v["units"].([]any)[0].(map[string]any)["atoms"].([]any)[0].(map[string]any)["span"] = "通话结束"
		},
		"invented_span": func(v map[string]any) {
			v["units"].([]any)[0].(map[string]any)["atoms"].([]any)[0].(map[string]any)["span"] = "并没有出现的原文"
		},
		"unknown_field": func(v map[string]any) { v["verdict"] = "allow" },
		"no_category": func(v map[string]any) {
			v["units"].([]any)[0].(map[string]any)["atoms"].([]any)[0].(map[string]any)["nonfactual_category"] = ""
		},
		"invented_category": func(v map[string]any) {
			v["units"].([]any)[0].(map[string]any)["atoms"].([]any)[0].(map[string]any)["nonfactual_category"] = "anything_goes"
		},
	} {
		t.Run(name, func(t *testing.T) {
			var value map[string]any
			json.Unmarshal([]byte(good), &value)
			mutate(value)
			raw, _ := json.Marshal(value)
			verdict, err := decodeGroundingUnits(string(raw), "bound", units, nil, 1)
			if name == "no_category" || name == "invented_category" {
				if err != nil || verdict.Verdict != "reject" {
					t.Fatal("invalid nonfactual label must reject, never allow", verdict, err)
				}
			} else if err == nil {
				t.Fatal("incomplete or malformed units were accepted", string(raw))
			}
		})
	}
	if _, err := decodeGroundingUnits(`{"bubbles":[{"index":0,"verdict":"allow","violations":[]}]}`, "bound", units, nil, 1); err == nil {
		t.Fatal("old empty-violations shortcut survived")
	}
}

func TestAtomicGroundingExactEvidenceAndUnsupportedPresuppositions(t *testing.T) {
	units := groundingReviewUnits([]string{"通话已经结束。"})
	row := GroundingEvidenceRow{Ref: "E1", Kind: "user_utterance", Text: "我刚打完电话。", SourceRefs: []string{"S1"}, SourceVersions: map[string]int{"S1": 3}, Metadata: map[string]any{"speaker_id": "member-a", "user_text_state": "verified_original"}}
	goodAtoms := func(unit GroundingReviewUnit) []GroundingAtom {
		return []GroundingAtom{{Span: unit.Text, Mode: "assertion", Claim: "来源人物的通话已结束", Status: "supported", Supports: []GroundingSupport{{Ref: "E1", Quote: "刚打完电话"}}, Reason: "原话明确陈述该事件已结束。"}}
	}
	good := atomicTestWire(units, goodAtoms)
	verdict, err := decodeGroundingUnits(good, "bound", units, []GroundingEvidenceRow{row}, 1)
	if err != nil || verdict.Verdict != "allow" {
		t.Fatal(verdict, err)
	}
	for _, bad := range []string{
		strings.Replace(good, `"ref":"E1"`, `"ref":"E999"`, 1),
		strings.Replace(good, `"quote":"刚打完电话"`, `"quote":"刚打完电话","version":3`, 1),
		strings.Replace(good, `"supports":[{"ref":"E1","quote":"刚打完电话"}]`, `"supports":[]`, 1),
	} {
		if _, err := decodeGroundingUnits(bad, "bound", units, []GroundingEvidenceRow{row}, 1); err == nil {
			t.Fatal("unsupported or invented provenance accepted", bad)
		}
	}
	badQuote := strings.Replace(good, "刚打完电话", "根本没有这句", 1)
	rejected, quoteErr := decodeGroundingUnits(badQuote, "bound", units, []GroundingEvidenceRow{row}, 1)
	if quoteErr != nil || rejected.Verdict != "reject" || rejected.Bubbles[0].Violations[0].Kind != "invalid_evidence_quote" {
		t.Fatal("invalid evidence quote must reject the candidate, not pass or retry JSON", rejected, quoteErr)
	}
	question := groundingReviewUnits([]string{"通话内容涉及许多工作安排吧？"})
	for _, status := range []string{"unsupported", "uncertain"} {
		wire := atomicTestWire(question, func(unit GroundingReviewUnit) []GroundingAtom {
			return []GroundingAtom{{Span: unit.Text, Mode: "presupposition", Claim: "该通话涉及工作安排且数量很多", Status: status, Supports: []GroundingSupport{}, Reason: "来源只给出结束状态，没有通话内容及数量。"}}
		})
		verdict, err := decodeGroundingUnits(wire, "bound", question, []GroundingEvidenceRow{row}, 1)
		if err != nil || verdict.Verdict != "reject" || verdict.Bubbles[0].Violations[0].Kind != "presupposition" {
			t.Fatal("unsupported/uncertain atomic presupposition escaped", verdict, err)
		}
	}
}

func atomicProjectionState() map[string]any {
	id := strings.Repeat("a", 64)
	source := map[string]any{"event_id": id, "message_id": "same-native-id", "instance_id": "persistent-instance", "account": "account", "thread": "group", "sender": "member-a", "revision": 3, "body_hash": "host-only-hash"}
	row := map[string]any{"kind": "history_message", "source_event_id": id, "source": source, "message_id": "same-native-id", "message_sender_id": "member-a", "speaker_id": "member-a", "text": "我刚打完电话。", "user_text_state": "verified_original", "evidence_kind": "user_source", "addressees": []string{"member-b"}}
	packet, _ := json.Marshal(row)
	current := map[string]any{}
	for key, value := range row {
		current[key] = value
	}
	current["must_reply"], current["mode"] = true, "social"
	return map[string]any{"evidence_event_ids": []string{id}, "candidate": Decision{Action: "reply", Messages: []string{"通话刚结束。"}, ReplyContexts: []ReplyContext{{SourceVersions: map[string]int{id: 3}}}}, "current": current, "sources": []map[string]string{{"role": "user", "content": string(packet)}}, "participant_utterances": []any{row}, "review_settings": map[string]any{"model": "private-review-config"}, "data_snapshot_epoch": "host-epoch", "current_time": "2026-10-31T12:00:00+08:00", "timezone": "Asia/Shanghai", "source_reference_contracts": map[string]any{"participation_reference": []any{}, "media_annotation": []any{}, "interpretation": "empty contract"}, "quote_role_bindings": map[string]any{"edges": []any{}, "actors": []any{}}}
}

func TestAtomicGroundingProjectionDeduplicatesRolesAndKeepsHostBinding(t *testing.T) {
	state := atomicProjectionState()
	before, _ := canonicalGroundingJSON(state)
	binding, _, err := projectGroundingCheckState(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(binding.EvidenceTable) != 1 || binding.EvidenceTable[0].Kind != "user_utterance" || binding.EvidenceTable[0].SourceVersions["S1"] != 3 || binding.EvidenceTable[0].Metadata["speaker_id"] != "member-a" || binding.SourceAliases["S1"] != strings.Repeat("a", 64) {
		t.Fatal("duplicate sources, role loss or source-version loss", binding.EvidenceTable)
	}
	wire := string(binding.ReviewInput)
	for _, forbidden := range []string{"participant_utterances", "sources\"", "must_reply", "private-review-config", "host-epoch", "host-only-hash", "source_reference_contracts", "quote_role_bindings"} {
		if strings.Contains(wire, forbidden) {
			t.Fatal("checker retained duplicate, irrelevant or host-only module", forbidden, wire)
		}
	}
	if strings.Count(wire, "我刚打完电话。") != 1 || !strings.Contains(string(binding.OriginalState), "host-epoch") || len(binding.ReviewUnits) != 1 {
		t.Fatal("source was duplicated or original audit state was lost", wire)
	}
	after, _ := canonicalGroundingJSON(state)
	if string(before) != string(after) || string(binding.OriginalState) != string(before) {
		t.Fatal("projection changed authoritative snapshot")
	}
}

func TestAtomicGroundingProjectionPreservesMediaRoleAndRejectsSourceConflicts(t *testing.T) {
	state := atomicProjectionState()
	current := state["current"].(map[string]any)
	current["text"], current["user_text_state"] = "", "verified_original"
	current["media_observations"] = []map[string]any{{"observation": "画面里是一辆自行车。", "uploader_id": "member-b", "observation_state": "observed", "observation_version": 4, "voice_speaker_id": nil, "observed_range": "frame-1", "source": current["source"]}}
	binding, _, err := projectGroundingCheckState(state)
	if err != nil {
		t.Fatal(err)
	}
	media := binding.EvidenceTable[len(binding.EvidenceTable)-1]
	if media.Kind != "media_observation" || media.Metadata["uploader_id"] != "member-b" || media.Metadata["voice_speaker_id"] != nil || media.Metadata["observation_version"] != json.Number("4") {
		t.Fatal("media observation promoted to human speech or lost scope", media)
	}
	state = atomicProjectionState()
	state["candidate"] = Decision{Action: "reply", Messages: []string{"通话结束。"}, ReplyContexts: []ReplyContext{{SourceVersions: map[string]int{strings.Repeat("a", 64): 4}}}}
	if _, _, err := projectGroundingCheckState(state); err == nil {
		t.Fatal("conflicting source revision silently chose latest")
	}
	state = atomicProjectionState()
	state["current"].(map[string]any)["text"] = "同编号但完全不同的正文。"
	if _, _, err := projectGroundingCheckState(state); err == nil {
		t.Fatal("different text under one source silently replaced original")
	}
}

func TestAtomicGroundingMetadataQuotesCannotCrossSerializedFields(t *testing.T) {
	row := GroundingEvidenceRow{Ref: "E1", Kind: "media_observation", Text: "图上有一个杯子。", Metadata: map[string]any{"speaker_id": "member-a", "observation_state": "failed", "range": map[string]any{"seconds": json.Number("2"), "complete": false}}}
	for _, valid := range []string{"member-a", "failed", "2", "false", "一个杯子"} {
		if !groundingEvidenceContains(row, valid) {
			t.Fatal("exact evidence value rejected", valid)
		}
	}
	for _, invented := range []string{"speaker_id", `member-a","observation_state`, `"seconds":2`, `failed","range"`, "null", "2,false"} {
		if groundingEvidenceContains(row, invented) {
			t.Fatal("JSON key or cross-field fabrication accepted as quote", invented)
		}
	}
	structured := GroundingEvidenceRow{Kind: "execution_result", Text: `{"result":{"status":"accepted","asset_id":"asset-a"}}`}
	if !groundingEvidenceContains(structured, "accepted") || groundingEvidenceContains(structured, `accepted","asset_id`) || groundingEvidenceContains(structured, "status") {
		t.Fatal("structured result serialization became literal evidence")
	}
}

func TestAtomicGroundingParserUsesTheDedicatedTransportResourceBoundary(t *testing.T) {
	units := groundingReviewUnits([]string{"晚安。"})
	wire := atomicTestWire(units, func(unit GroundingReviewUnit) []GroundingAtom {
		atoms := atomicNonfactual(unit)
		atoms[0].Reason = strings.Repeat("safe-summary ", 25000)
		return atoms
	})
	if len(wire) <= 256<<10 {
		t.Fatal("fixture does not exercise the removed short-chat cap")
	}
	if _, err := decodeGroundingUnits(wire, "bound", units, nil, 1); err != nil {
		t.Fatal("explicit review output was truncated to a short chat cap", err)
	}
	if _, err := decodeGroundingUnits(strings.Repeat(" ", (16<<20)+1), "bound", units, nil, 1); err == nil {
		t.Fatal("dedicated transport resource boundary was not enforced")
	}
}

func TestAtomicGroundingASRRemainsMachineEvidenceAndDoesNotMergeDistinctSources(t *testing.T) {
	state := atomicProjectionState()
	current := state["current"].(map[string]any)
	current["text"] = "我转发一条录音。"
	secondSource := map[string]any{}
	for key, value := range current["source"].(map[string]any) {
		secondSource[key] = value
	}
	secondSource["event_id"], secondSource["sender"], secondSource["revision"] = strings.Repeat("b", 64), "member-b", 1
	current["source"], current["source_event_id"], current["message_sender_id"], current["speaker_id"] = secondSource, strings.Repeat("b", 64), "member-b", "member-b"
	current["media_observations"] = []map[string]any{{"observation": "[语音转写] 我明天去杭州。", "uploader_id": "member-b", "observation_state": "observed", "observation_version": 1, "source": secondSource, "voice_speaker_id": nil}}
	state["evidence_event_ids"] = []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}
	binding, _, err := projectGroundingCheckState(state)
	if err != nil {
		t.Fatal(err)
	}
	humans, asr := 0, 0
	for _, row := range binding.EvidenceTable {
		if row.Kind == "user_utterance" {
			humans++
			if strings.Contains(row.Text, "杭州") {
				t.Fatal("ASR promoted to native human body", row)
			}
		}
		if row.Kind == "media_observation" {
			asr++
			if row.Metadata["voice_speaker_id"] != nil || row.Metadata["uploader_id"] != "member-b" {
				t.Fatal("ASR bound uploader to first person", row)
			}
		}
	}
	if humans != 2 || asr != 1 {
		t.Fatal("same native id with different source/sender was deduplicated", humans, asr, binding.EvidenceTable)
	}
}

func TestAtomicReadOnlyCheckerOmitsMustReplyButEditorPreservesIt(t *testing.T) {
	s, c, _ := memoryFixture(t)
	in := groundedInput(t, s, c, "alpha", "required-current", "请给出一句有依据的回应。")
	in.MustReply = true
	model := &groundingSequenceModel{checks: []string{"reject", "allow"}, answers: []string{`{"action":"reply","bubbles":[{"text":"收到。","route_id":"R1"}]}`}}
	e := &Engine{Store: s, Config: c, model: model}
	out, err := e.ReviewGroundedReply(t.Context(), in, Decision{Action: "reply", Messages: []string{"待核对草稿。"}, ReplyContexts: []ReplyContext{{RiskFlags: []string{"atomic_required_response_test"}}}})
	if err != nil || out.Action != "reply" || len(model.inputs) != 3 {
		t.Fatal(out, err, model.calls)
	}
	for _, index := range []int{0, 2} {
		raw, _ := json.Marshal(model.inputs[index])
		if strings.Contains(string(raw), "must_reply") {
			t.Fatal("read-only semantic checker received reply pressure", index, string(raw))
		}
	}
	if model.inputs[1]["must_reply"] != true {
		t.Fatal("editor lost the explicit required-response contract", model.inputs[1])
	}
}

func TestAtomicGroundingDedupRequiresRealIdentityAndTrustedExcerpt(t *testing.T) {
	newBuilder := func() *groundingTableBuilder {
		return &groundingTableBuilder{positions: map[string]int{}, versions: map[string]int{}, allowed: map[string]bool{}, messages: map[string]string{}}
	}
	for _, suffix := range []bool{false, true} {
		b := newBuilder()
		if err := b.add("user_utterance", "原始完整正文", nil, map[string]any{"text_excerpted": false}, "stable-event"); err != nil {
			t.Fatal(err)
		}
		changed := "额外前缀原始完整正文"
		if suffix {
			changed = "原始完整正文额外后缀"
		}
		if err := b.add("user_utterance", changed, nil, map[string]any{"text_excerpted": false}, "stable-event"); err == nil {
			t.Fatal("two full same-version bodies were silently treated as excerpt expansion")
		}
	}
	b := newBuilder()
	if err := b.add("user_utterance", "完整正文", nil, map[string]any{"text_excerpted": true}, "stable-event"); err != nil {
		t.Fatal(err)
	}
	if err := b.add("user_utterance", "前段完整正文后段", nil, map[string]any{"text_excerpted": false}, "stable-event"); err != nil || len(b.rows) != 1 || b.rows[0].Text != "前段完整正文后段" || b.rows[0].Metadata["text_excerpted"] != false {
		t.Fatal("trusted excerpt could not be expanded from authorized full original", b.rows, err)
	}
	if err := b.add("user_utterance", "完整正文", nil, map[string]any{"text_excerpted": true}, "stable-event"); err != nil || b.rows[0].Metadata["text_excerpted"] != false {
		t.Fatal("later excerpt degraded an already-full source", b.rows, err)
	}
	b = newBuilder()
	for range 2 {
		if err := b.add("user_utterance", "相同文字但没有来源身份", nil, map[string]any{"speaker_id": "unknown"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if len(b.rows) != 2 {
		t.Fatal("identical text without source identity was merged")
	}
	for _, field := range []string{"speaker_id", "message_sender_id", "uploader_id", "addressees"} {
		b = newBuilder()
		old, next := any("member-a"), any("member-b")
		if field == "addressees" {
			old, next = []any{"member-a"}, []any{"member-b"}
		}
		if err := b.add("user_utterance", "同一正文", nil, map[string]any{field: old}, "stable-event"); err != nil {
			t.Fatal(err)
		}
		if err := b.add("user_utterance", "同一正文", nil, map[string]any{field: next}, "stable-event"); err == nil {
			t.Fatal("identity-critical metadata silently used first value", field)
		}
	}
}

func TestAtomicGroundingTypedMemoryKeepsDerivedStatusAndSourceQuotes(t *testing.T) {
	state := atomicProjectionState()
	id := strings.Repeat("a", 64)
	packet, _ := json.Marshal(map[string]any{"kind": "long_term_memory", "items": []map[string]any{{"id": "memory-one", "version": 2, "text": "某成员偏好清淡口味。", "status": "active", "inference": false, "subject": "member-a", "source_event_ids": []string{id}, "source_quotes": []map[string]any{{"event_id": id, "quote": "我喜欢清淡口味。"}}}}})
	state["recalled_evidence"] = []map[string]string{{"role": "user", "content": string(packet)}}
	binding, _, err := projectGroundingCheckState(state)
	if err != nil {
		t.Fatal(err)
	}
	memories := []GroundingEvidenceRow{}
	for _, row := range binding.EvidenceTable {
		if row.Kind == "derived_memory" {
			memories = append(memories, row)
		}
	}
	if len(memories) != 1 || memories[0].Metadata["status"] != "active" || memories[0].Metadata["version"] != json.Number("2") || memories[0].Metadata["inference"] != false || len(memories[0].SourceRefs) != 1 || memories[0].SourceRefs[0] != "S1" || memories[0].SourceVersions["S1"] != 3 {
		t.Fatal("authorized memory was dropped, flattened into native words or lost revisions", memories)
	}
	if strings.Count(string(binding.ReviewInput), "某成员偏好清淡口味。") != 1 || !groundingEvidenceContains(memories[0], "我喜欢清淡口味。") {
		t.Fatal("memory text duplicated or exact original quotation lost")
	}
}
