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
}

func (f *commentCaptureFixture) Call(_ context.Context, _, method string, _ any, result any) error {
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
	}
	return nil
}

func TestReadDouyinCommentRelationTargetSessionReturnsOnlyNoticeTarget(t *testing.T) {
	f := newCommentCaptureFixture(`{"status_code":0,"comments":[{"aweme_id":"200","cid":"400","reply_id":"0","reply_to_reply_id":"0","user":{"uid":"901"},"text":"目标"},{"aweme_id":"200","cid":"401","reply_id":"0","reply_to_reply_id":"0","user":{"uid":"902"},"text":"其他"}],"has_more":false}`)
	f.requestURL = "https://www.douyin.com/aweme/v1/web/comment/list/?aweme_id=200&cursor=0"
	got, err := readDouyinCommentRelationTargetSession(context.Background(), f, "https://www.douyin.com/video/200",
		DouyinCommentRelationRequest{WorkID: "200", TargetID: "400"}, func() error { return nil })
	if err != nil || got.CID != "400" || got.Text != "目标" || f.bodyCalls != 1 {
		t.Fatalf("relation = %+v, body calls = %d, err = %v", got, f.bodyCalls, err)
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
	f.events <- commentCaptureEvent("Network.responseReceived", sessionID, map[string]any{
		"requestId": "r1", "type": "Fetch", "response": map[string]any{"url": requestURL, "status": 200, "mimeType": "application/json"},
	})
	f.events <- commentCaptureEvent("Network.loadingFinished", sessionID, map[string]any{"requestId": "r1", "encodedDataLength": 300})
}

func newCommentCaptureFixture(body string) *commentCaptureFixture {
	return &commentCaptureFixture{events: make(chan cdpEvent, 8), pageURL: "https://www.douyin.com/video/200", body: body}
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
