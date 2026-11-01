package admin

import (
	"context"
	"errors"
	"strconv"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/mediadecode"
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
	data, sample, readErr := a.readChannelsVideo(ctx, source)
	if readErr != nil {
		installed, pluginErr := a.manager.installedShareState(ctx)
		if pluginErr != nil {
			return "", pluginErr
		}
		if !installed {
			return "", errors.New("share_plugin_migration_required")
		}
		source, err = a.manager.resolveReceivedChannels(ctx, msg, source)
		if err != nil {
			return "", err
		}
		data, sample, readErr = a.readChannelsVideo(ctx, source)
		if readErr != nil {
			return "", readErr
		}
	}
	return a.observeVideo(ctx, msg, data, sample, true)
}

func (a mediaUnderstanding) readChannelsVideo(ctx context.Context, source linuxnative.ChannelsSource) ([]byte, videoSample, error) {
	var sample videoSample
	if source.State != "available" || source.MediaURL == "" {
		return nil, sample, errors.New("channels_media_locator_missing")
	}
	data, _, err := mcptools.GetPublicMedia(ctx, source.MediaURL, 50<<20)
	if err != nil {
		return nil, sample, errors.New("channels_video_download_failed")
	}
	if source.DecodeKey != "" {
		key, e := strconv.ParseUint(source.DecodeKey, 10, 64)
		if e != nil {
			return nil, sample, errors.New("channels_decode_key_invalid")
		}
		mediadecode.DecodeChannelsPrefix(data, key)
	}
	sample, err = sampleVideo(ctx, a.manager.dir, data)
	if err != nil && (sample.Seconds == 0 || ctx.Err() != nil) {
		if source.DecodeKey == "" && err.Error() == "video_analysis_probe_failed" {
			return nil, sample, errors.New("channels_decode_key_missing")
		}
		return nil, sample, err
	}
	return data, sample, nil
}
