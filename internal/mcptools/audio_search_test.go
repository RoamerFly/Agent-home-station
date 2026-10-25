package mcptools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCatalogCandidatesComeFromActualFilenamesAndStayLabeled(t *testing.T) {
	body := []byte(`[{"title":"Happy Tune","filename":"Happy Tune.mp3","genre":"15","length":"00:01:20","description":"bright","instruments":"Guitar, Drums"},{"title":"Unsafe","filename":"../secret.mp3","genre":"15"}]`)
	r, err := selectCatalogAudio(body, map[string]string{"15": "pop"}, "pop music")
	if err != nil || !json.Valid([]byte(r)) || !strings.Contains(r, "Happy%20Tune.mp3") || strings.Contains(r, "../secret") {
		t.Fatal(r, err)
	}
	if !strings.Contains(r, "attribution_required") || !strings.Contains(r, "器乐") {
		t.Fatal("lost recording type or attribution")
	}
}

func TestNamedSongDoesNotReturnTracksMatchingOnlyLightInDescription(t *testing.T) {
	body := []byte(`[{"title":"Unrelated music","filename":"Unrelated.mp3","description":"Turn on the light when traveling"},{"title":"Happy Alley","filename":"Happy Alley.mp3","description":"cheerful upbeat"}]`)
	result, err := selectCatalogAudio(body, nil, "Traveling Light")
	if err != nil || result != "" {
		t.Fatal("wrong track substituted", result, err)
	}
	result, err = selectCatalogAudio(body, nil, "Happy Alley")
	if err != nil || !strings.Contains(result, "Happy%20Alley.mp3") {
		t.Fatal("exact title lost", result, err)
	}
}

func TestAudioCatalogCacheIsExplicitAndDoesNotPretendToRefetch(t *testing.T) {
	musicCatalogCache.Lock()
	body, genres, at := musicCatalogCache.Body, musicCatalogCache.Genres, musicCatalogCache.At
	musicCatalogCache.Body = []byte(`[{"title":"Happy Tune","filename":"Happy Tune.mp3"}]`)
	musicCatalogCache.Genres = map[string]string{}
	musicCatalogCache.At = time.Now()
	musicCatalogCache.Unlock()
	defer func() {
		musicCatalogCache.Lock()
		musicCatalogCache.Body = body
		musicCatalogCache.Genres = genres
		musicCatalogCache.At = at
		musicCatalogCache.Unlock()
	}()
	r, err := incompetechAudio(context.Background(), "Happy Tune")
	var meta struct {
		Hit bool   `json:"catalog_cache_hit"`
		At  string `json:"catalog_fetched_at"`
	}
	_ = json.Unmarshal([]byte(r), &meta)
	if err != nil || !meta.Hit || meta.At == "" {
		t.Fatal("cached catalog disguised as a fresh request", r, err)
	}
}
