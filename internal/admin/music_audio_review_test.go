package admin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Explicit opt-in: real local recording and configured audio understanding,
// isolated review cache. No generation, production memory writes or sends.
func TestLiveSongAudioReview(t *testing.T) {
	root, path := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_SONG_AUDIO")
	if root == "" || path == "" {
		t.Skip("opt-in actual audio listening check")
	}
	c, err := loadSpeech(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f := setup(t)
	f.manager.speech = c
	p, _, _ := f.manager.toolAccess()
	review := p.reviewSongAudio(context.Background(), data, "female")
	if review["state"] != "observed" {
		t.Fatal(review)
	}
	t.Logf("actual sampled audio model observation: %v; no generation or WeChat sends", review["observation"])
	second := p.reviewSongAudio(context.Background(), data, "female")
	if second["observation"] != review["observation"] {
		t.Fatal("audio-bound review was not reused")
	}
}
