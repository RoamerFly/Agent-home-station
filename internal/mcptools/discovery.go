package mcptools

import (
	"crypto/rand"
	"math/big"
	"net/url"
)

// Discovery sources return bounded public metadata, never claimed video observations.
type DiscoverySource struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	URL     string `json:"url"`
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`
}
type DiscoveryItem struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Summary    string `json:"summary"`
	URL        string `json:"url"`
	MediaURL   string `json:"media_url,omitempty"`
	Kind       string `json:"kind"`
	Source     string `json:"source"`
	Published  string `json:"published,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

func DefaultDiscoverySources() []DiscoverySource {
	return []DiscoverySource{
		{"bili-popular", "B站热门", "bilibili", "https://api.bilibili.com/x/web-interface/popular?ps=30&pn=1", "video", true},
		{"bili-music", "B站音乐榜", "bilibili", "https://api.bilibili.com/x/web-interface/ranking/v2?rid=3&type=all", "voice", true},
		{"bing-images", "必应每日图片", "bing_images", "https://www.bing.com/HPImageArchive.aspx?format=js&idx=0&n=8&mkt=zh-CN", "image", true},
		{"commons", "维基共享每日图片", "rss", "https://commons.wikimedia.org/w/api.php?action=featuredfeed&feed=potd&feedformat=atom", "image", false},
		{"sspai", "少数派生活与新玩意", "rss", "https://sspai.com/feed", "text", true},
		{"music", "公开器乐目录", "audio_catalog", "https://incompetech.com/music/royalty-free/pieces.json", "voice", true},
	}
}
func ValidateDiscoverySource(s DiscoverySource) error {
	if s.ID == "" || len(s.ID) > 64 || s.Name == "" || len(s.Name) > 100 || len(s.URL) > 2048 || validPublicURL(s.URL) != nil {
		return &WebError{Code: "discovery_source_invalid"}
	}
	switch s.Kind {
	case "text", "voice", "video", "image":
	default:
		return &WebError{Code: "discovery_source_invalid"}
	}
	u, _ := url.Parse(s.URL)
	switch s.Type {
	case "rss":
	case "bilibili":
		if u.Hostname() != "api.bilibili.com" || (u.Path != "/x/web-interface/popular" && u.Path != "/x/web-interface/ranking/v2") {
			return &WebError{Code: "discovery_source_invalid"}
		}
	case "audio_catalog":
		if s.URL != "https://incompetech.com/music/royalty-free/pieces.json" {
			return &WebError{Code: "discovery_source_invalid"}
		}
	case "bing_images":
		if u.Hostname() != "www.bing.com" || u.Path != "/HPImageArchive.aspx" {
			return &WebError{Code: "discovery_source_invalid"}
		}
	default:
		return &WebError{Code: "discovery_source_invalid"}
	}
	return nil
}
func ShuffleDiscovery(items []DiscoveryItem) {
	for i := len(items) - 1; i > 0; i-- {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return
		}
		items[i], items[n.Int64()] = items[n.Int64()], items[i]
	}
}
