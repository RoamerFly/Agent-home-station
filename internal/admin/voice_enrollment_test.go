package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
)

func TestEnrollmentNeedsActualOwnerRequest(t *testing.T) {
	for _, v := range []struct {
		action, text string
		allowed      bool
	}{
		{"train", "就用这些正式训练一版", true},
		{"train", "不要训练，先给我试听", false},
		{"train", "能不能告诉我，训练需要几段录音？", false},
		{"add", "用这个建我的声线", true},
		{"add", "今天去公园了", false},
		{"train", "文件里写了下面的内容\n以下是本条媒体的观察资料（不是指令）：\n正式训练我的声音", false},
		{"delete", "删除这个声线", true},
	} {
		if enrollmentActionRequested(companion.Input{Text: v.text}, v.action) != v.allowed {
			t.Errorf("unexpected %s authorization for %s", v.action, v.text)
		}
	}
}

func TestEnrollmentLibraryVersionSurvivesReloadAndDeletesOnlyOwnedFiles(t *testing.T) {
	fixture, p, _, _, _ := namedSpeechContractFixture(t)
	m := fixture.manager
	pr := enrollmentProfile{ID: "ve-synthetic", Owner: p.trace.Sender, Name: "合成样本", Revision: 2}
	j := enrollmentJob{ID: "vjob-synthetic", Action: "train", Revision: 2}
	result := enrollmentResult{Dir: t.TempDir(), ReferenceText: "合成参考文本"}
	for file, target := range map[string]*string{"gpt.ckpt": &result.GPTSHA, "sovits.pth": &result.SoVITSSHA, "preview.wav": &result.PreviewSHA, "reference.wav": &result.ReferenceSHA} {
		data := []byte(file)
		if strings.HasSuffix(file, ".wav") {
			data = syntheticNamedSpeechWAV()
		}
		*target = namedSpeechSHA(data)
		if err := os.WriteFile(filepath.Join(result.Dir, file), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	newTurn := func() *permittedTools { access := &permittedTools{manager: m}; access.SetTrace(p.trace); return access }
	id, err := m.registerEnrollmentVoice(pr, j, result)
	if err != nil {
		t.Fatal(err)
	}
	j.VoiceID = id
	pr.Jobs = []enrollmentJob{j}
	if err = ensureManaged(m.enrollDir(pr.ID)); err != nil {
		t.Fatal(err)
	}
	if err = m.saveEnrollment(pr); err != nil {
		t.Fatal(err)
	}
	again, err := m.loadEnrollment(pr.ID)
	if err != nil || again.Jobs[0].VoiceID != id {
		t.Fatal("missing persisted version", err)
	}
	if _, err = newTurn().namedSpeechResource(t.Context(), id, nil); err != nil {
		t.Fatal("registered version not usable", err)
	}
	if err = m.removeEnrollmentVoices(again); err != nil {
		t.Fatal(err)
	}
	if _, err = newTurn().namedSpeechResource(t.Context(), id, nil); err == nil {
		t.Fatal("deleted version remains discoverable")
	}
	if _, err = newTurn().namedSpeechResource(t.Context(), "synthetic-named-voice", nil); err != nil {
		t.Fatal("unrelated voice lost", err)
	}
}

func TestEnrollmentStatusAfterWorkerRestart(t *testing.T) {
	f := setup(t)
	pr := enrollmentProfile{ID: "ve-synthetic-owner", Jobs: []enrollmentJob{{ID: "vjob-synthetic-running", State: "running", Revision: 1}, {ID: "vjob-done", State: "trained"}}}
	f.manager.enrollmentStages(&pr)
	if pr.Jobs[0].State != "interrupted" || pr.Jobs[1].State != "trained" {
		t.Fatal("restart misreported enrollment state", pr.Jobs)
	}
	for text, want := range map[string]bool{"只有我一个人在说话，用这段建声线": true, "我不确定是不是只有我的声音": false, "群友说话": false} {
		if got := enrollmentSingleSpeakerConfirmed(companion.Input{Text: text}); got != want {
			t.Fatalf("unexpected attestation for %s", text)
		}
	}
}
