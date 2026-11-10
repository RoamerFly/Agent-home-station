package adapter

import (
	"context"
	"io"

	"wechat-bot/internal/linuxnative"
)

// StreamAttachment writes the bound native original or, for transcription,
// the separately verified playable voice. The host publishes the bytes only
// after rechecking the saved source and its grant.
func StreamAttachment(ctx context.Context, native *linuxnative.Sender, source AttachmentSource, playableVoice bool, limit int64, dst io.Writer, allowed func() bool) (linuxnative.VerifiedIncomingMedia, error) {
	var zero linuxnative.VerifiedIncomingMedia
	if native == nil || source.Account == "" || source.Account != native.Account() ||
		source.Origin != "" || source.Message == "" || dst == nil || playableVoice && source.Kind != "voice" {
		return zero, ErrAttachmentDenied
	}
	grant := transferGrant{ctx: ctx, allowed: allowed}
	if err := grant.check(); err != nil {
		return zero, err
	}
	writer := checkedMediaWriter{transferGrant: grant, dst: dst}
	var media linuxnative.VerifiedIncomingMedia
	var err error
	if playableVoice {
		media, err = native.StreamVerifiedPlayableVoice(ctx, source.Message, limit, writer)
	} else {
		media, err = native.StreamVerifiedIncomingMedia(ctx, source.Message, source.Kind, limit, writer)
	}
	if err != nil {
		return zero, err
	}
	if err := grant.check(); err != nil {
		return zero, err
	}
	return media, nil
}

type MediaUpload struct {
	Account, Filename, MIME, SHA256 string
	Bytes                           int64
}

// StageMediaUpload returns an account-local handle for verified bytes. A
// staged upload is not a delivery and does not authorize sending.
func StageMediaUpload(ctx context.Context, native *linuxnative.Sender, file MediaUpload, body io.Reader, allowed func() bool) (string, error) {
	if native == nil || file.Account == "" || file.Account != native.Account() || body == nil {
		return "", ErrAttachmentDenied
	}
	grant := transferGrant{ctx: ctx, allowed: allowed}
	if err := grant.check(); err != nil {
		return "", err
	}
	// The native multipart writer reads on its own goroutine. Only the host's
	// cheap live grant is called here; source and policy proofs stay with it.
	reader := checkedMediaReader{transferGrant: grant, src: body}
	upload, err := native.UploadVerified(ctx, file.Filename, file.MIME, reader, file.Bytes, file.SHA256)
	if err := grant.check(); err != nil {
		return "", err
	}
	if err != nil {
		return "", err
	}
	return upload.ID, nil
}

func ReadBoundChannelsShortLink(ctx context.Context, native *linuxnative.Sender, original AttachmentSource, message linuxnative.Message, source linuxnative.ChannelsSource, check func(context.Context) error) (string, error) {
	if native == nil || check == nil || original.Origin != "channels" || original.Kind != "video" ||
		original.Account != native.Account() || original.Thread != message.ThreadID || original.Message != message.ID ||
		!MatchesChannelsMessage(message, original.Account, source) || ChannelsVideoBinding(message, source) != original.Binding {
		return "", ErrAttachmentDenied
	}
	if err := check(ctx); err != nil {
		return "", err
	}
	link, err := native.ChannelsShareURL(ctx, original.Message, original.Thread, source.ObjectID)
	if err != nil {
		return "", err
	}
	if err := check(ctx); err != nil {
		return "", err
	}
	return link, nil
}

type transferGrant struct {
	ctx     context.Context
	allowed func() bool
}

func (g transferGrant) check() error {
	if err := g.ctx.Err(); err != nil {
		return err
	}
	if g.allowed == nil || !g.allowed() {
		return ErrAttachmentDenied
	}
	return nil
}

type checkedMediaWriter struct {
	transferGrant
	dst io.Writer
}

func (w checkedMediaWriter) Write(p []byte) (int, error) {
	if err := w.check(); err != nil {
		return 0, err
	}
	return w.dst.Write(p)
}

type checkedMediaReader struct {
	transferGrant
	src io.Reader
}

func (r checkedMediaReader) Read(p []byte) (int, error) {
	if err := r.check(); err != nil {
		return 0, err
	}
	return r.src.Read(p)
}
