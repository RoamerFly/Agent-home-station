//go:build windows

package stationbrowser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"wechat-bot/internal/hostfile"
)

// ReadDouyinCommentRelationTarget verifies exactly one comment already found
// through a notification. The official video page itself must emit the
// matching list response; this function does not call a signed API or scroll
// through comments to find a notification. Child replies require RootID and
// will remain unavailable unless the page naturally loads that reply list.
func ReadDouyinCommentRelationTarget(ctx context.Context, profile string, input DouyinCommentRelationRequest, authorize func() error) (DouyinCommentRelation, error) {
	if err := ctx.Err(); err != nil {
		return DouyinCommentRelation{}, err
	}
	if authorize == nil || !douyinCommentID.MatchString(input.WorkID) || !douyinCommentID.MatchString(input.TargetID) ||
		input.RootID != "" && !douyinCommentID.MatchString(input.RootID) {
		return DouyinCommentRelation{}, ErrDouyinCommentsUnavailable
	}
	target, err := DouyinVideoURL(input.WorkID)
	if err != nil {
		return DouyinCommentRelation{}, ErrDouyinCommentsUnavailable
	}
	lockPath, err := DouyinLoginProfileLockPath(profile)
	if err != nil {
		return DouyinCommentRelation{}, ErrDouyinCommentsUnavailable
	}
	release, err := hostfile.TryLock(lockPath)
	if err != nil {
		return DouyinCommentRelation{}, ErrDouyinLoginActive
	}
	defer release()

	readCtx, cancel := context.WithTimeout(ctx, pageLoadTimeout)
	defer cancel()
	session, err := openRestrictedCDP(readCtx, profile, isDouyinNoticeHost)
	if err != nil {
		return DouyinCommentRelation{}, fmt.Errorf("%w: %v", ErrDouyinCommentsUnavailable, err)
	}
	defer closeDouyinHomeSession(session)
	relation, readErr := readDouyinCommentRelationTargetSession(readCtx, session, target, input, authorize)
	if err := ctx.Err(); err != nil {
		return DouyinCommentRelation{}, err
	}
	if readCtx.Err() != nil {
		return DouyinCommentRelation{}, ErrDouyinCommentsUnavailable
	}
	return relation, readErr
}

func readDouyinCommentRelationTargetSession(ctx context.Context, session readerSession, target string, input DouyinCommentRelationRequest, authorize func() error) (DouyinCommentRelation, error) {
	expected, _ := DouyinVideoURL(input.WorkID)
	if session == nil || authorize == nil || target != expected || !isDouyinVideoURL(target) {
		return DouyinCommentRelation{}, ErrDouyinCommentsUnavailable
	}
	var created struct {
		TargetID string `json:"targetId"`
	}
	if session.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank", "background": true}, &created) != nil || created.TargetID == "" {
		return DouyinCommentRelation{}, ErrDouyinCommentsUnavailable
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if session.Call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": created.TargetID, "flatten": true}, &attached) != nil || attached.SessionID == "" {
		return DouyinCommentRelation{}, ErrDouyinCommentsUnavailable
	}
	for _, call := range []struct {
		method string
		params any
	}{
		{"Page.enable", map[string]any{}},
		{"Network.enable", map[string]any{}},
		{"Network.setCacheDisabled", map[string]any{"cacheDisabled": true}},
		{"Network.setBypassServiceWorker", map[string]any{"bypass": true}},
		{"Browser.setDownloadBehavior", map[string]any{"behavior": "deny", "eventsEnabled": false}},
		{"Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": "window.open=()=>null;"}},
		{"Fetch.enable", map[string]any{"patterns": []map[string]string{{"urlPattern": "*", "requestStage": "Request"}}, "handleAuthRequests": true}},
	} {
		if session.Call(ctx, attached.SessionID, call.method, call.params, nil) != nil {
			return DouyinCommentRelation{}, ErrDouyinCommentsUnavailable
		}
	}
	// The about:blank main frame keeps its ID across Page.navigate. Obtain it
	// before navigation so the listener cannot miss an early API response.
	var tree struct {
		FrameTree struct {
			Frame struct {
				ID string `json:"id"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	if session.Call(ctx, attached.SessionID, "Page.getFrameTree", map[string]any{}, &tree) != nil || tree.FrameTree.Frame.ID == "" {
		return DouyinCommentRelation{}, ErrDouyinCommentsUnavailable
	}
	if err := authorize(); err != nil {
		return DouyinCommentRelation{}, err
	}
	request := douyinCommentCaptureRequest{SessionID: attached.SessionID, FrameID: tree.FrameTree.Frame.ID,
		WorkID: input.WorkID, RootID: input.RootID, TargetID: input.TargetID}
	type outcome struct {
		page douyinCommentRelationPage
		err  error
	}
	listenCtx, stop := context.WithCancel(ctx)
	defer stop()
	result := make(chan outcome, 1)
	go func() {
		page, err := captureDouyinCommentRelationPage(listenCtx, session, request, authorize)
		result <- outcome{page: page, err: err}
	}()
	var nav struct {
		FrameID    string `json:"frameId"`
		LoaderID   string `json:"loaderId"`
		ErrorText  string `json:"errorText"`
		IsDownload bool   `json:"isDownload"`
	}
	if session.Call(ctx, attached.SessionID, "Page.navigate", map[string]any{"url": target}, &nav) != nil || nav.FrameID != request.FrameID || nav.LoaderID == "" || nav.ErrorText != "" || nav.IsDownload {
		return DouyinCommentRelation{}, ErrDouyinCommentsUnavailable
	}
	select {
	case got := <-result:
		if got.err != nil {
			return DouyinCommentRelation{}, got.err
		}
		if got.page.loaderID != nav.LoaderID {
			return DouyinCommentRelation{}, ErrDouyinCommentsUnavailable
		}
		for _, relation := range got.page.Relations {
			if relation.CID == input.TargetID {
				return relation, nil
			}
		}
		return DouyinCommentRelation{}, ErrDouyinCommentsUnavailable
	case <-ctx.Done():
		return DouyinCommentRelation{}, ctx.Err()
	}
}

// douyinCommentCaptureRequest binds one page-emitted response to a notification
// target. The caller must enable Network before causing the page to load the
// requested list. This reader neither generates signed requests nor expands a
// comment thread. A response that does not contain TargetID is not evidence.
type douyinCommentCaptureRequest struct {
	SessionID string
	FrameID   string
	WorkID    string
	RootID    string // empty for a main comment; otherwise the exact root comment
	TargetID  string
}

type douyinCommentRelationPage struct {
	Relations []DouyinCommentRelation
	HasMore   bool
	Cursor    string
	loaderID  string
}

type pendingDouyinCommentResponse struct {
	url      string
	loaderID string
	accepted bool
}

// captureDouyinCommentRelationPage passively observes one matching web response.
// The event channel is single-consumer: the caller must dedicate this CDP
// session to the capture until the function returns.
func captureDouyinCommentRelationPage(ctx context.Context, session readerSession, req douyinCommentCaptureRequest, authorize func() error) (douyinCommentRelationPage, error) {
	if session == nil || authorize == nil || req.SessionID == "" || req.FrameID == "" ||
		!douyinCommentID.MatchString(req.WorkID) || !douyinCommentID.MatchString(req.TargetID) ||
		req.RootID != "" && !douyinCommentID.MatchString(req.RootID) {
		return douyinCommentRelationPage{}, ErrDouyinCommentsUnavailable
	}
	if err := authorize(); err != nil {
		return douyinCommentRelationPage{}, err
	}
	requests := make(map[string]pendingDouyinCommentResponse)
	approved := make(map[string]string)
	listIssued := false
	for {
		select {
		case <-ctx.Done():
			return douyinCommentRelationPage{}, ctx.Err()
		case event, ok := <-session.Events():
			if !ok {
				return douyinCommentRelationPage{}, ErrDouyinCommentsUnavailable
			}
			if event.SessionID != req.SessionID {
				continue
			}
			switch event.Method {
			case "Fetch.requestPaused":
				var paused struct {
					RequestID          string `json:"requestId"`
					NetworkID          string `json:"networkId"`
					FrameID            string `json:"frameId"`
					ResourceType       string `json:"resourceType"`
					ResponseStatusCode int    `json:"responseStatusCode"`
					ResponseError      string `json:"responseErrorReason"`
					Request            struct{ URL, Method string }
				}
				if json.Unmarshal(event.Params, &paused) != nil || paused.RequestID == "" {
					return douyinCommentRelationPage{}, ErrDouyinCommentsUnavailable
				}
				if err := authorize(); err != nil {
					return douyinCommentRelationPage{}, err
				}
				allowed, comment := allowDouyinCommentCaptureRequest(req, paused.FrameID, paused.ResourceType,
					paused.Request.URL, paused.Request.Method)
				if paused.ResponseStatusCode != 0 || paused.ResponseError != "" ||
					comment && (listIssued || paused.NetworkID == "") {
					allowed = false
				}
				method := "Fetch.failRequest"
				params := map[string]any{"requestId": paused.RequestID, "errorReason": "BlockedByClient"}
				if allowed {
					method = "Fetch.continueRequest"
					params = map[string]any{"requestId": paused.RequestID}
				}
				if session.Call(ctx, req.SessionID, method, params, nil) != nil {
					return douyinCommentRelationPage{}, ErrDouyinCommentsUnavailable
				}
				if allowed && comment {
					listIssued = true
					approved[paused.NetworkID] = paused.Request.URL
				}
			case "Fetch.authRequired":
				var challenge struct {
					RequestID string `json:"requestId"`
				}
				if err := authorize(); err != nil {
					return douyinCommentRelationPage{}, err
				}
				if json.Unmarshal(event.Params, &challenge) != nil || challenge.RequestID == "" ||
					session.Call(ctx, req.SessionID, "Fetch.continueWithAuth", map[string]any{
						"requestId": challenge.RequestID, "authChallengeResponse": map[string]any{"response": "CancelAuth"},
					}, nil) != nil {
					return douyinCommentRelationPage{}, ErrDouyinCommentsUnavailable
				}
			case "Network.requestWillBeSent":
				var p struct {
					RequestID string `json:"requestId"`
					FrameID   string `json:"frameId"`
					LoaderID  string `json:"loaderId"`
					Request   struct{ URL, Method string }
				}
				if json.Unmarshal(event.Params, &p) != nil || p.RequestID == "" {
					continue
				}
				delete(requests, p.RequestID)
				if p.FrameID == req.FrameID && p.LoaderID != "" && p.Request.Method == "GET" && exactDouyinCommentListURL(p.Request.URL, req) {
					requests[p.RequestID] = pendingDouyinCommentResponse{url: p.Request.URL, loaderID: p.LoaderID}
				}
			case "Network.responseReceived":
				var p struct {
					RequestID string `json:"requestId"`
					Type      string `json:"type"`
					Response  struct {
						URL               string
						MimeType          string `json:"mimeType"`
						Status            int
						FromServiceWorker bool
					}
				}
				if json.Unmarshal(event.Params, &p) != nil {
					continue
				}
				pending, found := requests[p.RequestID]
				if !found || approved[p.RequestID] != pending.url {
					continue
				}
				if p.Response.Status != 200 || p.Response.FromServiceWorker || p.Response.URL != pending.url ||
					(p.Type != "XHR" && p.Type != "Fetch") || !strings.HasPrefix(strings.ToLower(p.Response.MimeType), "application/json") {
					delete(requests, p.RequestID)
					continue
				}
				pending.accepted = true
				requests[p.RequestID] = pending
			case "Network.loadingFailed":
				var p struct {
					RequestID string `json:"requestId"`
				}
				if json.Unmarshal(event.Params, &p) == nil {
					delete(requests, p.RequestID)
					delete(approved, p.RequestID)
				}
			case "Network.loadingFinished":
				var p struct {
					RequestID         string  `json:"requestId"`
					EncodedDataLength float64 `json:"encodedDataLength"`
				}
				if json.Unmarshal(event.Params, &p) != nil {
					continue
				}
				pending, found := requests[p.RequestID]
				delete(requests, p.RequestID)
				delete(approved, p.RequestID)
				if !found || !pending.accepted || p.EncodedDataLength < 0 || p.EncodedDataLength > 512<<10 {
					continue
				}
				if err := authorize(); err != nil {
					return douyinCommentRelationPage{}, err
				}
				if err := verifyDouyinCommentCapturePage(ctx, session, req); err != nil {
					return douyinCommentRelationPage{}, err
				}
				var out struct {
					Body          string
					Base64Encoded bool
				}
				if session.Call(ctx, req.SessionID, "Network.getResponseBody", map[string]any{"requestId": p.RequestID}, &out) != nil || len(out.Body) > 700<<10 {
					return douyinCommentRelationPage{}, ErrDouyinCommentsUnavailable
				}
				raw := []byte(out.Body)
				if out.Base64Encoded {
					var err error
					raw, err = base64.StdEncoding.DecodeString(out.Body)
					if err != nil {
						return douyinCommentRelationPage{}, ErrDouyinCommentsUnavailable
					}
				}
				if len(raw) > 512<<10 {
					return douyinCommentRelationPage{}, ErrDouyinCommentsUnavailable
				}
				relations, err := parseDouyinCommentRelations(raw, req.WorkID, req.RootID)
				if err != nil {
					return douyinCommentRelationPage{}, err
				}
				found = false
				for _, relation := range relations {
					if relation.CID == req.TargetID {
						found = true
						break
					}
				}
				if !found {
					if err := authorize(); err != nil {
						return douyinCommentRelationPage{}, err
					}
					if err := verifyDouyinCommentCapturePage(ctx, session, req); err != nil {
						return douyinCommentRelationPage{}, err
					}
					// One natural response is the complete scope of this targeted check.
					// A missing CID may live on another page; do not traverse it.
					return douyinCommentRelationPage{}, ErrDouyinCommentTargetPending
				}
				page, err := parseDouyinCommentRelationPage(raw, relations)
				if err != nil {
					return douyinCommentRelationPage{}, err
				}
				page.loaderID = pending.loaderID
				if err := authorize(); err != nil {
					return douyinCommentRelationPage{}, err
				}
				if err := verifyDouyinCommentCapturePage(ctx, session, req); err != nil {
					return douyinCommentRelationPage{}, err
				}
				return page, nil
			}
		}
	}
}

func exactDouyinCommentListURL(raw string, req douyinCommentCaptureRequest) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Scheme != "https" || u.Host != "www.douyin.com" || u.Fragment != "" {
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false
	}
	param := func(key, expected string) bool {
		v := q[key]
		return len(v) == 1 && v[0] == expected
	}
	if req.RootID == "" {
		return u.EscapedPath() == "/aweme/v1/web/comment/list/" && param("aweme_id", req.WorkID) &&
			(len(q["item_id"]) == 0 || param("item_id", req.WorkID))
	}
	return u.EscapedPath() == "/aweme/v1/web/comment/list/reply/" && param("item_id", req.WorkID) && param("comment_id", req.RootID) &&
		(len(q["aweme_id"]) == 0 || param("aweme_id", req.WorkID))
}

// allowDouyinCommentCaptureRequest is the request-stage fence for this reader.
// The page may supply its own signed query, but only one target comment-list
// GET can be admitted by captureDouyinCommentRelationPage. Other API routes,
// subframes, media, downloads and mutation methods cannot leave the browser.
func allowDouyinCommentCaptureRequest(req douyinCommentCaptureRequest, frameID, kind, raw, method string) (bool, bool) {
	if frameID == "" || frameID != req.FrameID || method != "GET" {
		return false, false
	}
	target, err := DouyinVideoURL(req.WorkID)
	if err != nil {
		return false, false
	}
	if kind == "Document" {
		return raw == target, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" ||
		u.Opaque != "" || u.Port() != "" || u.Host == "" || u.Host != strings.ToLower(u.Host) {
		return false, false
	}
	if kind == "Script" || kind == "Stylesheet" || kind == "Image" || kind == "Font" {
		return u.Host != "www.douyin.com" && isDouyinNoticeHost(u.Host), false
	}
	if (kind != "XHR" && kind != "Fetch") || u.Host != "www.douyin.com" {
		return false, false
	}
	if exactDouyinCommentListURL(raw, req) {
		return true, true
	}
	if u.EscapedPath() != douyinDetailPath {
		return false, false
	}
	q, err := url.ParseQuery(u.RawQuery)
	return err == nil && len(q["aid"]) == 1 && q.Get("aid") == "6383" &&
		len(q["aweme_id"]) == 1 && q.Get("aweme_id") == req.WorkID, false
}

func verifyDouyinCommentCapturePage(ctx context.Context, session readerSession, req douyinCommentCaptureRequest) error {
	target, err := DouyinVideoURL(req.WorkID)
	if err != nil {
		return ErrDouyinCommentsUnavailable
	}
	var out struct {
		Result struct {
			Type  string `json:"type"`
			Value struct {
				URL       string `json:"url"`
				LoginForm bool   `json:"loginForm"`
				Challenge bool   `json:"challenge"`
			} `json:"value"`
		} `json:"result"`
	}
	const expression = `(()=>({url:String(location.href||''),loginForm:!!document.querySelector('input[type=password],form[action*="login"],form[action*="passport"]'),challenge:!!document.querySelector('iframe[src*="captcha"],[class*="captcha"],[class*="verify"]')}))()`
	if session.Call(ctx, req.SessionID, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true}, &out) != nil || out.Result.Type != "object" ||
		!sameDouyinHomeLocation(out.Result.Value.URL, target) {
		return ErrDouyinCommentsUnavailable
	}
	if out.Result.Value.LoginForm || out.Result.Value.Challenge {
		return ErrDouyinWebLoginRequired
	}
	return nil
}

func parseDouyinCommentRelationPage(raw []byte, relations []DouyinCommentRelation) (douyinCommentRelationPage, error) {
	fields, ok := noticeObject(raw)
	if !ok {
		return douyinCommentRelationPage{}, ErrDouyinCommentsUnavailable
	}
	page := douyinCommentRelationPage{Relations: relations}
	value, present := fields["has_more"]
	if !present {
		return douyinCommentRelationPage{}, ErrDouyinCommentsUnavailable
	}
	var flag bool
	var count int
	switch {
	case json.Unmarshal(value, &flag) == nil:
		page.HasMore = flag
	case json.Unmarshal(value, &count) == nil && (count == 0 || count == 1):
		page.HasMore = count == 1
	default:
		return douyinCommentRelationPage{}, ErrDouyinCommentsUnavailable
	}
	if value, present := fields["cursor"]; present {
		if id, valid := commentRelationID(value, true); valid {
			page.Cursor = id
		} else {
			return douyinCommentRelationPage{}, ErrDouyinCommentsUnavailable
		}
	}
	if page.HasMore && page.Cursor == "" {
		return douyinCommentRelationPage{}, ErrDouyinCommentsUnavailable
	}
	return page, nil
}
