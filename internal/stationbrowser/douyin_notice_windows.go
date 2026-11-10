//go:build windows

package stationbrowser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"wechat-bot/internal/hostfile"
)

// DouyinNoticeAccount probes the signed-in account without opening the
// notification panel. Use its fingerprint as the subject of the owner's grant.
func DouyinNoticeAccount(ctx context.Context, profile string, authorize func() error) (string, error) {
	page, err := readDouyinNotice(ctx, profile, DouyinNoticePageURL, nil, authorize, false)
	return page.Account, err
}

func ReadDouyinNotice(ctx context.Context, profile string, input DouyinNoticeRequest, authorize func() error) (DouyinNoticePage, error) {
	if input.Account == "" {
		return DouyinNoticePage{}, ErrDouyinNoticeAccountChanged
	}
	return readDouyinNotice(ctx, profile, DouyinNoticePageURL, &input, authorize, false)
}

func readDouyinNotice(ctx context.Context, profile, target string, input *DouyinNoticeRequest, authorize func() error, fixture bool) (DouyinNoticePage, error) {
	if authorize == nil || (!fixture && target != DouyinNoticePageURL) {
		return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
	}
	if err := authorize(); err != nil {
		return DouyinNoticePage{}, err
	}
	lock, err := DouyinLoginProfileLockPath(profile)
	if err != nil {
		return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
	}
	release, err := hostfile.TryLock(lock)
	if err != nil {
		return DouyinNoticePage{}, ErrDouyinLoginActive
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var session *cdpSession
	if fixture {
		session, err = openCDP(ctx, profile)
	} else {
		session, err = openRestrictedCDPWithCapacity(ctx, profile, isDouyinNoticeHost, 128)
	}
	if err != nil {
		return DouyinNoticePage{}, ErrBrowserUnavailable
	}
	defer closeDouyinHomeSession(session)
	return readDouyinNoticeSession(ctx, session, target, input, authorize)
}

func isDouyinNoticeHost(host string) bool {
	switch host {
	case "www.douyin.com", "lf-douyin-pc-web.douyinstatic.com", "lf-c-flwb.bytetos.com", "lf-cdn-tos.bytescm.com", "lf3-static.bytednsdoc.com":
		return true
	}
	return false
}

type noticeReadOutcome struct {
	page DouyinNoticePage
	err  error
}

type noticeRequestFence struct {
	target, origin, sessionID string
	authorize                 func() error
	armed                     atomic.Bool
	allowMarkRead             bool
}

func (f *noticeRequestFence) allowResource(raw, kind string) bool {
	if kind == "Document" {
		return raw == f.target
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return false
	}
	if u.Scheme+"://"+u.Host == f.origin {
		values, err := url.ParseQuery(u.RawQuery)
		return err == nil && u.EscapedPath() == "/aweme/v1/web/notice/count/" &&
			values.Get("is_new_notice") == "1" && values.Get("need_social_count") == "1"
	}
	// These are static assets referenced by the public help page. Requests
	// carrying user data or an unrecognized method never pass this fence.
	return u.Scheme == "https" && u.Host != "www.douyin.com" && isDouyinNoticeHost(u.Host) &&
		(kind == "Script" || kind == "Stylesheet" || kind == "Image" || kind == "Font")
}

func (f *noticeRequestFence) run(ctx context.Context, session readerSession, out chan<- noticeReadOutcome) {
	var admitted string
	settled := false
	report := func(page DouyinNoticePage, err error) {
		if !settled {
			settled = true
			out <- noticeReadOutcome{page: page, err: err}
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-session.Events():
			if !ok {
				report(DouyinNoticePage{}, ErrDouyinNoticeUnavailable)
				return
			}
			if event.SessionID != f.sessionID || event.Method != "Fetch.requestPaused" {
				continue
			}
			var req struct {
				ID            string `json:"requestId"`
				Kind          string `json:"resourceType"`
				Status        int    `json:"responseStatusCode"`
				ResponseError string `json:"responseErrorReason"`
				Request       struct{ URL, Method string }
			}
			if json.Unmarshal(event.Params, &req) != nil || req.ID == "" {
				report(DouyinNoticePage{}, ErrDouyinNoticeUnavailable)
				continue
			}
			method := "Fetch.failRequest"
			params := map[string]any{"requestId": req.ID, "errorReason": "BlockedByClient"}
			mark, exact := noticeListRequest(req.Request.URL, f.origin)
			switch {
			case req.Status != 0 || req.ResponseError != "":
				if exact && req.ID == admitted && !settled {
					var page DouyinNoticePage
					err := ErrDouyinNoticeUnavailable
					if req.Status == 200 {
						page, err = readDouyinNoticeResponse(ctx, session, f.sessionID, req.ID)
					}
					if denied := f.authorize(); denied != nil {
						err = denied
					}
					if err == nil {
						method = "Fetch.continueResponse"
						params = map[string]any{"requestId": req.ID}
					}
					if session.Call(ctx, f.sessionID, method, params, nil) != nil && err == nil {
						err = ErrDouyinNoticeUnavailable
					}
					report(page, err)
					continue
				}
			case req.Request.Method == "GET" && exact && (req.Kind == "XHR" || req.Kind == "Fetch") && f.armed.Swap(false) && !settled:
				if err := f.authorize(); err != nil {
					report(DouyinNoticePage{}, err)
				} else if mark && !f.allowMarkRead {
					report(DouyinNoticePage{}, ErrDouyinNoticeMarksRead)
				} else {
					admitted = req.ID
					method = "Fetch.continueRequest"
					params = map[string]any{"requestId": req.ID, "interceptResponse": true}
				}
			case req.Request.Method == "GET" && f.allowResource(req.Request.URL, req.Kind):
				method = "Fetch.continueRequest"
				params = map[string]any{"requestId": req.ID}
			}
			if session.Call(ctx, f.sessionID, method, params, nil) != nil {
				report(DouyinNoticePage{}, ErrDouyinNoticeUnavailable)
			}
		}
	}
}

func readDouyinNoticeResponse(ctx context.Context, session readerSession, id, requestID string) (DouyinNoticePage, error) {
	var out struct {
		Body          string
		Base64Encoded bool
	}
	if session.Call(ctx, id, "Fetch.getResponseBody", map[string]any{"requestId": requestID}, &out) != nil || len(out.Body) > 350<<10 {
		return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
	}
	raw := []byte(out.Body)
	if out.Base64Encoded {
		var err error
		raw, err = base64.StdEncoding.DecodeString(out.Body)
		if err != nil {
			return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
		}
	}
	return parseDouyinNoticePage(ctx, raw)
}

func readDouyinNoticeSession(ctx context.Context, session readerSession, target string, input *DouyinNoticeRequest, authorize func() error) (DouyinNoticePage, error) {
	var created struct{ TargetID string }
	var attached struct{ SessionID string }
	if session.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank", "background": true}, &created) != nil || created.TargetID == "" ||
		session.Call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": created.TargetID, "flatten": true}, &attached) != nil || attached.SessionID == "" {
		return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
	}
	id := attached.SessionID
	for _, call := range []struct {
		method string
		params any
	}{
		{"Page.enable", map[string]any{}},
		{"Network.enable", map[string]any{}},
		{"Network.setCacheDisabled", map[string]any{"cacheDisabled": true}},
		{"Network.setBypassServiceWorker", map[string]any{"bypass": true}},
		{"Browser.setDownloadBehavior", map[string]any{"behavior": "deny"}},
		{"Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": "window.open=()=>null;"}},
		{"Fetch.enable", map[string]any{"patterns": []map[string]string{{"urlPattern": "*", "requestStage": "Request"}}}},
	} {
		if session.Call(ctx, id, call.method, call.params, nil) != nil {
			return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
		}
	}
	u, _ := url.Parse(target)
	fence := noticeRequestFence{target: target, origin: u.Scheme + "://" + u.Host, sessionID: id, authorize: authorize,
		allowMarkRead: input != nil && input.AllowMarkRead}
	fenceCtx, cancel := context.WithCancel(ctx)
	finished := make(chan struct{})
	out := make(chan noticeReadOutcome, 1)
	go func() { defer close(finished); fence.run(fenceCtx, session, out) }()
	defer func() { cancel(); <-finished }()
	if err := authorize(); err != nil {
		return DouyinNoticePage{}, err
	}
	var nav struct {
		FrameID, ErrorText string
		IsDownload         bool
	}
	if session.Call(ctx, id, "Page.navigate", map[string]any{"url": target}, &nav) != nil || nav.ErrorText != "" || nav.IsDownload {
		return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
	}
	var world int64
	var account string
	for until := time.Now().Add(20 * time.Second); time.Now().Before(until); {
		if err := authorize(); err != nil {
			return DouyinNoticePage{}, err
		}
		if world == 0 {
			world, _ = createPlaybackWorld(ctx, session, id, nav.FrameID)
		}
		if world != 0 {
			account = noticePageState(ctx, session, id, world, target, false).Account
			if account != "" {
				break
			}
		}
		if err := creatorWait(ctx); err != nil {
			return DouyinNoticePage{}, err
		}
	}
	if account == "" {
		return DouyinNoticePage{}, ErrDouyinWebLoginRequired
	}
	if input == nil {
		return DouyinNoticePage{Account: account}, authorize()
	}
	if account != input.Account {
		return DouyinNoticePage{}, ErrDouyinNoticeAccountChanged
	}
	if err := authorize(); err != nil {
		return DouyinNoticePage{}, err
	}
	control := noticePageState(ctx, session, id, world, target, true)
	if control.Account != account {
		return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
	}
	fence.armed.Store(true)
	if session.Call(ctx, id, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": control.X, "y": control.Y}, nil) != nil {
		return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
	}
	select {
	case <-ctx.Done():
		return DouyinNoticePage{}, ctx.Err()
	case result := <-out:
		if result.err != nil {
			return DouyinNoticePage{}, result.err
		}
		if err := authorize(); err != nil {
			return DouyinNoticePage{}, err
		}
		if noticePageState(ctx, session, id, world, target, false).Account != account {
			return DouyinNoticePage{}, ErrDouyinNoticeAccountChanged
		}
		if len(result.page.Items) != 0 {
			textCtx, stop := context.WithTimeout(ctx, 3*time.Second)
			defer stop()
			for textCtx.Err() == nil {
				if err := authorize(); err != nil {
					return DouyinNoticePage{}, err
				}
				result.page.RenderedText = noticeRenderedText(textCtx, session, id, world, target)
				if result.page.RenderedText != "" || creatorWait(textCtx) != nil {
					break
				}
			}
			if noticePageState(ctx, session, id, world, target, false).Account != account {
				return DouyinNoticePage{}, ErrDouyinNoticeAccountChanged
			}
		}
		result.page.Account = account
		return result.page, authorize()
	}
}

func noticeRenderedText(ctx context.Context, session readerSession, id string, world int64, target string) string {
	arg, _ := json.Marshal(target)
	// This container and list class are from the official NoticeEntry bundle.
	// A missing or changed UI supplies no text and leaves wire metadata intact.
	const js = `((target) => {
		if (location.href !== target) return '';
		const panels = [...document.querySelectorAll('[data-e2e="listDlgTest-container"]')].filter(node => node.getClientRects().length);
		if (panels.length !== 1) return '';
		const lists = panels[0].querySelectorAll('.sKMx4DKd');
		if (lists.length !== 1 || !lists[0].getClientRects().length) return '';
		return [...lists[0].innerText].slice(0,4000).join('');
	})`
	var out struct{ Result struct{ Value string } }
	if session.Call(ctx, id, "Runtime.evaluate", map[string]any{"expression": js + "(" + string(arg) + ")", "contextId": world, "returnByValue": true}, &out) != nil {
		return ""
	}
	lines := strings.Split(out.Result.Value, "\n")
	text := lines[:0]
	for _, line := range lines {
		line = strings.TrimSpace(line)
		switch line {
		case "", "点击加载更多", "暂时没有更多了", "暂无互动消息":
		default:
			text = append(text, line)
		}
	}
	return strings.Join(text, "\n")
}

type douyinNoticeControl struct {
	Account string
	X, Y    float64
}

func noticePageState(ctx context.Context, session readerSession, id string, world int64, target string, requireControl bool) douyinNoticeControl {
	arg, _ := json.Marshal([]any{target, requireControl})
	const js = `(([target, requireControl]) => {
		if (location.href !== target || document.readyState !== 'complete') return null;
		const data = document.querySelector('script#RENDER_DATA[type="application/json"]');
		if (!data || data.textContent.length > 2000000) return null;
		let odin;
		try { odin = JSON.parse(decodeURIComponent(data.textContent)).app?.odin; } catch { return null; }
		if (odin?.user_is_auth !== 1 || typeof odin.user_id !== 'string' || !/^[1-9][0-9]{0,19}$/.test(odin.user_id)) return null;
		if (!requireControl) return {account:odin.user_id};
		const controls = [...document.querySelectorAll('[data-e2e="notice-entry"] [data-e2e="something-button"]')];
		if (controls.length !== 1) return null;
		const control = controls[0], rect = control.getBoundingClientRect();
		const x = rect.left + rect.width / 2, y = rect.top + rect.height / 2;
		if (!rect.width || !rect.height || !control.contains(document.elementFromPoint(x,y))) return null;
		return {account:odin.user_id,x,y};
	})`
	var out struct {
		Result struct{ Value douyinNoticeControl }
	}
	if session.Call(ctx, id, "Runtime.evaluate", map[string]any{"expression": js + "(" + string(arg) + ")", "contextId": world, "returnByValue": true}, &out) != nil ||
		!douyinVideoID.MatchString(out.Result.Value.Account) {
		return douyinNoticeControl{}
	}
	out.Result.Value.Account = creatorWorksDigest("douyin-notice-account\x00" + out.Result.Value.Account)
	return out.Result.Value
}
