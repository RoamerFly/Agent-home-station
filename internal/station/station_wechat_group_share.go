package station

// This file deliberately keeps the automatic group-share path small and
// stricter than the interactive video tools.  A group preference can opt in to
// an automatic summary, but it must never turn card metadata into a claim that
// the video was watched.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/mediadecode"
	"wechat-bot/internal/mediaobserve"
)

const (
	stationGroupShareAudioSeconds = 120
	stationGroupShareAudioBudget  = 240
	stationGroupShareFramePage    = 6
)

// readGroupShareEvidence decodes a current, opted-in WeChat Channels card for
// the automatic group-summary path.  It returns only firsthand ASR/frame
// observations: title, author, description, URL and other publisher card
// fields intentionally never enter either the returned text or evidence.
//
// The worker must call this only after it has authorized the conversation's
// reply grant.  This method separately checks the attachment grant and the
// exact persisted source, so a stale/revoked source fails closed before any
// media byte is fetched.
func (t *stationAttachmentTools) readGroupShareEvidence(ctx context.Context, in companion.Input, m linuxnative.Message) (string, companion.MediaObservationEvidence, error) {
	canonical, err := t.validateCurrentGroupShare(ctx, in, m)
	if err != nil {
		return "", companion.MediaObservationEvidence{}, err
	}

	// A fork prevents a concurrent model turn from replacing this callback's
	// trace while the original source and grant checks are in progress.
	tools, ok := t.ForkTools().(*stationAttachmentTools)
	if !ok {
		return "", companion.MediaObservationEvidence{}, errors.New("share_auto_summary_tools_unavailable")
	}
	tools.SetTrace(canonical)
	if !wechatapp.IsChannelsShare(m) {
		return tools.readPublicGroupShareEvidence(ctx, m)
	}
	text, evidence, err := tools.readAutomaticVideoEvidence(ctx, m.ID, stationGroupShareAudioBudget, true, true)
	if err != nil {
		return "", companion.MediaObservationEvidence{}, err
	}
	return "[微信视频号实际内容观察]\n" + text, evidence, nil
}

// readPublicGroupShareEvidence handles ordinary public cards (including
// Kuaishou and other non-Channels links) through the same credential-free
// public reader used by the interactive share tool. Public videos are staged
// privately and pass through the same decoder, ASR and frame observer as a
// native WeChat video.
func (t *stationAttachmentTools) readPublicGroupShareEvidence(ctx context.Context, m linuxnative.Message) (string, companion.MediaObservationEvidence, error) {
	if m.Link == nil || wechatapp.PublicShareURL(m.Link.URL) == "" {
		return "", companion.MediaObservationEvidence{}, errors.New("share_public_url_unavailable")
	}
	url := wechatapp.PublicShareURL(m.Link.URL)
	progress := func(event, status, reason string) {
		if t.shareProgress != nil {
			t.shareProgress(event, status, reason)
		}
	}
	checkSource := func() error {
		_, checkErr := t.validateCurrentGroupShare(ctx, t.trace, m)
		return checkErr
	}
	videoRequired := m.AppMessageType != nil && *m.AppMessageType == 4
	progress("share_public_read_started", "running", url)
	share, err := mcptools.ReadPublicShareViaHostReader(ctx, url, 64<<20, videoRequired)
	if err != nil {
		progress("share_public_read_failed", "failed", err.Error())
		return "", companion.MediaObservationEvidence{}, err
	}
	if err = checkSource(); err != nil {
		return "", companion.MediaObservationEvidence{}, err
	}
	if share.Kind != "video" {
		text := strings.TrimSpace(share.Text)
		if text == "" {
			progress("share_public_page_empty", "failed", "empty_page")
			return "", companion.MediaObservationEvidence{}, errors.New("share_public_page_empty")
		}
		evidence := companion.MediaObservationEvidence{ObservedRange: "公开页面正文", Excerpt: text,
			Parts: []companion.ObservationPart{{Kind: companion.ObservationPageBody, Range: "公开页面正文", Excerpt: text}}}
		progress("share_public_read_completed", "completed", "page_text")
		return "[微信公开分享页面正文]\n" + text, evidence, nil
	}
	if len(share.Data) == 0 {
		return "", companion.MediaObservationEvidence{}, errors.New("share_public_video_empty")
	}
	dir, err := os.MkdirTemp(t.dir, "public-share-")
	if err != nil {
		return "", companion.MediaObservationEvidence{}, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "original.video")
	if err = os.WriteFile(path, share.Data, 0600); err != nil {
		return "", companion.MediaObservationEvidence{}, err
	}
	meta, err := mediadecode.Probe(ctx, path)
	if err != nil {
		return "", companion.MediaObservationEvidence{}, err
	}
	parts := make([]companion.ObservationPart, 0, 2)
	if t.stationLocalAudio {
		_, transcribe, bindErr := t.localASR.bind(ctx, mediaobserve.SourceCheck(func(context.Context) error { return checkSource() }))
		if bindErr == nil {
			out := map[string]any{}
			length := math.Min(meta.Seconds, stationGroupShareAudioBudget)
			if audioErr := stationObserveAudio(ctx, path, dir, 0, length, meta.Seconds, false, transcribe, out); audioErr == nil {
				if text, ok := out["text"].(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, companion.ObservationPart{Kind: companion.ObservationAudioTranscript, Range: "公开视频完整音轨", Excerpt: text})
					progress("share_public_audio_transcription", "completed", "audio.cpp")
				}
			} else {
				progress("share_public_audio_failed", "failed", audioErr.Error())
			}
		}
	}
	if t.stationVideoVision {
		_, vision, bindErr := t.localVision.bind(ctx, mediaobserve.SourceCheck(func(context.Context) error { return checkSource() }))
		if bindErr == nil {
			out := map[string]any{}
			input := stationVideoObservationInput{Step: 1, Page: stationGroupShareFramePage, Batch: stationGroupShareFramePage, Dimension: 768, Tokens: 512}
			analyze := func(ctx context.Context, prompt string, images []deepseek.Image, tokens int) (string, error) {
				return vision(ctx, "public-share:"+url, prompt, images, tokens)
			}
			if frameErr := stationObserveFrames(ctx, path, dir, input, meta, analyze, out); frameErr == nil {
				if batches, ok := out["batches"].([]map[string]any); ok {
					var text []string
					for _, batch := range batches {
						if analysis, ok := batch["analysis"].(string); ok && strings.TrimSpace(analysis) != "" {
							text = append(text, analysis)
						}
					}
					if len(text) != 0 {
						parts = append(parts, companion.ObservationPart{Kind: companion.ObservationVisualFrames, Range: "公开视频采样帧", Excerpt: strings.Join(text, "\n")})
						progress("share_public_visual_frames", "completed", "station-vision")
					}
				}
			} else {
				progress("share_public_visual_failed", "failed", frameErr.Error())
			}
		}
	}
	if !companion.HasFirsthandObservation(parts) {
		progress("share_public_observation_unavailable", "failed", "no_audio_or_visual_observation")
		return "", companion.MediaObservationEvidence{}, errors.New("share_public_media_observer_unavailable")
	}
	evidence := companion.MediaObservationEvidence{ObservedRange: "公开视频音轨与采样帧", Parts: parts}
	_, evidence.Excerpt = stationGroupShareEvidenceText(parts)
	progress("share_public_read_completed", "completed", "audio_and_visual")
	return "[微信公开视频实际内容观察]\n" + evidence.Excerpt, evidence, nil
}

// Both automatic entry points use the same verified original and observation
// tools. Each caller controls its budget and authorized backends.
func (t *stationAttachmentTools) readAutomaticVideoEvidence(ctx context.Context, message string, audioBudget float64, useAudio, useVision bool) (string, companion.MediaObservationEvidence, error) {
	asset, duration, frames, err := stationGroupShareRead(ctx, t, message)
	if err != nil {
		return "", companion.MediaObservationEvidence{}, err
	}

	evidence := companion.MediaObservationEvidence{}
	var audioErr error
	var audio stationGroupShareAudio
	if useAudio {
		audio, audioErr = stationGroupShareObserveAudio(ctx, t, message, asset, duration, audioBudget)
		if audioErr == nil && strings.TrimSpace(audio.Text) != "" {
			rangeLabel := fmt.Sprintf("视频音轨 %.3f–%.3f 秒", audio.Start, audio.End)
			if audio.Complete {
				rangeLabel += "（完整）"
			}
			evidence.Parts = append(evidence.Parts, companion.ObservationPart{
				Kind: companion.ObservationAudioTranscript, Range: rangeLabel, Excerpt: audio.Text,
			})
		}
	}

	// A no-speech clip still deserves a visual observation.  Do not pay for a
	// second observer call when the decoded speech already gives the model
	// firsthand content; this is an intentional bounded fallback, not a
	// publisher-metadata fallback.
	if useVision && !companion.HasFirsthandObservation(evidence.Parts) {
		visual, visualErr := stationGroupShareObserveFrames(ctx, t, message, asset, frames)
		if visualErr != nil {
			if audioErr != nil {
				return "", companion.MediaObservationEvidence{}, errors.Join(audioErr, visualErr)
			}
			return "", companion.MediaObservationEvidence{}, visualErr
		}
		if strings.TrimSpace(visual.Text) != "" {
			times := make([]string, 0, len(visual.Times))
			for _, at := range visual.Times {
				times = append(times, fmt.Sprintf("%.3f秒", at))
			}
			rangeLabel := "视频采样帧"
			if len(times) != 0 {
				rangeLabel += "：" + strings.Join(times, "、")
			}
			evidence.Parts = append(evidence.Parts, companion.ObservationPart{
				Kind: companion.ObservationVisualFrames, Range: rangeLabel, Excerpt: visual.Text,
			})
		}
	}
	if !companion.HasFirsthandObservation(evidence.Parts) {
		if audioErr != nil {
			return "", companion.MediaObservationEvidence{}, audioErr
		}
		switch audio.State {
		case "no_audio_track":
			return "", companion.MediaObservationEvidence{}, observationFailure("video_audio_track_absent", "视频没有音轨，未取得画面观察。")
		case "no_recognizable_speech":
			return "", companion.MediaObservationEvidence{}, observationFailure("video_no_recognizable_speech", "已读取音轨，未识别到可辨语音，未取得画面观察。")
		}
		return "", companion.MediaObservationEvidence{}, errors.New("media_observation_empty")
	}
	evidence.ObservedRange, evidence.Excerpt = stationGroupShareEvidenceText(evidence.Parts)
	return evidence.Excerpt, evidence, nil
}

func (t *stationAttachmentTools) validateCurrentGroupShare(ctx context.Context, in companion.Input, m linuxnative.Message) (companion.Input, error) {
	if ctx.Err() != nil {
		return companion.Input{}, ctx.Err()
	}
	if _, err := wechatapp.ShareReadCapability(m); err != nil {
		return companion.Input{}, err
	}
	messageAt, err := time.Parse(time.RFC3339Nano, m.CreatedAt)
	inputAt, inputTimeErr := time.Parse(time.RFC3339Nano, in.MessageTime)
	if err != nil || inputTimeErr != nil || !messageAt.Equal(inputAt) || m.Direction != "incoming" || !strings.HasSuffix(m.ThreadID, "@chatroom") || m.IsQuotedText() ||
		m.ID == "" || m.AccountID == "" || m.SenderID == "" || m.AccountID != in.Account || m.ThreadID != in.Thread ||
		m.ID != in.TraceID || m.SenderID != in.Sender || !t.scopeAllowed(in, t.grants) {
		return companion.Input{}, errors.New("share_auto_summary_source_invalid")
	}
	if t.store == nil || in.Source == nil {
		return companion.Input{}, errors.New("share_auto_summary_source_unavailable")
	}
	stored, ok := t.store.SourceForInput(in)
	if !ok {
		return companion.Input{}, errors.New("share_auto_summary_source_changed")
	}
	storedAt, err := time.Parse(time.RFC3339Nano, stored.OccurredAt)
	if err != nil || !storedAt.Equal(messageAt) || stored.EventID == "" || in.Source.EventID != "" && stored.EventID != in.Source.EventID || in.Source.Revision > 0 && stored.Revision != in.Source.Revision ||
		stored.Account != m.AccountID || stored.Thread != m.ThreadID || stored.MessageID != m.ID || stored.Sender != m.SenderID ||
		stored.ServerID != m.ServerID || stored.MessageType != m.Type {
		return companion.Input{}, errors.New("share_auto_summary_source_changed")
	}
	// The native packet hash is bound to the media reference. The durable
	// event hashes its retained (possibly empty) user text instead.
	bodyHash := sha256.Sum256([]byte(m.Body))
	for _, media := range in.IncomingMedia {
		source := media.Source
		if media.Kind == "share" && source.Account == m.AccountID && source.Thread == m.ThreadID &&
			source.MessageID == m.ID && source.Sender == m.SenderID && source.BodyHash == hex.EncodeToString(bodyHash[:]) {
			in.Source = stored
			return in, nil
		}
	}
	return companion.Input{}, errors.New("share_auto_summary_source_changed")
}

func stationGroupShareRead(ctx context.Context, tools *stationAttachmentTools, message string) (asset string, duration float64, frames int, err error) {
	raw, err := tools.readVideo(ctx, stationGroupShareJSON(map[string]any{"message_id": message, "max_bytes": int64(50 << 20), "timeout_seconds": 120}))
	if err != nil {
		return "", 0, 0, err
	}
	var result struct {
		Asset    string `json:"asset_id"`
		Metadata struct {
			Duration float64 `json:"duration_seconds"`
			Frames   int     `json:"total_frames"`
		} `json:"metadata"`
	}
	if json.Unmarshal([]byte(raw), &result) != nil || !wechatapp.ValidAttachmentAssetID(result.Asset) || result.Metadata.Duration <= 0 {
		return "", 0, 0, errors.New("share_auto_summary_read_invalid")
	}
	return result.Asset, result.Metadata.Duration, result.Metadata.Frames, nil
}

type stationGroupShareAudio struct {
	Text       string
	State      string
	Start, End float64
	Complete   bool
}

func stationGroupShareObserveAudio(ctx context.Context, tools *stationAttachmentTools, message, asset string, duration, audioBudget float64) (stationGroupShareAudio, error) {
	start, limit := 0.0, math.Min(duration, audioBudget)
	result := stationGroupShareAudio{Start: 0}
	var text []string
	for start < limit-.001 {
		requested := math.Min(stationGroupShareAudioSeconds, limit-start)
		raw, err := tools.observeVideo(ctx, stationGroupShareJSON(map[string]any{
			"message_id": message, "asset_id": asset, "mode": "audio", "start_seconds": start, "duration_seconds": requested, "timeout_seconds": 120,
		}))
		if err != nil {
			if len(text) == 0 || ctx.Err() != nil {
				return stationGroupShareAudio{}, err
			}
			break
		}
		var page struct {
			Text  string  `json:"text"`
			Start float64 `json:"start_seconds"`
			End   float64 `json:"end_seconds"`
			Next  float64 `json:"next_start_seconds"`
			State string  `json:"transcription_state"`
			Done  bool    `json:"audio_complete"`
		}
		if json.Unmarshal([]byte(raw), &page) != nil || page.State == "" || page.Next < start {
			return stationGroupShareAudio{}, errors.New("share_auto_summary_audio_invalid")
		}
		if value := strings.TrimSpace(page.Text); value != "" {
			text = append(text, value)
		}
		result.End, result.Complete = max(result.End, page.End), page.Done
		result.State = page.State
		if page.Done || page.Next <= start+.001 {
			break
		}
		start = page.Next
	}
	result.Text = strings.Join(text, "\n")
	return result, nil
}

type stationGroupShareFrames struct {
	Text  string
	Times []float64
}

func stationGroupShareObserveFrames(ctx context.Context, tools *stationAttachmentTools, message, asset string, frames int) (stationGroupShareFrames, error) {
	step := 1
	if frames > stationGroupShareFramePage {
		step = max(1, frames/stationGroupShareFramePage)
	}
	raw, err := tools.observeVideo(ctx, stationGroupShareJSON(map[string]any{
		"message_id": message, "asset_id": asset, "mode": "frames", "page_size": stationGroupShareFramePage,
		"step": step, "batch_size": stationGroupShareFramePage, "max_dimension": 768, "analysis_tokens": 512, "timeout_seconds": 120,
	}))
	if err != nil {
		return stationGroupShareFrames{}, err
	}
	var result struct {
		Batches []struct {
			Analysis string    `json:"analysis"`
			Times    []float64 `json:"timestamps_seconds"`
		} `json:"batches"`
	}
	if json.Unmarshal([]byte(raw), &result) != nil || len(result.Batches) == 0 {
		return stationGroupShareFrames{}, errors.New("share_auto_summary_frames_invalid")
	}
	var text []string
	var times []float64
	for _, batch := range result.Batches {
		if value := strings.TrimSpace(batch.Analysis); value != "" {
			text = append(text, value)
		}
		times = append(times, batch.Times...)
	}
	return stationGroupShareFrames{Text: strings.Join(text, "\n"), Times: times}, nil
}

func stationGroupShareEvidenceText(parts []companion.ObservationPart) (string, string) {
	ranges, excerpts := make([]string, 0, len(parts)), make([]string, 0, len(parts))
	for _, part := range parts {
		if !part.Firsthand() {
			continue
		}
		ranges = append(ranges, part.Range)
		switch part.Kind {
		case companion.ObservationAudioTranscript:
			excerpts = append(excerpts, "音轨转写："+part.Excerpt)
		case companion.ObservationVisualFrames:
			excerpts = append(excerpts, "画面观察："+part.Excerpt)
		}
	}
	return strings.Join(ranges, "; "), strings.Join(excerpts, "\n")
}

func stationGroupShareJSON(value any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}
