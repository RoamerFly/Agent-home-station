package mcptools

import (
	"context"
	"testing"
)

func TestResolveDouyinCanonicalLink(t *testing.T) {
	page, id, err := ResolveDouyinLink(context.Background(), "https://www.douyin.com/video/7710665268560849875?previous_page=share")
	if err != nil || id != "7710665268560849875" || page != "https://www.douyin.com/video/7710665268560849875" {
		t.Fatal(page, id, err)
	}
	for _, raw := range []string{"http://v.douyin.com/short/", "https://evil.invalid/video/7710665268560849875", "https://www.douyin.com/user/name"} {
		if _, _, err := ResolveDouyinLink(context.Background(), raw); err == nil {
			t.Fatal("accepted", raw)
		}
	}
}
