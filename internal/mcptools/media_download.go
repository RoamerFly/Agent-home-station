package mcptools

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type publicSessionKey struct{}
type publicReadSession struct{ Transport *http.Transport }

type rangeHeaders struct {
	ContentRange, Validator string
	Status                  int
}
type rangeReader func(context.Context, string, int64, time.Duration, string, string) ([]byte, string, string, rangeHeaders, error)

func parseRange(s string) (int64, int64, int64, bool) {
	if !strings.HasPrefix(s, "bytes ") {
		return 0, 0, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(s, "bytes "), "/")
	if len(parts) != 2 {
		return 0, 0, 0, false
	}
	bounds := strings.Split(parts[0], "-")
	if len(bounds) != 2 {
		return 0, 0, 0, false
	}
	a, e1 := strconv.ParseInt(bounds[0], 10, 64)
	b, e2 := strconv.ParseInt(bounds[1], 10, 64)
	n, e3 := strconv.ParseInt(parts[1], 10, 64)
	return a, b, n, e1 == nil && e2 == nil && e3 == nil && a >= 0 && b >= a && n > b
}
func downloadPublicMedia(ctx context.Context, raw string, limit int64) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 240*time.Second)
	defer cancel()
	session := &publicReadSession{}
	ctx = context.WithValue(ctx, publicSessionKey{}, session)
	defer func() {
		if session.Transport != nil {
			session.Transport.CloseIdleConnections()
		}
	}()
	return downloadWithReader(ctx, raw, limit, publicReadRange)
}
func downloadWithReader(ctx context.Context, raw string, limit int64, read rangeReader) ([]byte, string, error) {
	const chunk int64 = 64 << 10
	b, typ, final, h, err := read(ctx, raw, limit, 30*time.Second, fmt.Sprintf("bytes=0-%d", min(chunk, limit)-1), "")
	if err != nil {
		return nil, "", err
	}
	if h.Status == 200 {
		return b, typ, nil
	}
	start, end, total, ok := parseRange(h.ContentRange)
	if ok && total > limit {
		return nil, "", &WebError{Code: "response_too_large"}
	}
	if !ok || start != 0 || end+1 != int64(len(b)) || total > limit {
		return nil, "", &WebError{Code: "media_download_incomplete"}
	}
	if int64(len(b)) == total {
		return b, typ, nil
	}
	full := make([]byte, total)
	copy(full, b)
	offset := int64(len(b))
	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if firstErr != nil || offset >= total {
					mu.Unlock()
					return
				}
				at := offset
				last := min(at+chunk, total) - 1
				offset = last + 1
				mu.Unlock()
				part, t, _, next, err := read(workCtx, final, last-at+1, 30*time.Second, fmt.Sprintf("bytes=%d-%d", at, last), h.Validator)
				if err == nil {
					a, z, n, ok := parseRange(next.ContentRange)
					if !ok || a != at || z != last || n != total || len(part) != int(last-at+1) || next.Status != 206 || h.Validator != "" && next.Validator != "" && h.Validator != next.Validator || t != typ {
						err = &WebError{Code: "media_download_incomplete"}
					}
				}
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					cancelWork()
					return
				}
				copy(full[at:last+1], part)
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, "", firstErr
	}
	return full, typ, nil
}
