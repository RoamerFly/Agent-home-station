package station

import (
	"context"
	"errors"
	"time"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/stationbrowser"
)

// A prepare result is a progress report for saved evidence, not an assertion
// that the platform still serves the same original media. The provider checks
// the exact creator work again before every newly requested media page.
type localDouyinRoundPrepareResult struct {
	Date                 string `json:"date"`
	WorkID               string `json:"work_id"`
	State                string `json:"state"`
	Ownership            string `json:"ownership"`
	MediaSHA256          string `json:"media_sha256,omitempty"`
	AudioComplete        bool   `json:"audio_complete"`
	VisualComplete       bool   `json:"visual_complete"`
	ContextComplete      bool   `json:"context_complete"`
	RequestedAudioPages  int    `json:"requested_audio_pages"`
	RequestedVisualPages int    `json:"requested_visual_pages"`
	AudioPagesRead       int    `json:"audio_pages_read"`
	VisualPagesRead      int    `json:"visual_pages_read"`
	AudioSeconds         int    `json:"audio_seconds"`
	VisualFrames         int    `json:"visual_frames"`
	MediaPageCount       int    `json:"media_page_count"`
	Resumed              bool   `json:"resumed"`
	CanResume            bool   `json:"can_resume"`
	StopReason           string `json:"stop_reason,omitempty"`
	ResumeBoundary       string `json:"resume_boundary"`
	Coverage             string `json:"coverage"`
	ReplyEligible        bool   `json:"reply_eligible"`
}

func (t *localToolSet) prepareDouyinRoundWork(ctx context.Context, m *localDouyinWebLogin, record localDouyinWebRecord,
	scope string, in localDouyinRoundsInput, notice localDouyinNoticeProof) (localDouyinRoundPrepareResult, *localDouyinRoundContextProof, error) {
	result := localDouyinRoundPrepareResult{Date: in.Date, WorkID: in.WorkID, State: "pending", Ownership: "unverified",
		RequestedAudioPages: in.AudioPages, RequestedVisualPages: in.VisualPages,
		AudioSeconds: in.AudioSeconds, VisualFrames: in.VisualFrames,
		ResumeBoundary: "临时 content_id 约 20 分钟有效；过期需重新 locate。新 content_id 不继承未完整音画字段的观察游标，须从首段重读，视觉可能重复计费。",
		Coverage:       "仅表示已保存证据的覆盖范围；尚未重新核实当前平台原件", ReplyEligible: false}
	if t.observations == nil {
		return result, nil, apphost.ErrObservationNotFound
	}
	source, granted := douyinMemorySource(record, t.access.fishID, t.access.ownerID, douyinMemoryWorks)
	if !granted {
		return result, nil, apphost.ErrObservationForbidden
	}
	// Require a previously saved notice for this exact work in this account and
	// day. No creator handle alone grants access to a media observation.
	group, err := m.listExecutionRoundsForWork(ctx, scope, in.Date, 0, 1, &in.WorkID)
	if err != nil {
		return result, nil, err
	}
	if len(group.Rounds) == 0 {
		return result, nil, mcptools.ArgumentError("$.work_id", "所选日期没有该作品的已保存互动通知")
	}
	current, err := m.executionWorkContext(ctx, scope, record, t.access.fishID, t.access.ownerID, in.Date, in.WorkID, 0, 1)
	if err != nil {
		return result, nil, err
	}
	if current.Ownership != "verified_own_work" {
		return result, nil, apphost.ErrObservationForbidden
	}
	result.Ownership = current.Ownership
	result.setSavedMedia(current)
	proof := &localDouyinRoundContextProof{Source: source, Revision: record.Revision, Date: in.Date, WorkID: in.WorkID}
	if err := t.checkNoticeProof(ctx, notice); err != nil {
		return result, nil, err
	}
	// Page text is generated solely from the live creator handle index. Its
	// source ID binds that temporary handle to the notice's exact work. The
	// provider also verifies its creator account against the current owner.
	page, err := t.observations.Read(ctx, apphost.ObservationInput{AppID: douyinapp.ID,
		ConnectionID: douyinapp.WebReadConnectionID, ContentID: in.ContentID, Field: apphost.ObservationPageText})
	if err != nil {
		return result, nil, err
	}
	if err := t.observations.Check(ctx, page); err != nil {
		return result, nil, err
	}
	if len(page.Chunks) != 1 || page.Chunks[0].SourceID != in.WorkID ||
		page.Chunks[0].SourceURL != stationbrowser.DouyinCreatorManageURL || !page.Complete {
		return result, nil, apphost.ErrObservationForbidden
	}
	if err := t.checkNoticeProof(ctx, notice); err != nil {
		return result, nil, err
	}
	if err := t.checkDouyinRoundContextProof(ctx, *proof); err != nil {
		return result, nil, err
	}
	t.appReads = append(t.appReads, page)
	// Observer.Read uses the same durable checkpoint as app_observe. A completed
	// creator media checkpoint deliberately refreshes on the next Read, so
	// complete fields must be skipped before making any media Read call.
	if result.done(in) {
		result.requestedFieldsComplete()
		return result, proof, nil
	}
	// One call remains bounded even if an Agent requests many pages. The
	// original request is reported unchanged; another call resumes the cursor.
	readCtx, cancel := context.WithTimeout(ctx, 170*time.Second)
	defer cancel()
	for _, field := range []apphost.ObservationField{apphost.ObservationAudio, apphost.ObservationVisual} {
		pages, limit := in.AudioPages, in.AudioSeconds
		reserve := 50 * time.Second
		if field == apphost.ObservationVisual {
			pages, limit, reserve = in.VisualPages, in.VisualFrames, 125*time.Second
		}
		for pageIndex := 0; pageIndex < pages; pageIndex++ {
			if result.fieldComplete(field) {
				break
			}
			if deadline, ok := readCtx.Deadline(); ok && time.Until(deadline) < reserve {
				result.StopReason = "turn_time_budget"
				result.CanResume = true
				return result, proof, nil
			}
			if err := t.checkNoticeProof(readCtx, notice); err != nil {
				return result, nil, err
			}
			if err := t.checkDouyinRoundContextProof(readCtx, *proof); err != nil {
				return result, nil, err
			}
			observed, readErr := t.observations.Read(readCtx, apphost.ObservationInput{AppID: douyinapp.ID,
				ConnectionID: douyinapp.WebReadConnectionID, ContentID: in.ContentID, Field: field, Limit: limit})
			if readErr != nil {
				if err := t.checkNoticeProof(ctx, notice); err != nil {
					return result, nil, err
				}
				if err := t.checkDouyinRoundContextProof(ctx, *proof); err != nil {
					return result, nil, err
				}
				if errors.Is(readErr, apphost.ErrObservationWebSnapshotChanged) {
					result.State, result.StopReason, result.CanResume = "restart_required", "creator_works_changed", false
					return result, proof, nil
				}
				if errors.Is(readErr, apphost.ErrObservationAudioSourceChanged) ||
					errors.Is(readErr, apphost.ErrObservationVisualSourceChanged) {
					result.State, result.StopReason, result.CanResume = "source_changed", "media_sha_changed", false
					return result, proof, nil
				}
				if errors.Is(readErr, context.Canceled) || errors.Is(readErr, apphost.ErrObservationForbidden) ||
					result.AudioPagesRead+result.VisualPagesRead == 0 {
					return result, nil, readErr
				}
				result.State, result.StopReason, result.CanResume = "partial", "media_read_failed", true
				return result, proof, nil
			}
			if err := t.observations.Check(readCtx, observed); err != nil {
				return result, nil, err
			}
			if err := t.checkNoticeProof(readCtx, notice); err != nil {
				return result, nil, err
			}
			if err := t.checkDouyinRoundContextProof(readCtx, *proof); err != nil {
				return result, nil, err
			}
			mediaHash := ""
			if field == apphost.ObservationAudio && observed.AudioProof != nil {
				mediaHash = observed.AudioProof.MediaSHA256
			} else if field == apphost.ObservationVisual && observed.VisualProof != nil {
				mediaHash = observed.VisualProof.MediaSHA256
			}
			if mediaHash == "" {
				return result, nil, apphost.ErrObservationInvalid
			}
			t.appReads = append(t.appReads, observed)
			if field == apphost.ObservationAudio {
				result.AudioPagesRead++
			} else {
				result.VisualPagesRead++
			}
			result.Resumed = result.Resumed || observed.Resumed
			previousHash := result.MediaSHA256
			current, err = m.executionWorkContext(readCtx, scope, record, t.access.fishID, t.access.ownerID,
				in.Date, in.WorkID, 0, 1)
			if err != nil {
				return result, nil, err
			}
			if current.Ownership != "verified_own_work" || current.MediaSHA256 != mediaHash {
				return result, nil, apphost.ErrObservationInvalid
			}
			result.setSavedMedia(current)
			if previousHash != "" && mediaHash != previousHash {
				result.State, result.StopReason, result.CanResume = "source_changed", "media_sha_changed", false
				return result, proof, nil
			}
			if observed.Complete && !result.fieldComplete(field) {
				// The Observer will refresh this creator field after completion.
				// Do not instruct an automatic retry if saved evidence did not
				// establish a continuous field on this exact media hash.
				result.State, result.StopReason, result.CanResume = "evidence_unverified", "completed_field_not_verified_in_saved_context", false
				return result, proof, nil
			}
		}
	}
	if result.done(in) {
		result.requestedFieldsComplete()
	} else {
		result.State, result.StopReason, result.CanResume = "pending", "page_budget", true
	}
	return result, proof, nil
}

func (r *localDouyinRoundPrepareResult) setSavedMedia(context localDouyinExecutionWorkContext) {
	r.MediaSHA256 = context.MediaSHA256
	r.AudioComplete, r.VisualComplete = context.AudioComplete, context.VisualComplete
	r.ContextComplete = r.MediaSHA256 != "" && r.AudioComplete && r.VisualComplete
	r.MediaPageCount = context.MediaPageCount
}

func (r localDouyinRoundPrepareResult) fieldComplete(field apphost.ObservationField) bool {
	if field == apphost.ObservationAudio {
		return r.AudioComplete
	}
	return r.VisualComplete
}

func (r localDouyinRoundPrepareResult) done(in localDouyinRoundsInput) bool {
	return (in.AudioPages == 0 || r.AudioComplete) && (in.VisualPages == 0 || r.VisualComplete)
}

func (r *localDouyinRoundPrepareResult) requestedFieldsComplete() {
	r.State = "requested_fields_complete"
	r.CanResume = !r.ContextComplete
	switch {
	case r.ContextComplete:
		r.StopReason = "already_saved_or_completed"
	case !r.VisualComplete:
		r.StopReason = "visual_incomplete_set_audio_pages_0_and_visual_pages_positive"
	default:
		r.StopReason = "audio_incomplete_set_audio_pages_positive_and_visual_pages_0"
	}
}
