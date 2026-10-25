package admin

import (
	"context"
	"errors"
	"strconv"
	"wechat-bot/internal/channels"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
)

type channelsReader interface {
	ChannelsMedia(context.Context, string) (linuxnative.ChannelsSource, error)
}

func (a mediaUnderstanding) understandChannels(ctx context.Context, msg linuxnative.Message) (string, error) {
	native, ok := a.manager.native.(channelsReader)
	if !ok {
		return "", errors.New("channels_source_backend_missing")
	}
	source, err := native.ChannelsMedia(ctx, msg.ID)
	if err != nil {
		return "", err
	}
	if source.ThreadID != msg.ThreadID || source.AccountID != a.manager.native.Account() || source.MessageID != msg.ID {
		return "", errors.New("channels_source_scope_mismatch")
	}
	if source.State != "available" || source.MediaURL == "" {
		return "", errors.New("channels_media_locator_missing")
	}
	// Resolve and validate public destinations through the existing media client.
	// Never include the signed locator or decode key in logs or model prompts.
	data, _, err := mcptools.GetPublicMedia(ctx, source.MediaURL, 50<<20)
	if err != nil {
		return "", errors.New("channels_video_download_failed")
	}
	if source.DecodeKey != "" {
		key, err := strconv.ParseUint(source.DecodeKey, 10, 64)
		if err != nil {
			return "", errors.New("channels_decode_key_invalid")
		}
		channels.DecodePrefix(data, key)
	}
	sample, err := sampleVideo(ctx, a.manager.dir, data)
	if err != nil && (sample.Seconds == 0 || ctx.Err() != nil) {
		if source.DecodeKey == "" && err.Error() == "video_analysis_probe_failed" {
			return "", errors.New("channels_decode_key_missing")
		}
		return "", err
	}
	return a.observeVideo(ctx, msg, data, sample, true)
}
