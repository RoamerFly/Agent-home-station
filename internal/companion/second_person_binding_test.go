package companion

import (
	"testing"
)

func TestPluralSecondPersonCannotIncludeReporterAsCallParticipant(t *testing.T) {
	in := Input{IsGroup: true, Sender: "alpha", Addressees: []string{"beta"}, Text: "你爸刚视频，刚挂掉。"}
	for _, pronoun := range append([]string{"你", "您"}, pluralSecondPersonForms...) {
		text := "那" + pronoun + "聊得怎么样？"
		for _, targets := range [][]string{{"alpha"}, {"alpha", "beta"}} {
			d := Decision{Messages: []string{text}, ReplyContexts: []ReplyContext{{TargetIDs: targets}}}
			if !misboundIncomingPerson(in, d) {
				t.Fatal("reporter became a participant via second-person form", text, targets)
			}
		}
		legitimate := Decision{Messages: []string{text}, ReplyContexts: []ReplyContext{{TargetIDs: []string{"beta"}}}}
		if misboundIncomingPerson(in, legitimate) {
			t.Fatal("actual participant was blocked", text)
		}
	}
}

func TestPluralSecondPersonAllowsReportedSpeechAndOpenQuestions(t *testing.T) {
	in := Input{IsGroup: true, Sender: "alpha", Addressees: []string{"beta"}, Text: "你爸刚视频，刚挂掉。"}
	for _, text := range []string{"聊得怎么样？", "你说的是乙与父亲刚结束的那通视频。", "你们说的是乙与父亲刚结束的那通视频。", "您们知道他们聊了什么吗？", "你们觉得这个消息要怎么回应？"} {
		d := Decision{Messages: []string{text}, ReplyContexts: []ReplyContext{{TargetIDs: []string{"alpha"}}}}
		if misboundIncomingPerson(in, d) {
			t.Fatal("reported speech or open question became a participant assertion", text)
		}
	}
}

func TestOutgoingPluralAudienceIncludesActualTargets(t *testing.T) {
	d := Decision{ReplyContexts: []ReplyContext{{TargetIDs: []string{"alpha"}}}}
	rows := replyAudiences(d)
	actual, ok := rows[0]["plural_second_person_must_include"].([]string)
	if !ok || len(actual) != 1 || actual[0] != "alpha" {
		t.Fatal("plural silently changed audience", rows)
	}
	forms, ok := rows[0]["outgoing_plural_second_person_forms"].([]string)
	if !ok || len(forms) != len(pluralSecondPersonForms) {
		t.Fatal("checker missing plural binding forms", rows)
	}
}
