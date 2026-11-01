package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"wechat-bot/internal/pluginhost"
)

// A cached recording has its own origin. Its uploader is not necessarily the
// current requester, and neither is a manufactured native incoming message.
func referenceTranscriptionAssetVersion(a mediaAsset) string {
	data, _ := json.Marshal([]any{a.ID, a.Account, a.Scope, a.Hash, a.Bytes, a.MIME, a.ParentAssetID, a.VoiceID, a.VoiceProfile, a.Music})
	return agentID(string(data))
}

func (p *permittedTools) referenceTranscriptionAuthorization() string {
	if p == nil || p.manager == nil || !p.servicePluginsAllowed() || !p.manager.Config().Config.Enabled || !p.manager.Config().Companion.Permissions.Remember || p.trace.Channel != "wechat" || p.trace.Surf || !p.manager.agentTaskActive(p.trace) || p.manager.draining() {
		return ""
	}
	if strings.HasSuffix(p.trace.Thread, "@chatroom") && !p.manager.Config().Companion.Permissions.ObserveGroups {
		return ""
	}
	data, _ := json.Marshal([]any{p.trace.Account, p.trace.Thread, p.trace.Sender, p.trace.TaskID, p.trace.TraceID, p.trace.Source, p.trace.ReplyContext, p.MemoryResultAuthorization()})
	return agentID(string(data))
}

// newReferenceAudioOrigin binds a cached audio asset to the current authorized
// request.  It deliberately returns no bytes: callers must independently
// normalize a verified cache read before giving it to a bound media policy.
func (p *permittedTools) newReferenceAudioOrigin(ctx context.Context, asset mediaAsset, offset, seconds int, purpose, keyPrefix string) (pluginhost.Invocation, func(context.Context) error, error) {
	authorization := p.referenceTranscriptionAuthorization()
	if authorization == "" || asset.ID == "" || asset.Hash == "" || asset.Account != p.trace.Account || asset.Scope != assetScope(p.trace) || !strings.HasPrefix(asset.MIME, "audio/") || offset < 0 || seconds < 1 {
		return pluginhost.Invocation{}, nil, errors.New("media_reference_audio_not_authorized")
	}
	epoch, err := p.manager.memory.RecallEpoch(ctx)
	if err != nil {
		return pluginhost.Invocation{}, nil, err
	}
	version := referenceTranscriptionAssetVersion(asset)
	assetSource := "asset:" + asset.ID + ":" + version
	sources := map[string]int{assetSource: 1}
	if p.trace.Source != nil && p.trace.Source.EventID != "" {
		sources[p.trace.Source.EventID] = p.trace.Source.Revision
	}
	if p.trace.ReplyContext != nil {
		for id, revision := range p.trace.ReplyContext.SourceVersions {
			sources[id] = revision
		}
	}
	if purpose == "" || keyPrefix == "" {
		return pluginhost.Invocation{}, nil, errors.New("media_reference_audio_origin_required")
	}
	inv := pluginhost.Invocation{Account: p.trace.Account, Thread: p.trace.Thread, Requester: p.trace.Sender, Purpose: purpose, RequestKey: keyPrefix + "-" + agentID(p.trace.TraceID, p.trace.TaskID, asset.ID, version, strconv.Itoa(offset), strconv.Itoa(seconds)), SourceVersions: sources, EvidenceEpoch: epoch, Authorization: authorization}
	check := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if p.referenceTranscriptionAuthorization() != authorization {
			return errors.New("media_reference_transcription_authorization_changed")
		}
		currentEpoch, err := p.manager.memory.RecallEpoch(ctx)
		if err != nil || currentEpoch != epoch {
			return errors.New("media_reference_transcription_source_changed")
		}
		for id, revision := range sources {
			if id == assetSource {
				continue
			}
			event, err := p.manager.memory.ReadEvent(ctx, []string{assetScope(p.trace)}, id)
			if err != nil || revision < 1 || event.Revision != revision {
				return errors.New("media_reference_transcription_source_changed")
			}
			if source := p.trace.Source; source != nil && source.EventID == id && (event.Role != "user" || event.Sender != p.trace.Sender || source.Sender != "" && source.Sender != p.trace.Sender || source.BodyHash != "" && source.BodyHash != event.Hash || source.MessageID != "" && source.MessageID != event.Trace) {
				return errors.New("media_reference_transcription_requester_changed")
			}
		}
		if p.trace.TaskID != "" {
			task, ok := p.manager.agent.lookupTask(p.trace.TaskID, p.trace.Account, p.trace.Thread)
			if !ok || task.Sender != p.trace.Sender || p.manager.validateTaskSource(task) != nil || task.Constraints != nil && task.Constraints.AssetID != "" && task.Constraints.AssetID != asset.ID {
				return errors.New("media_reference_transcription_task_changed")
			}
		}
		currentAsset, _, err := p.referenceAsset(asset.ID)
		if err != nil || referenceTranscriptionAssetVersion(currentAsset) != version {
			return errors.New("media_reference_transcription_asset_changed")
		}
		return nil
	}
	return inv, check, nil
}

func (p *permittedTools) newReferenceTranscriptionPolicy(ctx context.Context, asset mediaAsset, wav []byte, offset, seconds int) (*mediaTranscriptionPolicy, error) {
	if !p.manager.mediaTranscriptionConfigured(ctx) {
		return nil, errors.New("media_transcription_plugin_required_or_unconfigured")
	}
	inv, check, err := p.newReferenceAudioOrigin(ctx, asset, offset, seconds, "transcribe selected reference recording", "reference-asr")
	if err != nil {
		return nil, err
	}
	return p.manager.newBoundMediaTranscriptionPolicy(ctx, wav, inv, 1, check)
}
