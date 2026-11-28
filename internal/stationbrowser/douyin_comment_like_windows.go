//go:build windows

package stationbrowser

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	"wechat-bot/internal/hostfile"
)

//go:embed douyin_comment_like_ui.js
var douyinCommentLikeUI string

// RunDouyinCommentLike checks one exact rendered comment on one fixed official
// video page. Ownership and durable deduplication remain caller obligations.
// The current comment-like control DOM contract has not been verified against
// Douyin; an unrecognized control is never clicked.
func RunDouyinCommentLike(ctx context.Context, profile string, req DouyinCommentLikeRequest, authorize func() error) (DouyinCommentLikeResult, error) {
	target, err := DouyinVideoURL(req.VideoID)
	if err != nil {
		return DouyinCommentLikeResult{State: DouyinInteractionFailed, Code: "invalid_video"}, ErrDouyinInteractionInput
	}
	return runDouyinCommentLike(ctx, profile, target, req, authorize, false)
}

// runDouyinCommentLike accepts local targets only in package synthetic tests.
func runDouyinCommentLike(ctx context.Context, profile, target string, req DouyinCommentLikeRequest, authorize func() error, fixture bool) (DouyinCommentLikeResult, error) {
	result := DouyinCommentLikeResult{State: DouyinInteractionUnknown, Submitted: req.Submitted}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if authorize == nil || !validDouyinCommentLike(req) || (!fixture && !isDouyinVideoURL(target)) {
		result.State, result.Code = DouyinInteractionFailed, "invalid_request"
		return result, ErrDouyinInteractionInput
	}
	if err := authorize(); err != nil {
		result.State, result.Code = DouyinInteractionCancelled, "authorization_changed"
		return result, err
	}
	lock, err := DouyinLoginProfileLockPath(profile)
	if err != nil {
		return result, ErrDouyinInteractionUnavailable
	}
	release, err := hostfile.TryLock(lock)
	if err != nil {
		return result, ErrDouyinLoginActive
	}
	defer release()
	workCtx, cancel := context.WithTimeout(ctx, pageLoadTimeout)
	defer cancel()
	var session *cdpSession
	if fixture {
		session, err = openCDP(workCtx, profile)
	} else {
		session, err = openRestrictedCDP(workCtx, profile, isDouyinHomeHost)
	}
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrDouyinInteractionUnavailable, err)
	}
	defer closeDouyinHomeSession(session)
	return runDouyinCommentLikeSession(workCtx, session, target, req, authorize)
}

type douyinCommentLikePageState struct {
	Ready, Invalid, Login, Clicked, Liked bool
	URL, Code                             string
}

func douyinCommentLikePage(ctx context.Context, s readerSession, id string, world int64, target, action string, req DouyinCommentLikeRequest) (douyinCommentLikePageState, error) {
	input, _ := json.Marshal(map[string]string{
		"target": target, "action": action, "commentID": req.CommentID,
		"text": req.ExpectedText, "authorID": req.ExpectedAuthorID,
	})
	var out struct {
		Result struct {
			Type  string
			Value douyinCommentLikePageState
		}
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	if err := s.Call(ctx, id, "Runtime.evaluate", map[string]any{
		"expression": "(" + douyinCommentLikeUI + ")(" + string(input) + ")",
		"contextId":  world, "returnByValue": true,
	}, &out); err != nil || out.Result.Type != "object" || len(out.Exception) != 0 || out.Result.Value.Invalid {
		return douyinCommentLikePageState{}, ErrDouyinInteractionUnavailable
	}
	if out.Result.Value.Login {
		return out.Result.Value, ErrDouyinWebLoginRequired
	}
	return out.Result.Value, nil
}

func runDouyinCommentLikeSession(ctx context.Context, session readerSession, target string, req DouyinCommentLikeRequest, authorize func() error) (DouyinCommentLikeResult, error) {
	result := DouyinCommentLikeResult{State: DouyinInteractionUnknown, Submitted: req.Submitted}
	var created struct {
		TargetID string `json:"targetId"`
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := session.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank", "background": true}, &created); err != nil || created.TargetID == "" ||
		session.Call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": created.TargetID, "flatten": true}, &attached) != nil || attached.SessionID == "" {
		return result, ErrDouyinInteractionUnavailable
	}
	id := attached.SessionID
	for _, call := range []struct {
		method string
		params any
	}{{"Page.enable", map[string]any{}}, {"Network.enable", map[string]any{"maxPostDataSize": 8192, "maxResourceBufferSize": 128 << 10, "maxTotalBufferSize": 1 << 20}}, {"Performance.enable", map[string]any{}}, {"Browser.setDownloadBehavior", map[string]any{"behavior": "deny", "eventsEnabled": false}}, {"Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": "window.open=()=>null;"}}} {
		if err := session.Call(ctx, id, call.method, call.params, nil); err != nil {
			return result, ErrDouyinInteractionUnavailable
		}
	}
	if err := authorize(); err != nil {
		result.State, result.Code = DouyinInteractionCancelled, "authorization_changed"
		return result, err
	}
	var nav struct {
		FrameID, LoaderID, ErrorText string
		IsDownload                   bool
	}
	if err := session.Call(ctx, id, "Page.navigate", map[string]any{"url": target}, &nav); err != nil || nav.ErrorText != "" || nav.IsDownload || nav.FrameID == "" || nav.LoaderID == "" {
		return result, ErrDouyinInteractionUnavailable
	}
	receipt := newDouyinCommentLikeReceipt(target, id, nav.FrameID, nav.LoaderID, req)
	receipt.start(ctx, session)
	defer receipt.stop()
	var world int64
	for world == 0 {
		var err error
		world, err = createPlaybackWorld(ctx, session, id, nav.FrameID)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(60 * time.Millisecond):
		}
	}
	var state douyinCommentLikePageState
	for until := time.Now().Add(6 * time.Second); ; {
		if err := authorize(); err != nil {
			result.State, result.Code = DouyinInteractionCancelled, "authorization_changed"
			return result, err
		}
		var err error
		state, err = douyinCommentLikePage(ctx, session, id, world, target, "query", req)
		if err != nil {
			return result, err
		}
		if state.Ready && state.Code != "comment_unavailable" && sameDouyinHomeLocation(state.URL, target) {
			break
		}
		if time.Now().After(until) {
			result.Code = "comment_unavailable"
			return result, ErrDouyinInteractionUnknown
		}
		if err := creatorWait(ctx); err != nil {
			return result, err
		}
	}
	if state.Code != "" {
		result.Code = state.Code
		if state.Code == "comment_changed" || state.Code == "comment_author_changed" || state.Code == "comment_identity_ambiguous" {
			result.State = DouyinInteractionFailed
		}
		return result, ErrDouyinInteractionUnknown
	}
	if state.Liked {
		if err := authorize(); err != nil {
			result.State, result.Code = DouyinInteractionCancelled, "authorization_changed"
			return result, err
		}
		result.State, result.Code = DouyinInteractionApplied, "already_applied"
		return result, nil
	}
	if req.Submitted {
		result.Code = "receipt_not_found"
		return result, ErrDouyinInteractionUnknown
	}
	var metrics struct {
		Metrics []struct {
			Name  string
			Value float64
		}
	}
	if err := session.Call(ctx, id, "Performance.getMetrics", map[string]any{}, &metrics); err != nil {
		return result, ErrDouyinInteractionUnavailable
	}
	var submitAfter float64
	for _, metric := range metrics.Metrics {
		if metric.Name == "Timestamp" {
			submitAfter = metric.Value
		}
	}
	if submitAfter <= 0 {
		return result, ErrDouyinInteractionUnavailable
	}
	if err := req.BeforeSubmit(ctx); err != nil {
		result.State, result.Code = DouyinInteractionCancelled, "cancelled_before_submit"
		return result, err
	}
	// The caller has now persisted an intent. Any uncertainty after this point
	// must be reconciled without another click.
	result.Submitted = true
	if err := authorize(); err != nil {
		result.State, result.Code = DouyinInteractionCancelled, "authorization_changed"
		return result, err
	}
	receipt.arm(submitAfter)
	state, err := douyinCommentLikePage(ctx, session, id, world, target, "submit", req)
	if err != nil {
		return result, err
	}
	if state.Code != "" || !state.Ready || !sameDouyinHomeLocation(state.URL, target) {
		result.Code = state.Code
		if result.Code == "" {
			result.Code = "submit_unknown"
		}
		return result, ErrDouyinInteractionUnknown
	}
	if !state.Clicked && !state.Liked {
		result.Code = "submit_unknown"
		return result, ErrDouyinInteractionUnknown
	}
	if !state.Clicked && state.Liked {
		if err := authorize(); err != nil {
			result.Code = "authorization_changed"
			return result, err
		}
		result.State, result.Code = DouyinInteractionApplied, "already_applied"
		return result, nil
	}
	for until := time.Now().Add(4 * time.Second); ; {
		if err := authorize(); err != nil {
			result.Code = "authorization_changed"
			return result, err
		}
		accepted, rejected, unsafe := receipt.snapshot()
		if unsafe {
			result.Code = "receipt_mismatch"
			return result, ErrDouyinInteractionUnknown
		}
		if rejected {
			result.State, result.Code = DouyinInteractionFailed, "platform_rejected"
			return result, ErrDouyinInteractionRejected
		}
		if accepted {
			if err := authorize(); err != nil {
				result.Code = "authorization_changed"
				return result, err
			}
			result.State, result.Code = DouyinInteractionApplied, "applied"
			return result, nil
		}
		if time.Now().After(until) {
			result.Code = "receipt_unknown"
			return result, ErrDouyinInteractionUnknown
		}
		if err := creatorWait(ctx); err != nil {
			return result, err
		}
	}
}
