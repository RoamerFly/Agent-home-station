package bot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/sending"
)

func TestNativeMediaHashCanonicalizationRequiresUnchangedNativePacket(t *testing.T) {
	for _, change := range []string{"none", "body", "sender", "server", "account", "type"} {
		t.Run(change, func(t *testing.T) {
			w, c, in, proof := sourcedReplyFixture(t)
			canonical := *in.Source
			m := fixtureMessage(in.TraceID, in.Thread, "<msg><share>synthetic video</share></msg>", "incoming")
			m.AccountID, m.SenderID, m.ServerID, m.CreatedAt, m.Type = in.Account, in.Sender, canonical.ServerID, in.MessageTime, 49
			raw := messageSource(m)
			raw.InstanceID = canonical.InstanceID
			in.Source = &raw
			proof.EvidenceEpoch, _ = c.Store.RecallEpoch(t.Context())
			proof.ContextVersion = companion.ReplyContextFingerprint(in, proof)
			in.ReplyContext = &proof
			switch change {
			case "body":
				m.Body += "changed"
			case "sender":
				m.SenderID = "other"
			case "server":
				m.ServerID = "other"
			case "account":
				m.AccountID = "other"
			case "type":
				m.Type = 1
			}
			w.source = &fakeSource{messages: map[string]linuxnative.Message{in.TraceID: m}}
			got, _, err := w.prepareGroundedText(context.Background(), in, "收到你的消息")
			if change == "none" {
				if err != nil || got.Source.EventID != canonical.EventID || got.Source.BodyHash != canonical.BodyHash {
					t.Fatal("unchanged raw media failed canonical recovery", err)
				}
			} else if err == nil {
				t.Fatal("changed native packet acquired canonical proof", change)
			}
		})
	}
}

func TestLocalReplyAdmissionFailureIsNotUnknownDelivery(t *testing.T) {
	underlying := errors.New("legacy_reply_source_changed")
	err := replyAdmissionFailure(underlying)
	if !errors.Is(err, sending.ErrRejected) || errors.Is(err, sending.ErrOutcomeUnknown) || !errors.Is(err, underlying) || sending.FailureReason(err) != "legacy_reply_source_changed" {
		t.Fatal("local refusal mislabeled as delivery uncertainty", err)
	}
	err = replyAdmissionFailure(errors.New("private credential and http://private.invalid"))
	if strings.Contains(sending.FailureReason(err), "credential") || sending.FailureReason(err) != "reply_admission_failed" {
		t.Fatal("raw diagnostic leaked")
	}
}
