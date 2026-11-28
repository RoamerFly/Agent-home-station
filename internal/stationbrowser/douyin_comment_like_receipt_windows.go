//go:build windows

package stationbrowser

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
)

const douyinCommentDiggPath = "/aweme/v1/web/comment/digg/"

// A receipt is observed from the page's own request. The host never creates
// this POST and never retains its URL, signed parameters, or response body.
type douyinCommentLikeReceipt struct {
	origin, sessionID, frameID, loaderID, videoID, commentID string
	mu                                                       sync.Mutex
	after                                                    float64
	accepted, rejected, unsafe                               bool
	cancel                                                   context.CancelFunc
	done                                                     chan struct{}
}

type pendingDouyinCommentLike struct {
	urlHash  [32]byte
	response bool
}

func newDouyinCommentLikeReceipt(target, sessionID, frameID, loaderID string, req DouyinCommentLikeRequest) *douyinCommentLikeReceipt {
	u, _ := url.Parse(target)
	return &douyinCommentLikeReceipt{
		origin: u.Scheme + "://" + u.Host, sessionID: sessionID,
		frameID: frameID, loaderID: loaderID, videoID: req.VideoID, commentID: req.CommentID,
		done: make(chan struct{}),
	}
}

func (l *douyinCommentLikeReceipt) arm(timestamp float64) {
	l.mu.Lock()
	l.after = timestamp
	l.mu.Unlock()
}

func (l *douyinCommentLikeReceipt) snapshot() (accepted, rejected, unsafe bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.accepted, l.rejected, l.unsafe
}

func (l *douyinCommentLikeReceipt) stop() { l.cancel(); <-l.done }

func (l *douyinCommentLikeReceipt) isDiggEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.User == nil && u.Scheme+"://"+u.Host == l.origin && u.Path == douyinCommentDiggPath && u.Fragment == ""
}

func exactDouyinCommentDiggRequest(rawURL, postData string, hasPostData bool, origin, videoID, commentID string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.User != nil || u.Scheme+"://"+u.Host != origin || u.Path != douyinCommentDiggPath || u.Fragment != "" {
		return false
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["cid"]) != 1 || query.Get("cid") != commentID ||
		len(query["aweme_id"]) != 1 || query.Get("aweme_id") != videoID ||
		len(query["digg_type"]) != 1 || query.Get("digg_type") != "1" {
		return false
	}
	if hasPostData && postData == "" {
		return false
	}
	if postData != "" {
		form, err := url.ParseQuery(postData)
		if err != nil {
			return false
		}
		for _, field := range []string{"cid", "aweme_id", "digg_type"} {
			if len(form[field]) > 1 || (len(form[field]) == 1 && form.Get(field) != query.Get(field)) {
				return false
			}
		}
	}
	return true
}

func (l *douyinCommentLikeReceipt) start(parent context.Context, session readerSession) {
	ctx, cancel := context.WithCancel(parent)
	l.cancel = cancel
	go func() {
		defer close(l.done)
		pending := map[string]pendingDouyinCommentLike{}
		seen := 0
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-session.Events():
				if !ok {
					return
				}
				if event.SessionID != l.sessionID {
					continue
				}
				switch event.Method {
				case "Network.requestWillBeSent":
					var p struct {
						RequestID         string
						FrameID, LoaderID string
						Timestamp         float64
						Request           struct {
							URL, Method, PostData string
							HasPostData           bool
						}
					}
					if json.Unmarshal(event.Params, &p) != nil || p.RequestID == "" {
						continue
					}
					l.mu.Lock()
					armed := l.after > 0 && p.Timestamp > l.after
					l.mu.Unlock()
					if !armed || p.Request.Method != "POST" || !l.isDiggEndpoint(p.Request.URL) {
						continue
					}
					if p.FrameID != l.frameID || p.LoaderID != l.loaderID ||
						!exactDouyinCommentDiggRequest(p.Request.URL, p.Request.PostData, p.Request.HasPostData, l.origin, l.videoID, l.commentID) {
						l.mu.Lock()
						l.unsafe = true
						l.mu.Unlock()
						continue
					}
					seen++
					if seen != 1 {
						l.mu.Lock()
						l.unsafe = true
						l.mu.Unlock()
						continue
					}
					pending[p.RequestID] = pendingDouyinCommentLike{urlHash: sha256.Sum256([]byte(p.Request.URL))}
				case "Network.responseReceived":
					var p struct {
						RequestID string
						Type      string
						Response  struct {
							URL, MimeType     string
							Status            int
							FromServiceWorker bool
						}
					}
					if json.Unmarshal(event.Params, &p) != nil {
						continue
					}
					entry, found := pending[p.RequestID]
					if !found {
						continue
					}
					if sha256.Sum256([]byte(p.Response.URL)) != entry.urlHash || p.Response.FromServiceWorker ||
						(p.Type != "XHR" && p.Type != "Fetch") || !strings.HasPrefix(strings.ToLower(p.Response.MimeType), "application/json") {
						delete(pending, p.RequestID)
						l.mu.Lock()
						l.unsafe = true
						l.mu.Unlock()
						continue
					}
					if p.Response.Status != 200 {
						delete(pending, p.RequestID)
						l.mu.Lock()
						l.rejected = true
						l.mu.Unlock()
						continue
					}
					entry.response = true
					pending[p.RequestID] = entry
				case "Network.loadingFailed":
					var p struct{ RequestID string }
					if json.Unmarshal(event.Params, &p) == nil {
						delete(pending, p.RequestID)
					}
				case "Network.loadingFinished":
					var p struct {
						RequestID         string
						EncodedDataLength float64
					}
					if json.Unmarshal(event.Params, &p) != nil {
						continue
					}
					entry, found := pending[p.RequestID]
					delete(pending, p.RequestID)
					if !found || !entry.response || p.EncodedDataLength < 0 || p.EncodedDataLength > 64<<10 {
						continue
					}
					var out struct {
						Body          string
						Base64Encoded bool
					}
					if session.Call(ctx, l.sessionID, "Network.getResponseBody", map[string]any{"requestId": p.RequestID}, &out) != nil || len(out.Body) > 64<<10 {
						continue
					}
					body := []byte(out.Body)
					if out.Base64Encoded {
						var err error
						body, err = base64.StdEncoding.DecodeString(out.Body)
						if err != nil {
							continue
						}
					}
					var status struct {
						Code *int    `json:"status_code"`
						Msg  *string `json:"msg"`
					}
					if len(body) > 64<<10 || json.Unmarshal(body, &status) != nil || status.Code == nil || status.Msg == nil {
						continue
					}
					l.mu.Lock()
					if *status.Code == 0 && *status.Msg == "" {
						l.accepted = true
					} else {
						l.rejected = true
					}
					l.mu.Unlock()
				}
			}
		}
	}()
}
