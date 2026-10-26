package mcptools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestKuaishouUsesExactReceivedPhotoAndBoundedReferences(t *testing.T) {
	id := "3xsynthetic12345"
	state := map[string]any{"defaultClient": map[string]any{
		"VisionVideoDetailPhoto:" + id: map[string]any{"id": id, "caption": "synthetic video", "duration": 169000, "manifest": map[string]string{"type": "id", "id": "manifest"}},
		"manifest":                     map[string]any{"adaptationSet": []any{map[string]string{"type": "id", "id": "set"}}},
		"set":                          map[string]any{"representation": []any{map[string]string{"type": "id", "id": "rep"}}},
		"rep":                          map[string]any{"url": map[string]any{"json": []string{"https://cdn.kwaicdn.com/movie.mp4?public=1"}}},
		"VisionVideoDetailPhoto:other": map[string]any{"id": "other", "photoUrl": "https://evil.invalid/other.mp4"},
	}}
	b, _ := json.Marshal(state)
	body := append([]byte("<script>window.__APOLLO_STATE__="), b...)
	p, urls, err := parseKuaishouPhoto(body, id)
	if err != nil || p.ID != id || len(urls) != 1 || !strings.HasPrefix(urls[0], "https://cdn.kwaicdn.com/") {
		t.Fatal(p, urls, err)
	}
	if _, _, err = parseKuaishouPhoto(body, "different"); err == nil {
		t.Fatal("recommended or unrelated video accepted")
	}
	for _, unsafe := range []string{"https://cdn.kwaicdn.com.evil.invalid/movie.mp4", "https://user@cdn.kwaicdn.com/movie.mp4", "http://127.0.0.1/movie.mp4", "https://cdn.kwaicdn.com:444/movie.mp4"} {
		bad := strings.Replace(string(body), urls[0], unsafe, 1)
		if _, _, e := parseKuaishouPhoto([]byte(bad), id); e == nil {
			t.Fatal("unsafe media URL accepted", unsafe)
		}
	}
}
func TestKuaishouPageIdentityRejectsAmbiguousAndForeignShares(t *testing.T) {
	good := "https://v.m.chenzhongtech.com/fw/photo/3xsynthetic12345?shareToken=private-share&photoId=3xsynthetic12345"
	if kuaishouPhotoID(good) != "3xsynthetic12345" || !IsPublicVideoPage(good) {
		t.Fatal("received share not recognized")
	}
	for _, bad := range []string{strings.Replace(good, "chenzhongtech.com", "chenzhongtech.com.evil.invalid", 1), strings.Replace(good, "photoId=3xsynthetic12345", "photoId=different", 1), "https://www.kuaishou.com/profile/3xsynthetic12345", "https://user@www.kuaishou.com/short-video/3xsynthetic12345"} {
		if kuaishouPhotoID(bad) != "" {
			t.Fatal("unsafe or ambiguous page accepted", bad)
		}
	}
}
