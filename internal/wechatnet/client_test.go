package wechatnet

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type trip func(*http.Request) (*http.Response, error)

func (f trip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHostAndNoAutomaticRetry(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "example.com", "evilweixin.qq.com", "short.weixin.qq.com:80", "user@short.weixin.qq.com", "short.weixin.qq.com/", "short..wechat.com", "SHORT.weixin.qq.com"} {
		if _, err := New(host); err == nil {
			t.Fatalf("accepted %q", host)
		}
	}
	c, err := New("short.weixin.qq.com")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	c.http.Transport = trip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/octet-stream" || !r.Close || r.URL.Path != "/mmtls/100" {
			t.Fatal("wrong carrier shape")
		}
		body, _ := io.ReadAll(r.Body)
		if !bytes.Equal(body, []byte{1, 2, 3}) {
			t.Fatal("body changed")
		}
		return nil, errors.New("synthetic connection drop")
	})
	if _, err := c.PostRecord(context.Background(), []byte{1, 2, 3}, time.Unix(100, 0)); err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
func TestCarrierRejectsOversizeAndRedirect(t *testing.T) {
	c, _ := New("short.weixin.qq.com")
	calls := 0
	c.http.Transport = trip(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{"Location": []string{"http://example.com"}}}, nil
	})
	_, err := c.PostRecord(context.Background(), []byte{1}, time.Now())
	var status *HTTPError
	if !errors.As(err, &status) || status.Status != 302 || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	c.http.Transport = trip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", MaxResponse+1))), Header: make(http.Header)}, nil
	})
	if _, err := c.PostRecord(context.Background(), []byte{1}, time.Now()); err == nil {
		t.Fatal("oversize response accepted")
	}
}
