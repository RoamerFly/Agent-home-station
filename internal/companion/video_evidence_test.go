package companion

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestObservedShareIsNotDowngradedToCard(t *testing.T) {
	for _, tc := range []struct {
		body, state string
		cardOnly    bool
	}{
		{ObservedShareHeader + "\n标题：样例\n实际观察", "observed", false},
		{"[已缓存本会话原视频素材：示例]\n" + ObservedShareHeader + "\n实际观察", "observed", false},
		{"[收到分享卡片，仅依据卡片标题与简介，尚未观看视频]\n标题：" + ObservedShareHeader, "observed", true},
		{ObservedShareHeader + "\n旧观察", "metadata_only", true},
	} {
		packet, err := json.Marshal(map[string]any{"kind": "current_message", "media_observations": []map[string]string{{"kind": "share", "observation": tc.body, "observation_state": tc.state}}})
		if err != nil {
			t.Fatal(err)
		}
		if got := currentMetadataOnly([]map[string]string{{"role": "user", "content": string(packet)}}); got != tc.cardOnly {
			t.Errorf("cardOnly=%v for %q", got, tc.body)
		}
	}
}

func TestTypedObservationPartsControlShareMetadataState(t *testing.T) {
	packet := map[string]any{"kind": "current_message", "media_observations": []map[string]any{{"kind": "share", "observation": "", "observation_state": "observed", "observation_parts": []ObservationPart{{Kind: ObservationPublisherDescription, Range: "card", Excerpt: "标题"}, {Kind: ObservationAudioTranscript, Range: "0-8 seconds", Excerpt: "实际转写"}}}}}
	raw, err := json.Marshal(packet)
	if err != nil || currentMetadataOnly([]map[string]string{{"role": "user", "content": string(raw)}}) {
		t.Fatalf("typed firsthand share was classified as metadata-only: %v", err)
	}
	packet["media_observations"] = []map[string]any{{"kind": "share", "observation": "", "observation_state": "observed", "observation_parts": []ObservationPart{{Kind: ObservationPublisherDescription, Range: "card", Excerpt: "标题"}}}}
	raw, err = json.Marshal(packet)
	if err != nil || !currentMetadataOnly([]map[string]string{{"role": "user", "content": string(raw)}}) {
		t.Fatalf("publisher-only share was not classified as metadata-only: %v", err)
	}
}

func TestVideoEvidenceKeepsFullAudioSeparateFromSampledFramesAndOldFailures(t *testing.T) {
	state := atomicProjectionState()
	current := state["current"].(map[string]any)
	fullAudio := strings.Repeat("第一个整理书桌第二个整理书架", 80) + "第六个坚持每周复盘最后一句原文也必须保留。"
	obs := "[分享来源资料；以下文字为发布者描述]\n标题：六个整理方法\n[微信视频实际内容观察]\n本次读取范围为采样概览\n[画面观察：6帧]\n六张有字幕的画面\n[实际音轨转写：0–120秒]\n" + fullAudio
	current["media_observations"] = []map[string]any{{"observation": obs, "observation_state": "observed", "result_available": true, "source": current["source"], "uploader_id": "member-a", "observation_version": 1}}
	var old map[string]any
	json.Unmarshal([]byte(state["sources"].([]map[string]string)[0]["content"]), &old)
	oldID := strings.Repeat("b", 64)
	old["source_event_id"] = oldID
	oldSource := old["source"].(map[string]any)
	oldSource["event_id"] = oldID
	oldSource["message_id"] = "different-old-video"
	old["message_id"] = "different-old-video"
	old["media_observations"] = []map[string]any{{"observation": "旧视频读取失败，未取得音轨。", "observation_state": "incomplete", "source": oldSource, "uploader_id": "member-a"}}
	oldRaw, _ := json.Marshal(old)
	state["sources"] = []map[string]string{{"role": "user", "content": string(oldRaw)}}
	state["evidence_event_ids"] = []string{strings.Repeat("a", 64), oldID}
	before, _ := canonicalGroundingJSON(state)
	binding, _, err := projectGroundingCheckState(state)
	if err != nil {
		t.Fatal(err)
	}
	transcripts, visuals, failures := 0, 0, 0
	for _, row := range binding.EvidenceTable {
		switch row.Kind {
		case "media_transcript":
			transcripts++
			if !strings.HasSuffix(row.Text, fullAudio) || row.Metadata["coverage_basis"] != "audio_header_not_visual_sampling" || row.Metadata["result_available"] != true || len(row.SourceVersions) != 1 {
				t.Fatal("audio lost its body/source/status", row)
			}
			if strings.Contains(row.Text, "采样概览") || strings.Contains(row.Text, "旧视频") {
				t.Fatal("unrelated status contaminated audio", row)
			}
		case "media_visual_observation":
			visuals++
		case "media_observation":
			if strings.Contains(row.Text, "旧视频读取失败") {
				failures++
			}
		}
	}
	after, _ := canonicalGroundingJSON(state)
	if transcripts != 1 || visuals != 1 || failures != 1 || string(before) != string(after) {
		t.Fatal(transcripts, visuals, failures)
	}
}

func TestTypedObservationPartsDoNotInferActualMediaFromPublisherText(t *testing.T) {
	state := atomicProjectionState()
	current := state["current"].(map[string]any)
	current["media_observations"] = []map[string]any{{
		"observation": "标题：妈妈发视频你会烦吗\n[微信视频实际内容观察]", "observation_state": "observed", "result_available": true,
		"source": current["source"], "uploader_id": "member-a", "observation_version": 1,
		"observation_parts": []ObservationPart{
			{Kind: ObservationPublisherDescription, Range: "share card", Excerpt: "妈妈发视频你会烦吗"},
			{Kind: ObservationVisualFrames, Range: "frames 4-44 seconds", Excerpt: "单人对镜头讲述母亲发视频。"},
			{Kind: ObservationAudioTranscript, Range: "audio 0-48 seconds", Excerpt: "妈妈总给你发一些健康小视频。"},
		},
	}}
	binding, _, err := projectGroundingCheckState(state)
	if err != nil {
		t.Fatal(err)
	}
	var publisher, visual, audio int
	for _, row := range binding.EvidenceTable {
		switch row.Kind {
		case "media_source_description":
			publisher++
		case "media_visual_observation":
			visual++
			if row.Text != "单人对镜头讲述母亲发视频。" {
				t.Fatal(row)
			}
			if _, leaked := row.Metadata["observation_parts"]; leaked {
				t.Fatal("all typed parts leaked into each visual row", row)
			}
		case "media_transcript":
			audio++
			if row.Text != "妈妈总给你发一些健康小视频。" || row.Metadata["coverage_basis"] != "audio_observer" {
				t.Fatal(row)
			}
			if _, leaked := row.Metadata["observation_parts"]; leaked {
				t.Fatal("all typed parts leaked into each transcript row", row)
			}
		}
	}
	if publisher != 1 || visual != 1 || audio != 1 {
		t.Fatalf("typed parts projected publisher=%d visual=%d audio=%d", publisher, visual, audio)
	}
}
