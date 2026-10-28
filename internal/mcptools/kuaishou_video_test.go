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

func TestKuaishouNewCDNAndExactPhotoBackupURLs(t *testing.T) {
	id := "3xsynthetic12345"
	primary := "https://synthetic.djvod.ndcimgs.com/video.mp4?synthetic=1"
	backup := "https://synthetic.kwaicdn.com/video.mp4?synthetic=2"
	makePage := func(direct string, backups []string) []byte {
		data, _ := json.Marshal(map[string]any{"defaultClient": map[string]any{
			"VisionVideoDetailPhoto:" + id: map[string]any{"id": id, "caption": "synthetic", "duration": 155100, "photoUrl": direct,
				"manifest": map[string]any{"adaptationSet": []any{map[string]any{"representation": []any{
					map[string]any{"url": direct, "backupUrl": map[string]any{"type": "json", "json": backups}},
				}}}}},
			"VisionVideoDetailPhoto:unrelated": map[string]any{"id": "unrelated", "photoUrl": "https://other.kwaicdn.com/not-this-video.mp4"},
		}})
		return append([]byte("window.__APOLLO_STATE__="), data...)
	}
	_, urls, err := parseKuaishouPhoto(makePage(primary, []string{backup, primary}), id)
	if err != nil || len(urls) != 2 || urls[0] != primary || urls[1] != backup {
		t.Fatal("valid primary/backup missing or duplicated", urls, err)
	}
	for _, unsafe := range []string{"https://synthetic.djvod.ndcimgs.com.evil.invalid/video.mp4", "https://unrelated.ndcimgs.com/video.mp4", "http://synthetic.djvod.ndcimgs.com/video.mp4", "https://user@synthetic.djvod.ndcimgs.com/video.mp4", "https://synthetic.djvod.ndcimgs.com:444/video.mp4", "https://127.0.0.1/video.mp4"} {
		_, got, e := parseKuaishouPhoto(makePage(unsafe, []string{unsafe}), id)
		if e == nil || len(got) != 0 {
			t.Fatal("unsafe primary/backup accepted", unsafe, got, e)
		}
	}
	_, urls, err = parseKuaishouPhoto(makePage("https://unavailable.invalid/no.mp4", []string{backup}), id)
	if err != nil || len(urls) != 1 || urls[0] != backup {
		t.Fatal("valid backup lost when primary is unavailable", urls, err)
	}
}
