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

func TestPublicMediaBudgetIsCapacityNotRequiredFileSize(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := GetPublicMedia(ctx, "https://synthetic.invalid/file.mp4", MaxPublicMediaBytes)
	if err == nil || err.Error() == "media_limit_invalid" {
		t.Fatal("valid file budget rejected before cancellation", err)
	}
	for _, budget := range []int64{0, -1, MaxPublicMediaBytes + 1} {
		_, _, err = GetPublicMedia(ctx, "https://synthetic.invalid/file.mp4", budget)
		if err == nil || err.Error() != "media_limit_invalid" {
			t.Fatal("invalid media budget was not distinguished", err)
		}
	}
	data, _, err := downloadWithReader(context.Background(), "https://synthetic.invalid/file.mp4", MaxPublicMediaBytes,
		func(_ context.Context, u string, budget int64, _ time.Duration, _, _ string) ([]byte, string, string, rangeHeaders, error) {
			if budget != MaxPublicMediaBytes {
				t.Fatal("explicit budget clamped", budget)
			}
			return []byte("synthetic small file"), "video/mp4", u, rangeHeaders{Status: 200}, nil
		})
	if err != nil || string(data) != "synthetic small file" {
		t.Fatal("small file requires no full-budget allocation", err)
	}
}
