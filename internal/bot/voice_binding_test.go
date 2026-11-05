package bot

import (
	"context"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestExpectedNamedVoiceBlocksWrongOrMissingMetadata(t *testing.T) {
	for _, voice := range []string{"", "default-character", "named-mom"} {
		t.Run("binding-"+voice, func(t *testing.T) {
			w, n, c, in := mediaWorker(t)
			in.ExpectedVoiceID = "named-mom"
			w.queueMedia(context.Background(), c, in, companion.GeneratedMedia{ID: "synthetic-source", Kind: "voice", Title: "mom.wav", VoiceID: voice, VoiceEngine: "GPT-SoVITS"})
			want := 0
			if voice == in.ExpectedVoiceID {
				want = 1
			}
			if n.sends != want {
				t.Fatal("name/title bypassed voice identity", n.sends, want)
			}
			if voice != "named-mom" && (w.runtime.Media[0].State != "needs_review" || w.runtime.Media[0].Reason != "media_expected_voice_mismatch") {
				t.Fatal(w.runtime.Media[0])
			}
		})
	}
}

func TestVoiceMetadataSurvivesRuntimeAndMemoryRecovery(t *testing.T) {
	w, _, c, in := mediaWorker(t)
	if err := c.Store.Observe(in, c.Config, time.Now()); err != nil {
		t.Fatal(err)
	}
	w.config.Draining = func() bool { return true }
	w.queueMedia(context.Background(), c, in, companion.GeneratedMedia{ID: "synthetic", Kind: "voice", Title: "plain.wav", VoiceID: "named-mom", VoiceEngine: "GPT-SoVITS"})
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	d := w.runtime.Media[0]
	if d.VoiceID != "named-mom" || d.VoiceEngine != "GPT-SoVITS" {
		t.Fatal("runtime lost named identity", d)
	}
	w.mediaRecord(d, "media_queued", "decision")
	scope := c.Store.Context(in, c.Config, time.Now())
	found := false
	for _, turn := range scope.Messages {
		if turn.Media != nil && turn.Media.Key == d.Key {
			found = turn.Media.VoiceID == d.VoiceID && turn.Media.VoiceEngine == d.VoiceEngine
		}
	}
	if !found {
		t.Fatal("memory lost named voice identity")
	}
}

func TestExpectedSingingProfileCannotUseTitleOrDifferentProfile(t *testing.T) {
	for _, profile := range []string{"", "default-rvc", "mom-rvc"} {
		w, n, c, in := mediaWorker(t)
		in.ExpectedVoiceProfile = "mom-rvc"
		w.queueMedia(context.Background(), c, in, companion.GeneratedMedia{ID: "synthetic-singing", Kind: "voice", Title: "mom-song.wav", VoiceProfile: profile, VoiceEngine: "rvc"})
		want := 0
		if profile == in.ExpectedVoiceProfile {
			want = 1
		}
		if n.sends != want {
			t.Fatal("profile requirement bypassed", profile, n.sends)
		}
	}
}

func TestFinalDecisionCannotChangeExistingVoiceTaskBinding(t *testing.T) {
	w, n, c, in := mediaWorker(t)
	in.ExpectedVoiceID = "named-a"
	w.finishCharacter(context.Background(), c, in, companion.Decision{Action: "reply", Messages: []string{"成品"}, ExpectedVoiceID: "named-b"}, nil, nil, false, false)
	if n.sends != 0 || len(w.runtime.Plans) != 0 || len(w.runtime.Media) != 0 {
		t.Fatal("mismatched final binding reached delivery")
	}
}
