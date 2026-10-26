package channels

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type resolverTransport func(*http.Request) (*http.Response, error)

func (f resolverTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestResolverAcceptsPreview201AndNeverForwardsCookie(t *testing.T) {
	calls := 0
	r := NewResolver()
	r.client.Transport = resolverTransport(func(q *http.Request) (*http.Response, error) {
		calls++
		body := ""
		status := 200
		switch q.URL.Host {
		case "yuanbao.tencent.com":
			if q.Header.Get("Cookie") != "hy_user=synthetic; hy_token=synthetic-session" {
				t.Fatal("cookie omitted")
			}
			body = `{"code":0,"data":{"playable_url":"https://channels.weixin.qq.com/finder-preview/pages/feed?token=private-transfer-token&eid=export%2Fsynthetic"}}`
		case "channels.weixin.qq.com":
			if q.Header.Get("Cookie") != "" {
				t.Fatal("yuanbao cookie escaped its origin")
			}
			payload, _ := io.ReadAll(q.Body)
			if !strings.Contains(string(payload), `"generalToken":"private-transfer-token"`) {
				t.Fatal("playback token missing")
			}
			status = 201
			body = `{"errCode":0,"data":{"authorInfo":{"nickname":"作者甲"},"feedInfo":{"description":"合成测试视频标题","decodeKey":18446744073709551615,"objectId":"18446744073709551614","h264VideoInfo":{"videoUrl":"https://media.example.test/video.mp4"}}}}`
		default:
			t.Fatal("unexpected origin", q.URL.Host)
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: q}, nil
	})
	v, e := r.Resolve(context.Background(), "https://weixin.qq.com/sph/Synthetic1", "hy_user=synthetic; hy_token=synthetic-session")
	if e != nil || calls != 2 || v.DecodeKey != "18446744073709551615" || v.ObjectID != "18446744073709551614" {
		t.Fatal("parse or integer precision lost", e, v)
	}
}

func TestResolverRefusesUntrustedLinksAndRedactsErrors(t *testing.T) {
	for _, link := range []string{"http://weixin.qq.com/sph/Synthetic1", "https://weixin.qq.com.evil.test/sph/Synthetic1", "https://user@weixin.qq.com/sph/Synthetic1", "https://weixin.qq.com/sph/../private", "https://127.0.0.1/sph/Synthetic1"} {
		if _, e := ShareURL(link); e == nil {
			t.Fatal("untrusted link accepted", link)
		}
	}
	r := NewResolver()
	calls := 0
	r.client.Transport = resolverTransport(func(q *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`private upstream credential details`)), Request: q}, nil
	})
	_, e := r.Resolve(context.Background(), "https://weixin.qq.com/sph/Synthetic1", "hy_user=synthetic; hy_token=synthetic-session")
	if e == nil || e.Error() != "channels_yuanbao_auth_expired" || calls != 1 {
		t.Fatal("auth failure not safely surfaced", e)
	}
	if ValidCookie("hy_token=abc\r\nX-Secret: injected") {
		t.Fatal("header injection allowed")
	}
}

func TestResolverRejectsPlaybackRedirectOutsideTencent(t *testing.T) {
	r := NewResolver()
	calls := 0
	r.client.Transport = resolverTransport(func(q *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"playable_url":"https://evil.test/finder-preview/?token=secret&eid=export1"}}`)), Request: q}, nil
	})
	_, e := r.Resolve(context.Background(), "https://weixin.qq.com/sph/Synthetic1", "hy_user=synthetic; hy_token=synthetic-session")
	if e == nil || calls != 1 {
		t.Fatal("untrusted playback accepted")
	}
}

func TestObjectBridgeValidatesExactObjectAndDoesNotUsePublicEndpoint(t *testing.T) {
	for _, u := range []string{"https://example.test", "http://localhost:2026", "http://127.0.0.1:2026/path", "http://127.0.0.1:2026?token=private"} {
		if _, e := ObjectBridgeURL(u); e == nil {
			t.Fatal("untrusted bridge", u)
		}
	}
	b := []byte(`{"code":0,"data":{"object":{"id":18446744073709551614,"contact":{"nickname":"作者"},"objectDesc":{"description":"测试作品","media":[{"url":"https://media.example.test/video?x=1","urlToken":"&token=synthetic","decodeKey":"18446744073709551615"}]}}}}`)
	v, e := parseObjectVideo(b, "18446744073709551614")
	if e != nil || v.DecodeKey != "18446744073709551615" || !strings.Contains(v.URL, "&token=synthetic") {
		t.Fatal("valid object rejected or lost precision", v, e)
	}
	if _, e := parseObjectVideo(b, "1234"); e == nil {
		t.Fatal("wrong work accepted")
	}
}
