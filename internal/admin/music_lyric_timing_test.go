package admin

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
)

func TestLyricTimingCanBeDiscoveredBeforeGeneration(t *testing.T) {
	f := setup(t)
	f.manager.speech.YuEEnabled = true
	f.manager.speech.YuEEndpoint = "http://127.0.0.1:1"
	f.manager.speech.YuEToken = "synthetic"
	installLegacyLocalMusicPlugin(t, f.manager)
	f.manager.config.Companion.Permissions.SendVoice = true
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Account: "test", Thread: "filehelper", Channel: "wechat"})
	found := false
	for _, tool := range p.yueTools() {
		if tool.Name != "music_reference_inspect" {
			continue
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if e := json.Unmarshal(tool.InputSchema, &schema); e != nil {
			t.Fatal(e)
		}
		if _, ok := schema.Properties["lyrics_evidence_ids"]; !ok {
			t.Fatal("timing inspection missing from actual discovery schema")
		}
		found = true
	}
	if !found {
		t.Fatal("inspection tool unavailable")
	}
}

func TestLyricTimingReportsCoarseCoverageWithoutClaimingWordAlignment(t *testing.T) {
	items := []lyricEvidence{{ID: "first", Text: "合成开场晚风轻轻吹过窗台", Offset: 0, Seconds: 30}, {ID: "second", Text: "晚风轻轻吹过窗台合成后半段", Offset: 25, Seconds: 30}, {ID: "third", Text: "合成结尾", Offset: 60, Seconds: 10}}
	r := lyricTimingReview(items)
	if r["audio_coverage_seconds"] != 65 || r["overlap_seconds"] != 5 || r["gap_seconds"] != 5 || r["word_timestamps_verified"] != false || r["melody_alignment_verified"] != false {
		t.Fatal(r)
	}
	if len(r["boundary_repetitions"].([]map[string]any)) != 1 {
		t.Fatal("lost duplicate boundary observation", r)
	}
	if items[1].Text != "晚风轻轻吹过窗台合成后半段" {
		t.Fatal("silently rewrote original words")
	}
	reversed := lyricTimingReview([]lyricEvidence{items[2], items[0]})
	if reversed["out_of_order"] != true {
		t.Fatal(reversed)
	}
}

func TestFineLyricWindowsAreNotLimitedToTenEvidenceFiles(t *testing.T) {
	f := setup(t)
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper"})
	a := mediaAsset{Hash: "synthetic-matching-source"}
	ids := []string{}
	var words strings.Builder
	for i := 0; i < 12; i++ {
		text := fmt.Sprintf("第%d段合成歌词", i)
		e, err := p.saveLyricEvidence(a, text, "asr_candidate", i*15, 15)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID)
		words.WriteString(text)
	}
	if e := p.checkLyricEvidence(a, words.String(), ids); e != nil {
		t.Fatal(e)
	}
	if len(p.availableLyricEvidence(a)) != 12 {
		t.Fatal("fine-grained windows disappeared")
	}
	review := p.lyricTimingFor(a, ids)
	if review["audio_coverage_seconds"] != 180 || review["overlap_seconds"] != 0 {
		t.Fatal(review)
	}
}
