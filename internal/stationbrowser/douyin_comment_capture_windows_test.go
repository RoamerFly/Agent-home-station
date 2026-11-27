//go:build windows

package stationbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type commentCaptureFixture struct {
	events     chan cdpEvent
	pageURL    string
	body       string
	bodyCalls  int
	requestURL string
	fetchOn    bool
	continued  []string
	blocked    []string
}

func (f *commentCaptureFixture) Call(_ context.Context, _, method string, params any, result any) error {
	switch method {
	case "Target.createTarget":
		putReaderResult(result, map[string]any{"targetId": "target-1"})
	case "Target.attachToTarget":
		putReaderResult(result, map[string]any{"sessionId": "session-1"})
	case "Page.getFrameTree":
		putReaderResult(result, map[string]any{"frameTree": map[string]any{"frame": map[string]any{"id": "frame-1"}}})
	case "Page.navigate":
		putReaderResult(result, map[string]any{"frameId": "frame-1", "loaderId": "loader-1"})
		if f.requestURL != "" {
			commentCaptureEvents(f, f.requestURL, "frame-1", "session-1")
		}
	case "Runtime.evaluate":
		putReaderResult(result, map[string]any{"result": map[string]any{"type": "object", "value": map[string]any{"url": f.pageURL}}})
	case "Network.getResponseBody":
		f.bodyCalls++
		putReaderResult(result, map[string]any{"body": f.body})
	case "Fetch.enable":
		f.fetchOn = true
	case "Fetch.continueRequest", "Fetch.failRequest":
		value, _ := params.(map[string]any)
		id, _ := value["requestId"].(string)
		if method == "Fetch.continueRequest" {
			f.continued = append(f.continued, id)
		} else {
			f.blocked = append(f.blocked, id)
		}
	}
	return nil
}

func TestReadDouyinCommentRelationTargetSessionReturnsOnlyNoticeTarget(t *testing.T) {
	f := newCommentCaptureFixture(`{"status_code":0,"comments":[{"aweme_id":"200","cid":"400","reply_id":"0","reply_to_reply_id":"0","user":{"uid":"901"},"text":"目标"},{"aweme_id":"200","cid":"401","reply_id":"0","reply_to_reply_id":"0","user":{"uid":"902"},"text":"其他"}],"has_more":false}`)
	f.requestURL = "https://www.douyin.com/aweme/v1/web/comment/list/?aweme_id=200&cursor=0"
	got, err := readDouyinCommentRelationTargetSession(context.Background(), f, "https://www.douyin.com/video/200",
		DouyinCommentRelationRequest{WorkID: "200", TargetID: "400"}, func() error { return nil })
	if err != nil || got.CID != "400" || got.Text != "目标" || f.bodyCalls != 1 || !f.fetchOn || len(f.continued) != 1 || f.continued[0] != "fetch-r1" {
		t.Fatalf("relation = %+v, body calls = %d, err = %v", got, f.bodyCalls, err)
	}
}

func TestReadDouyinCommentRelationTargetSessionFindsEmbeddedReply(t *testing.T) {
	f := newCommentCaptureFixture(`{"status_code":0,"comments":[{"aweme_id":"200","cid":"400","reply_id":"0","reply_to_reply_id":"0","user":{"uid":"901"},"text":"主评论","reply_comment":[{"aweme_id":"200","cid":"500","reply_id":"400","reply_to_reply_id":"0","user":{"uid":"900"},"text":"作者子评论","reply_comment":[]},{"aweme_id":"200","cid":"600","reply_id":"400","reply_to_reply_id":"500","user":{"uid":"901"},"text":"回复作者子评论","reply_comment":[]}]}],"has_more":false}`)
	f.requestURL = "https://www.douyin.com/aweme/v1/web/comment/list/?aweme_id=200&cursor=0"
	got, err := readDouyinCommentRelationTargetSession(context.Background(), f, "https://www.douyin.com/video/200",
		DouyinCommentRelationRequest{WorkID: "200", TargetID: "600"}, func() error { return nil })
	if err != nil || got.CID != "600" || got.ReplyID != "400" || got.ReplyToReplyID != "500" || f.bodyCalls != 1 {
		t.Fatalf("embedded reply relation = %+v, body calls = %d, err = %v", got, f.bodyCalls, err)
	}
}

func TestReadDouyinCommentRelationTargetSessionMissingCIDRemainsPending(t *testing.T) {
	f := newCommentCaptureFixture(`{"status_code":0,"comments":[{"aweme_id":"200","cid":"401","reply_id":"0","reply_to_reply_id":"0","user":{"uid":"902"},"text":"其他"}],"cursor":20,"has_more":1}`)
	f.requestURL = "https://www.douyin.com/aweme/v1/web/comment/list/?aweme_id=200&cursor=0"
	_, err := readDouyinCommentRelationTargetSession(context.Background(), f, "https://www.douyin.com/video/200",
		DouyinCommentRelationRequest{WorkID: "200", TargetID: "400"}, func() error { return nil })
	if !errors.Is(err, ErrDouyinCommentTargetPending) || f.bodyCalls != 1 {
		t.Fatalf("err = %v, body calls = %d", err, f.bodyCalls)
	}
}
func (f *commentCaptureFixture) Events() <-chan cdpEvent { return f.events }
func (f *commentCaptureFixture) Close() error            { return nil }

func commentCaptureEvent(method, session string, params any) cdpEvent {
	raw, _ := json.Marshal(params)
	return cdpEvent{Method: method, SessionID: session, Params: raw}
}

func commentCaptureEvents(f *commentCaptureFixture, requestURL, frameID, sessionID string) {
	f.events <- commentCaptureEvent("Network.requestWillBeSent", sessionID, map[string]any{
		"requestId": "r1", "frameId": frameID, "loaderId": "loader-1", "request": map[string]any{"url": requestURL, "method": "GET"},
	})
	f.events <- commentCaptureEvent("Fetch.requestPaused", sessionID, map[string]any{
		"requestId": "fetch-r1", "networkId": "r1", "frameId": frameID, "resourceType": "Fetch",
		"request": map[string]any{"url": requestURL, "method": "GET"},
	})
	f.events <- commentCaptureEvent("Network.responseReceived", sessionID, map[string]any{
		"requestId": "r1", "type": "Fetch", "response": map[string]any{"url": requestURL, "status": 200, "mimeType": "application/json"},
	})
	f.events <- commentCaptureEvent("Network.loadingFinished", sessionID, map[string]any{"requestId": "r1", "encodedDataLength": 300})
}

func newCommentCaptureFixture(body string) *commentCaptureFixture {
	return &commentCaptureFixture{events: make(chan cdpEvent, 8), pageURL: "https://www.douyin.com/video/200", body: body}
}

func TestAllowDouyinCommentCaptureRequest(t *testing.T) {
	req := douyinCommentCaptureRequest{FrameID: "frame-1", WorkID: "200", TargetID: "400"}
	for _, tc := range []struct {
		name, frame, kind, url, method string
		allowed, comment               bool
	}{
		{"video document", "frame-1", "Document", "https://www.douyin.com/video/200", "GET", true, false},
		{"other video", "frame-1", "Document", "https://www.douyin.com/video/201", "GET", false, false},
		{"script", "frame-1", "Script", "https://lf-douyin-pc-web.douyinstatic.com/web/app.js", "GET", true, false},
		{"target list", "frame-1", "Fetch", "https://www.douyin.com/aweme/v1/web/comment/list/?aweme_id=200&cursor=0", "GET", true, true},
		{"video detail", "frame-1", "XHR", "https://www.douyin.com/aweme/v1/web/aweme/detail/?aid=6383&aweme_id=200&msToken=x", "GET", true, false},
		{"wrong work", "frame-1", "Fetch", "https://www.douyin.com/aweme/v1/web/comment/list/?aweme_id=201", "GET", false, false},
		{"mutation", "frame-1", "Fetch", "https://www.douyin.com/aweme/v1/web/comment/publish/?aweme_id=200", "POST", false, false},
		{"other API", "frame-1", "XHR", "https://www.douyin.com/aweme/v1/web/user/profile/", "GET", false, false},
		{"subframe", "frame-2", "Fetch", "https://www.douyin.com/aweme/v1/web/comment/list/?aweme_id=200", "GET", false, false},
		{"other host", "frame-1", "Image", "https://example.com/asset.png", "GET", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allowed, comment := allowDouyinCommentCaptureRequest(req, tc.frame, tc.kind, tc.url, tc.method)
			if allowed != tc.allowed || comment != tc.comment {
				t.Fatalf("allow = %v, comment = %v", allowed, comment)
			}
		})
	}
}

func TestCaptureDouyinCommentRelationPageBindsMainComment(t *testing.T) {
	f := newCommentCaptureFixture(`{"status_code":0,"comments":[{"aweme_id":"200","cid":"400","reply_id":"0","reply_to_reply_id":"0","user":{"uid":"901"},"text":"主评论"}],"cursor":3,"has_more":1}`)
	commentCaptureEvents(f, "https://www.douyin.com/aweme/v1/web/comment/list/?aweme_id=200&cursor=0&count=20", "frame-1", "session-1")
	req := douyinCommentCaptureRequest{SessionID: "session-1", FrameID: "frame-1", WorkID: "200", TargetID: "400"}
	page, err := captureDouyinCommentRelationPage(context.Background(), f, req, func() error { return nil })
	if err != nil || f.bodyCalls != 1 || len(page.Relations) != 1 || page.Relations[0].CID != "400" || page.Cursor != "3" || !page.HasMore {
		t.Fatalf("page = %+v, body calls = %d, err = %v", page, f.bodyCalls, err)
	}
}

func TestCaptureDouyinCommentRelationPageRequiresApprovedRequest(t *testing.T) {
	f := newCommentCaptureFixture(`{"status_code":0,"comments":[{"cid":"400","reply_id":"0","reply_to_reply_id":"0","user":{"uid":"901"},"text":"主评论"}]}`)
	url := "https://www.douyin.com/aweme/v1/web/comment/list/?aweme_id=200"
	f.events <- commentCaptureEvent("Network.requestWillBeSent", "session-1", map[string]any{
		"requestId": "r1", "frameId": "frame-1", "loaderId": "loader-1", "request": map[string]any{"url": url, "method": "GET"},
	})
	f.events <- commentCaptureEvent("Network.responseReceived", "session-1", map[string]any{
		"requestId": "r1", "type": "Fetch", "response": map[string]any{"url": url, "status": 200, "mimeType": "application/json"},
	})
	f.events <- commentCaptureEvent("Network.loadingFinished", "session-1", map[string]any{
		"requestId": "r1", "encodedDataLength": 300,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := captureDouyinCommentRelationPage(ctx, f, douyinCommentCaptureRequest{
		SessionID: "session-1", FrameID: "frame-1", WorkID: "200", TargetID: "400",
	}, func() error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) || f.bodyCalls != 0 {
		t.Fatalf("unapproved response was read: body calls = %d, err = %v", f.bodyCalls, err)
	}
}

func TestCaptureDouyinCommentRelationPageBindsReplyRootAndDirectParent(t *testing.T) {
	f := newCommentCaptureFixture(`{"status_code":0,"comments":[{"aweme_id":"200","cid":"600","reply_id":"400","reply_to_reply_id":"500","user":{"uid":"901"},"text":"回复自己的子评论"}],"has_more":false}`)
	commentCaptureEvents(f, "https://www.douyin.com/aweme/v1/web/comment/list/reply/?item_id=200&comment_id=400&cursor=0", "frame-1", "session-1")
	req := douyinCommentCaptureRequest{SessionID: "session-1", FrameID: "frame-1", WorkID: "200", RootID: "400", TargetID: "600"}
	page, err := captureDouyinCommentRelationPage(context.Background(), f, req, func() error { return nil })
	if err != nil || len(page.Relations) != 1 || page.Relations[0].ReplyToReplyID != "500" || page.Relations[0].ReplyID != "400" {
		t.Fatalf("reply page = %+v, err = %v", page, err)
	}
}

func TestCaptureDouyinCommentRelationPageRejectsUnboundResponses(t *testing.T) {
	for name, url := range map[string]string{
		"wrong work":     "https://www.douyin.com/aweme/v1/web/comment/list/?aweme_id=201",
		"wrong path":     "https://www.douyin.com/aweme/v1/web/comment/list/reply/?item_id=200&comment_id=400",
		"duplicate work": "https://www.douyin.com/aweme/v1/web/comment/list/?aweme_id=200&aweme_id=201",
		"other host":     "https://example.com/aweme/v1/web/comment/list/?aweme_id=200",
	} {
		t.Run(name, func(t *testing.T) {
			f := newCommentCaptureFixture(`{"status_code":0,"comments":[]}`)
			commentCaptureEvents(f, url, "frame-1", "session-1")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			_, err := captureDouyinCommentRelationPage(ctx, f, douyinCommentCaptureRequest{SessionID: "session-1", FrameID: "frame-1", WorkID: "200", TargetID: "400"}, func() error { return nil })
			if !errors.Is(err, context.DeadlineExceeded) || f.bodyCalls != 0 {
				t.Fatalf("err = %v, body calls = %d", err, f.bodyCalls)
			}
		})
	}
}

func TestCaptureDouyinCommentRelationPageChecksGrantAndCurrentPage(t *testing.T) {
	for name, denied := range map[string]bool{"revoked grant": true, "moved page": false} {
		t.Run(name, func(t *testing.T) {
			f := newCommentCaptureFixture(`{"status_code":0,"comments":[{"cid":"400","reply_id":"0","reply_to_reply_id":"0","user":{"uid":"901"},"text":"主评论"}]}`)
			commentCaptureEvents(f, "https://www.douyin.com/aweme/v1/web/comment/list/?aweme_id=200", "frame-1", "session-1")
			grantCalls := 0
			if !denied {
				f.pageURL = "https://www.douyin.com/video/201"
			}
			_, err := captureDouyinCommentRelationPage(context.Background(), f, douyinCommentCaptureRequest{SessionID: "session-1", FrameID: "frame-1", WorkID: "200", TargetID: "400"}, func() error {
				grantCalls++
				if denied && grantCalls == 2 {
					return errors.New("授权撤销")
				}
				return nil
			})
			if err == nil || f.bodyCalls != 0 {
				t.Fatalf("err = %v, body calls = %d", err, f.bodyCalls)
			}
		})
	}
}
