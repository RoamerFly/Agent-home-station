package mcptools

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestMediaRangesAssembleExactlyAndRejectChangedFile(t *testing.T) {
	for _, changed := range []bool{false, true} {
		calls := 0
		reader := func(_ context.Context, u string, limit int64, timeout time.Duration, r, v string) ([]byte, string, string, rangeHeaders, error) {
			calls++
			if calls == 1 {
				return []byte{1, 2}, "audio/mpeg", u, rangeHeaders{ContentRange: "bytes 0-1/4", Validator: "etag", Status: 206}, nil
			}
			if v != "etag" || r != "bytes=2-3" {
				t.Fatal("missing interval/validator", v, r)
			}
			etag := "etag"
			if changed {
				etag = "different"
			}
			return []byte{3, 4}, "audio/mpeg", u, rangeHeaders{ContentRange: "bytes 2-3/4", Validator: etag, Status: 206}, nil
		}
		b, _, err := downloadWithReader(context.Background(), "https://public.example/audio", 100, reader)
		if changed && err == nil {
			t.Fatal("changed resource accepted")
		}
		if !changed && (err != nil || !bytes.Equal(b, []byte{1, 2, 3, 4})) {
			t.Fatal(b, err)
		}
	}
}
