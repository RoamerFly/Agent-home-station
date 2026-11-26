//go:build windows

package stationbrowser

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"sync"
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
	next *noticeIntent
	err  error
}

type noticeRequestFence struct {
	target, origin, sessionID string
	authorize                 func() error
	intent                    atomic.Pointer[noticeIntent]
	mainFrame                 atomic.Pointer[string]
	closed                    atomic.Bool
	allowMarkRead             bool
	after                     DouyinNoticeCheckpoint
	pages                     int
	detailMu                  sync.Mutex
	details                   map[string]DouyinNoticeDetail
	detailConflicts           map[string]bool
	detailInvalid             atomic.Bool
	detailInFlight            atomic.Int32
}

type noticeIntent struct {
	frame    string
	position noticePosition
}

type noticeDetailIntent struct {
	frame     string
	urlDigest [32]byte
	refs      []noticeDetailRef
}

func (f *noticeRequestFence) recordDetails(items []DouyinNoticeDetail) {
	f.detailMu.Lock()
	defer f.detailMu.Unlock()
	if f.details == nil {
		f.details = make(map[string]DouyinNoticeDetail)
		f.detailConflicts = make(map[string]bool)
	}
	for _, item := range items {
		if f.detailConflicts[item.NoticeID] {
			continue
		}
		if previous, found := f.details[item.NoticeID]; found && !reflect.DeepEqual(previous, item) {
			delete(f.details, item.NoticeID)
			f.detailConflicts[item.NoticeID] = true
			continue
		}
		f.details[item.NoticeID] = item
	}
}

func (f *noticeRequestFence) snapshotDetails(items []DouyinNotice) ([]DouyinNoticeDetail, string) {
	f.detailMu.Lock()
	defer f.detailMu.Unlock()
	var details []DouyinNoticeDetail
	progress := ""
	for _, item := range items {
		if f.detailConflicts[item.ID] {
			progress = "conflict"
			continue
		}
		if detail, found := f.details[item.ID]; found {
			details = append(details, detail)
		}
	}
	if progress == "" && f.detailInvalid.Load() {
		progress = "invalid_response"
	}
	if progress == "" && f.detailInFlight.Load() != 0 {
		progress = "in_flight"
	}
	if progress == "" && len(details) == 0 {
		progress = "not_observed"
	} else if progress == "" && len(details) < len(items) {
		progress = "partial"
	}
	return details, progress
}

func checkNoticeAccount(ctx context.Context, session readerSession, id string, world int64, target, account string, authorize func() error) error {
	if err := authorize(); err != nil {
		return err
	}
	switch current := noticePageState(ctx, session, id, world, target, false).Account; current {
	case "":
		return ErrDouyinNoticeUnavailable
	case account:
		return nil
	default:
		return ErrDouyinNoticeAccountChanged
	}
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
	var admittedIntent noticeIntent
	var checkpoint DouyinNoticeCheckpoint
	seen := map[noticePosition]bool{}
	known := map[string]int{}
	detailAdmitted := map[string]noticeDetailIntent{}
	failed := false
	report := func(outcome noticeReadOutcome) {
		if failed {
			return
		}
		if outcome.err != nil {
			failed = true
			f.intent.Store(nil)
		}
		select {
		case out <- outcome:
		case <-ctx.Done():
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-session.Events():
			if !ok {
				report(noticeReadOutcome{err: ErrDouyinNoticeUnavailable})
				return
			}
			if event.SessionID != f.sessionID || event.Method != "Fetch.requestPaused" {
				continue
			}
			var req struct {
				ID            string `json:"requestId"`
				Frame         string `json:"frameId"`
				Kind          string `json:"resourceType"`
				Status        int    `json:"responseStatusCode"`
				ResponseError string `json:"responseErrorReason"`
				Request       struct{ URL, Method string }
			}
			if json.Unmarshal(event.Params, &req) != nil || req.ID == "" {
				report(noticeReadOutcome{err: ErrDouyinNoticeUnavailable})
				continue
			}
			method := "Fetch.failRequest"
			params := map[string]any{"requestId": req.ID, "errorReason": "BlockedByClient"}
			position, mark, exact := noticeListRequest(req.Request.URL, f.origin)
			detailRefs, detailExact := noticeDetailRequest(req.Request.URL, f.origin, known)
			intent := f.intent.Load()
			mainFrame := f.mainFrame.Load()
			switch {
			case req.Status != 0 || req.ResponseError != "":
				if detail, found := detailAdmitted[req.ID]; found {
					delete(detailAdmitted, req.ID)
					if req.Frame != detail.frame || sha256.Sum256([]byte(req.Request.URL)) != detail.urlDigest || req.ResponseError != "" {
						report(noticeReadOutcome{err: ErrDouyinNoticeUnavailable})
					} else if err := f.authorize(); err != nil {
						report(noticeReadOutcome{err: err})
					} else {
						if req.Status == 200 {
							if items, err := readDouyinNoticeDetailResponse(ctx, session, f.sessionID, req.ID, detail.refs); err == nil {
								f.recordDetails(items)
							} else {
								f.detailInvalid.Store(true)
							}
						} else {
							f.detailInvalid.Store(true)
						}
						method = "Fetch.continueResponse"
						params = map[string]any{"requestId": req.ID}
					}
					if session.Call(ctx, f.sessionID, method, params, nil) != nil {
						report(noticeReadOutcome{err: ErrDouyinNoticeUnavailable})
					}
					f.detailInFlight.Add(-1)
					continue
				}
				if exact && req.ID == admitted && req.Frame == admittedIntent.frame && !failed {
					admitted = ""
					var page DouyinNoticePage
					var nextIntent *noticeIntent
					err := ErrDouyinNoticeUnavailable
					if req.Status == 200 {
						page, err = readDouyinNoticeResponse(ctx, session, f.sessionID, req.ID)
					}
					if denied := f.authorize(); denied != nil {
						err = denied
					}
					if err == nil {
						for _, item := range page.Items {
							if previous, found := known[item.ID]; found && previous != item.Type {
								known[item.ID] = -1
							} else if !found {
								known[item.ID] = item.Type
							}
						}
						checkpoint = noticeCheckpoint(checkpoint, admittedIntent.position, page)
						if checkpoint.Pages <= f.after.Pages && (!page.HasMore ||
							checkpoint.Pages == f.after.Pages && checkpoint != f.after) {
							report(noticeReadOutcome{err: ErrDouyinNoticeCursorChanged})
							_ = session.Call(ctx, f.sessionID, method, params, nil)
							continue
						}
						next := noticePosition{min: page.MinTime, max: page.MaxTime, group: admittedIntent.position.group}
						if page.HasMore && !seen[next] {
							page.Next = checkpoint
							if !f.closed.Load() && checkpoint.Pages-f.after.Pages < f.pages {
								nextIntent = &noticeIntent{frame: admittedIntent.frame, position: next}
							}
						} else if page.HasMore {
							page.Progress = "cursor_stalled"
						}
						method = "Fetch.continueResponse"
						params = map[string]any{"requestId": req.ID}
					}
					if session.Call(ctx, f.sessionID, method, params, nil) != nil && err == nil {
						err = ErrDouyinNoticeUnavailable
					}
					page.PagesRead = checkpoint.Pages
					report(noticeReadOutcome{page: page, next: nextIntent, err: err})
					continue
				}
			case !failed && !f.closed.Load() && f.allowMarkRead && len(detailAdmitted) < 20 && mainFrame != nil &&
				req.Frame == *mainFrame && req.Request.Method == "GET" &&
				(req.Kind == "XHR" || req.Kind == "Fetch") && detailExact && len(detailRefs) != 0:
				if err := f.authorize(); err != nil {
					report(noticeReadOutcome{err: err})
				} else {
					detailAdmitted[req.ID] = noticeDetailIntent{frame: req.Frame, urlDigest: sha256.Sum256([]byte(req.Request.URL)), refs: detailRefs}
					f.detailInFlight.Add(1)
					method = "Fetch.continueRequest"
					params = map[string]any{"requestId": req.ID, "interceptResponse": true}
				}
			case !failed && !f.closed.Load() && admitted == "" && intent != nil && req.Frame == intent.frame && req.Request.Method == "GET" && exact &&
				(req.Kind == "XHR" || req.Kind == "Fetch") && position.min == intent.position.min && position.max == intent.position.max &&
				(intent.position.group == "" || position.group == intent.position.group) && f.intent.CompareAndSwap(intent, nil):
				if err := f.authorize(); err != nil {
					report(noticeReadOutcome{err: err})
				} else if mark && !f.allowMarkRead {
					report(noticeReadOutcome{err: ErrDouyinNoticeMarksRead})
				} else {
					admitted = req.ID
					admittedIntent = noticeIntent{frame: req.Frame, position: position}
					seen[position] = true
					method = "Fetch.continueRequest"
					params = map[string]any{"requestId": req.ID, "interceptResponse": true}
				}
			case req.Request.Method == "GET" && f.allowResource(req.Request.URL, req.Kind):
				method = "Fetch.continueRequest"
				params = map[string]any{"requestId": req.ID}
			}
			if session.Call(ctx, f.sessionID, method, params, nil) != nil {
				if _, found := detailAdmitted[req.ID]; found && method == "Fetch.continueRequest" {
					delete(detailAdmitted, req.ID)
					f.detailInFlight.Add(-1)
				}
				report(noticeReadOutcome{err: ErrDouyinNoticeUnavailable})
			}
		}
	}
}

func readDouyinNoticeResponse(ctx context.Context, session readerSession, id, requestID string) (DouyinNoticePage, error) {
	raw, err := readDouyinNoticeBody(ctx, session, id, requestID)
	if err != nil {
		return DouyinNoticePage{}, err
	}
	return parseDouyinNoticePage(ctx, raw)
}

func readDouyinNoticeDetailResponse(ctx context.Context, session readerSession, id, requestID string, refs []noticeDetailRef) ([]DouyinNoticeDetail, error) {
	raw, err := readDouyinNoticeBody(ctx, session, id, requestID)
	if err != nil {
		return nil, err
	}
	return parseDouyinNoticeDetail(ctx, raw, refs)
}

func readDouyinNoticeBody(ctx context.Context, session readerSession, id, requestID string) ([]byte, error) {
	var out struct {
		Body          string
		Base64Encoded bool
	}
	if session.Call(ctx, id, "Fetch.getResponseBody", map[string]any{"requestId": requestID}, &out) != nil || len(out.Body) > 350<<10 {
		return nil, ErrDouyinNoticeUnavailable
	}
	raw := []byte(out.Body)
	if out.Base64Encoded {
		var err error
		raw, err = base64.StdEncoding.DecodeString(out.Body)
		if err != nil {
			return nil, ErrDouyinNoticeUnavailable
		}
	}
	return raw, nil
}

func readDouyinNoticeSession(ctx context.Context, session readerSession, target string, input *DouyinNoticeRequest, authorize func() error) (DouyinNoticePage, error) {
	pages := 1
	if input != nil && input.Pages != 0 {
		pages = input.Pages
	}
	if pages < 1 || pages > MaxDouyinNoticePages || input != nil &&
		(input.After.Pages < 0 || input.After.Pages > int(^uint(0)>>1)-pages || (input.After.Pages == 0) != (input.After.Digest == [32]byte{})) {
		return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
	}
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
		allowMarkRead: input != nil && input.AllowMarkRead, pages: pages}
	if input != nil {
		fence.after = input.After
	}
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
	fence.mainFrame.Store(&nav.FrameID)
	var world int64
	var account, creatorAccount string
	for until := time.Now().Add(20 * time.Second); time.Now().Before(until); {
		if err := authorize(); err != nil {
			return DouyinNoticePage{}, err
		}
		if world == 0 {
			world, _ = createPlaybackWorld(ctx, session, id, nav.FrameID)
		}
		if world != 0 {
			state := noticePageState(ctx, session, id, world, target, false)
			account, creatorAccount = state.Account, state.CreatorAccount
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
		return DouyinNoticePage{Account: account, CreatorAccount: creatorAccount}, authorize()
	}
	if account != input.Account {
		return DouyinNoticePage{}, ErrDouyinNoticeAccountChanged
	}
	if err := authorize(); err != nil {
		return DouyinNoticePage{}, err
	}
	control := noticePageState(ctx, session, id, world, target, true)
	if control.Account != "" && (control.Account != account || control.CreatorAccount != creatorAccount) {
		return DouyinNoticePage{}, ErrDouyinNoticeAccountChanged
	}
	if control.Account != account {
		return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
	}
	fence.intent.Store(&noticeIntent{frame: nav.FrameID})
	if session.Call(ctx, id, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": control.X, "y": control.Y}, nil) != nil {
		return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
	}
	collected := DouyinNoticePage{Account: account, CreatorAccount: creatorAccount, HasMore: true}
	seenIDs := map[string]bool{}
	for {
		wait, stop := context.WithTimeout(ctx, 8*time.Second)
		result := waitNoticePage(wait, session, id, world, target, account, &fence, out, authorize)
		stop()
		if result.err != nil {
			if result.err != context.DeadlineExceeded || collected.PagesRead == 0 || ctx.Err() != nil {
				return DouyinNoticePage{}, result.err
			}
			collected.Progress = "next_unavailable"
			break
		}
		// Arm only after this result is available to the caller loop. The
		// next request needs a new identity check before the page is advanced.
		if err := checkNoticeAccount(ctx, session, id, world, target, account, authorize); err != nil {
			return DouyinNoticePage{}, err
		}
		fence.intent.Store(result.next)
		if err := authorize(); err != nil {
			return DouyinNoticePage{}, err
		}
		for _, item := range result.page.Items {
			if result.page.PagesRead > input.After.Pages && !seenIDs[item.ID] {
				collected.Items = append(collected.Items, item)
			}
			seenIDs[item.ID] = true
		}
		collected.MinTime, collected.MaxTime = result.page.MinTime, result.page.MaxTime
		collected.HasMore, collected.Next, collected.Progress = result.page.HasMore, result.page.Next, result.page.Progress
		collected.PagesRead = max(0, result.page.PagesRead-input.After.Pages)
		if !collected.HasMore || collected.PagesRead == pages || collected.Progress != "" {
			if collected.PagesRead == 0 {
				return DouyinNoticePage{}, ErrDouyinNoticeCursorChanged
			}
			break
		}
	}
	fence.intent.Store(nil)
	// Let the page finish natural, list-triggered detail GETs. The fence has
	// no list intent now, so this short window cannot admit another page.
	if len(collected.Items) != 0 {
		settle, stop := context.WithTimeout(ctx, 300*time.Millisecond)
		for settle.Err() == nil {
			if err := authorize(); err != nil {
				stop()
				return DouyinNoticePage{}, err
			}
			if creatorWait(settle) != nil {
				break
			}
		}
		stop()
	}
	// Close the request gate before reading the rendered list. Its text is
	// presentation evidence, never authority to admit an additional page.
	fence.closed.Store(true)
	fence.intent.Store(nil)
	if fence.detailInFlight.Load() != 0 {
		pending, stop := context.WithTimeout(ctx, time.Second)
		for pending.Err() == nil && fence.detailInFlight.Load() != 0 {
			if err := authorize(); err != nil {
				stop()
				return DouyinNoticePage{}, err
			}
			if creatorWait(pending) != nil {
				break
			}
		}
		stop()
	}
	if len(collected.Items) != 0 {
		textCtx, stop := context.WithTimeout(ctx, 3*time.Second)
		defer stop()
		for textCtx.Err() == nil {
			if err := authorize(); err != nil {
				return DouyinNoticePage{}, err
			}
			collected.RenderedText = noticeRenderedText(textCtx, session, id, world, target)
			if collected.RenderedText != "" || creatorWait(textCtx) != nil {
				break
			}
		}
	}
	if err := checkNoticeAccount(ctx, session, id, world, target, account, authorize); err != nil {
		return DouyinNoticePage{}, err
	}
	collected.Details, collected.DetailProgress = fence.snapshotDetails(collected.Items)
	return collected, nil
}

func waitNoticePage(ctx context.Context, session readerSession, id string, world int64, target, account string, fence *noticeRequestFence, out <-chan noticeReadOutcome, authorize func() error) noticeReadOutcome {
	advanced := false
	for {
		select {
		case result := <-out:
			return result
		case <-ctx.Done():
			return noticeReadOutcome{err: ctx.Err()}
		default:
		}
		intent := fence.intent.Load()
		if !advanced && intent != nil && intent.position.group != "" {
			if err := checkNoticeAccount(ctx, session, id, world, target, account, authorize); err != nil {
				return noticeReadOutcome{err: err}
			}
			advanced = advanceNoticePage(ctx, session, id, world, target)
		}
		if err := creatorWait(ctx); err != nil {
			return noticeReadOutcome{err: err}
		}
	}
}

func advanceNoticePage(ctx context.Context, session readerSession, id string, world int64, target string) bool {
	arg, _ := json.Marshal(target)
	const js = `((target) => {
		if (location.href !== target) return false;
		const panels = [...document.querySelectorAll('[data-e2e="listDlgTest-container"]')].filter(node => node.getClientRects().length);
		if (panels.length !== 1) return false;
		const lists = panels[0].querySelectorAll('.sKMx4DKd');
		if (lists.length !== 1 || !lists[0].getClientRects().length) return false;
		const list = lists[0];
		const more = [...list.querySelectorAll('.RQOLsL3h')].filter(node => node.getClientRects().length && node.textContent.trim() === '点击加载更多');
		if (more.length === 1) { more[0].click(); return true; }
		if (!more.length && list.scrollTop + list.clientHeight + 1 < list.scrollHeight) {
			list.scrollTop = list.scrollHeight;
			// Background Edge targets may defer their native scroll notification.
			// Notify the same DOM listener; the request fence still admits one page.
			list.dispatchEvent(new Event('scroll', {bubbles:true}));
			return true;
		}
		return false;
	})`
	var out struct{ Result struct{ Value bool } }
	return session.Call(ctx, id, "Runtime.evaluate", map[string]any{"expression": js + "(" + string(arg) + ")", "contextId": world, "returnByValue": true}, &out) == nil && out.Result.Value
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
	Account, CreatorAccount string
	X, Y                    float64
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
	uid := out.Result.Value.Account
	out.Result.Value.Account = creatorWorksDigest("douyin-notice-account\x00" + uid)
	out.Result.Value.CreatorAccount = creatorWorksDigest(uid)
	return out.Result.Value
}
