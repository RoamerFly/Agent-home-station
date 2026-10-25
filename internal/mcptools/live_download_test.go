package mcptools

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLivePublicMediaRangeDownload(t *testing.T) {
	u := os.Getenv("WECHAT_LIVE_PUBLIC_AUDIO")
	if u == "" {
		t.Skip("explicit read-only download diagnostic")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	session := &publicReadSession{}
	ctx = context.WithValue(ctx, publicSessionKey{}, session)
	defer func() {
		if session.Transport != nil {
			session.Transport.CloseIdleConnections()
		}
	}()
	reader := func(ctx context.Context, url string, limit int64, timeout time.Duration, r, v string) ([]byte, string, string, rangeHeaders, error) {
		start := time.Now()
		b, typ, final, h, err := publicReadRange(ctx, url, limit, timeout, r, v)
		t.Logf("range=%s bytes=%d content_range=%s elapsed=%s error=%v", r, len(b), h.ContentRange, time.Since(start), err)
		return b, typ, final, h, err
	}
	b, _, err := downloadWithReader(ctx, u, 25<<20, reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("complete bytes=%d", len(b))
}
