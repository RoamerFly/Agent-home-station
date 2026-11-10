package adapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type attachmentSourceFixture struct {
	message       linuxnative.Message
	onChannelRead func()
}

func (f attachmentSourceFixture) Account() string { return "fixture" }
func (f attachmentSourceFixture) Identity(context.Context) (string, error) {
	return "fixture-data", nil
}
func (f attachmentSourceFixture) Message(context.Context, string) (linuxnative.Message, error) {
	return f.message, nil
}
func (f attachmentSourceFixture) ChannelsMedia(_ context.Context, message string) (linuxnative.ChannelsSource, error) {
	f.onChannelRead()
	return linuxnative.ChannelsSource{AccountID: f.Account(), MessageID: message, ThreadID: f.message.ThreadID, ObjectID: "fixture-object"}, nil
}

func TestAttachmentSourceKeepsAssetIdentityAndHonorsRevocation(t *testing.T) {
	now := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	m := linuxnative.Message{ID: "fixture-msg", AccountID: "fixture", ThreadID: "room@chatroom", SenderID: "member", ServerID: "11",
		CreatedAt: now.Format(time.RFC3339Nano), Direction: "incoming", Body: "fixture body", Type: 3}
	req := AttachmentSourceRequest{AttachmentRequest: AttachmentRequest{Thread: m.ThreadID, Message: m.ID}, Grant: now,
		Reference: &companion.SourceRef{MessageID: m.ID, Account: m.AccountID, Thread: m.ThreadID, Sender: m.SenderID,
			ServerID: m.ServerID, OccurredAt: m.CreatedAt, MessageType: m.Type, BodyHash: "ca260f20e9412d1ac5e1e30014e8592c75e07ad93446586497e04863084b52a3"}}
	authorized, confirmed := true, true
	allowed := func(_ context.Context, thread string) bool { return authorized && thread == m.ThreadID }
	confirm := func(_ context.Context, in companion.Input) bool {
		return confirmed && in.TraceID == req.Message && in.Channel == ID && in.Source == req.Reference
	}
	source, err := ResolveAttachmentSource(t.Context(), attachmentSourceFixture{message: m}, req, allowed, confirm)
	if err != nil || source.AssetID() != "wa-1b080ffa52d9e538a934fea1fbe773e9a86ce82ce596cdf56e3a412a1f5412d6" {
		t.Fatal("the saved attachment identity changed", source.AssetID(), err)
	}
	foreign := m
	foreign.AccountID = "another-account"
	if _, err := ResolveAttachmentSource(t.Context(), attachmentSourceFixture{message: foreign}, req, allowed, confirm); !errors.Is(err, ErrAttachmentDenied) {
		t.Fatal("another account supplied the source", err)
	}
	confirmed = false
	if _, err := ResolveAttachmentSource(t.Context(), attachmentSourceFixture{message: m}, req, allowed, confirm); !errors.Is(err, ErrAttachmentDenied) {
		t.Fatal("the host could not revoke source visibility", err)
	}
	card := 51
	m.Type, m.AppMessageType = 49, &card
	req.Reference, req.VideoOnly = nil, true
	fixture := attachmentSourceFixture{message: m, onChannelRead: func() { authorized = false }}
	if _, err := ResolveAttachmentSource(t.Context(), fixture, req, allowed, nil); !errors.Is(err, ErrAttachmentDenied) {
		t.Fatal("a Channels locator outlived the original grant", err)
	}
}
