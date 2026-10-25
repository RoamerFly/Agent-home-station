package mcptools

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"encoding/xml"
	"math/big"
	"net/url"
	"regexp"
	"strings"
	"time"
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
func DiscoverPublic(ctx context.Context, s DiscoverySource, related string) ([]DiscoveryItem, error) {
	if err := ValidateDiscoverySource(s); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 14*time.Second)
	defer cancel()
	if s.Type == "audio_catalog" {
		b, _, _, err := publicReadTimeout(ctx, s.URL, maxBody, 12*time.Second)
		if err != nil {
			return nil, err
		}
		var tracks []struct{ Title, Filename, Description, Length string }
		if json.Unmarshal(b, &tracks) != nil {
			return nil, &WebError{Code: "catalog_invalid"}
		}
		items := []DiscoveryItem{}
		for _, t := range tracks {
			if t.Title != "" && strings.HasSuffix(strings.ToLower(t.Filename), ".mp3") && !strings.ContainsAny(t.Filename, "/\\") {
				items = append(items, DiscoveryItem{Title: t.Title, Summary: "Kevin MacLeod 器乐；按来源标注署名许可。" + trimBytes(extractHTMLText(t.Description), 500), URL: "https://incompetech.com/music/royalty-free/", MediaURL: "https://incompetech.com/music/royalty-free/mp3-royaltyfree/" + url.PathEscape(t.Filename), Kind: "voice", Source: s.Name})
			}
		}
		ShuffleDiscovery(items)
		if len(items) > 12 {
			items = items[:12]
		}
		return items, nil
	}
	raw := s.URL
	if related != "" {
		if !IsPublicVideoPage(related) {
			return nil, &WebError{Code: "video_page_unsupported"}
		}
		u, _ := url.Parse(related)
		m := biliVideoPath.FindStringSubmatch(u.Path)
		if len(m) < 2 {
			return nil, &WebError{Code: "video_page_unsupported"}
		}
		raw = "https://api.bilibili.com/x/web-interface/archive/related?bvid=" + url.QueryEscape(m[1])
	}
	b, _, _, err := publicReadTimeout(ctx, raw, maxBody, 12*time.Second)
	if err != nil {
		return nil, err
	}
	if s.Type == "bilibili" {
		return parseDiscoveryBili(b, s)
	}
	if s.Type == "bing_images" {
		var data struct {
			Images []struct{ Title, Copyright, CopyrightLink, URL string } `json:"images"`
		}
		if json.Unmarshal(b, &data) != nil || len(data.Images) == 0 {
			return nil, &WebError{Code: "discovery_feed_unavailable"}
		}
		items := []DiscoveryItem{}
		for _, im := range data.Images {
			ref, e := url.Parse(im.URL)
			if e != nil {
				continue
			}
			base, _ := url.Parse("https://www.bing.com")
			media := base.ResolveReference(ref).String()
			page := im.CopyrightLink
			if validPublicURL(media) != nil || validPublicURL(page) != nil {
				continue
			}
			items = append(items, DiscoveryItem{Title: trimBytes(im.Title, 400), Summary: trimBytes(im.Copyright, 700) + "；来源图片，不代表自由版权。", URL: page, MediaURL: media, Kind: "image", Source: s.Name})
		}
		return items, nil
	}
	return parseDiscoveryFeed(b, s)
}
func parseDiscoveryBili(b []byte, s DiscoverySource) ([]DiscoveryItem, error) {
	var result struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(b, &result) != nil || result.Code != 0 || len(result.Data) == 0 {
		return nil, &WebError{Code: "discovery_feed_unavailable"}
	}
	type video struct {
		BVID     string `json:"bvid"`
		Title    string `json:"title"`
		Desc     string `json:"desc"`
		Pic      string `json:"pic"`
		Duration int64  `json:"duration"`
		PubDate  int64  `json:"pubdate"`
	}
	var rows []video
	if result.Data[0] == '[' {
		_ = json.Unmarshal(result.Data, &rows)
	} else {
		var data struct {
			List []video `json:"list"`
		}
		_ = json.Unmarshal(result.Data, &data)
		rows = data.List
	}
	items := []DiscoveryItem{}
	for _, v := range rows {
		if len(items) >= 40 {
			break
		}
		raw := "https://www.bilibili.com/video/" + v.BVID + "/"
		if !IsPublicVideoPage(raw) || v.Title == "" {
			continue
		}
		published := ""
		if v.PubDate > 0 {
			published = time.Unix(v.PubDate, 0).UTC().Format(time.RFC3339)
		}
		items = append(items, DiscoveryItem{Title: trimBytes(v.Title, 400), Summary: trimBytes(extractHTMLText(v.Desc), 700), URL: raw, Kind: s.Kind, Source: s.Name, DurationMS: v.Duration * 1000, Published: published})
	}
	if len(items) == 0 {
		return nil, &WebError{Code: "discovery_feed_unavailable"}
	}
	return items, nil
}

type feedLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

var feedImage = regexp.MustCompile(`(?is)<img\b[^>]*\bsrc\s*=\s*["']([^"']+)["']`)

func parseDiscoveryFeed(b []byte, s DiscoverySource) ([]DiscoveryItem, error) {
	// Separate RSS and Atom link representations to avoid overlapping XML paths.
	var doc struct {
		Channel struct {
			Items []struct {
				Title       string `xml:"title"`
				Link        string `xml:"link"`
				Description string `xml:"description"`
				Content     string `xml:"encoded"`
				Published   string `xml:"pubDate"`
				Enclosure   struct {
					URL  string `xml:"url,attr"`
					Type string `xml:"type,attr"`
				} `xml:"enclosure"`
			} `xml:"item"`
		} `xml:"channel"`
		Entries []struct {
			Title     string     `xml:"title"`
			Links     []feedLink `xml:"link"`
			Summary   string     `xml:"summary"`
			Content   string     `xml:"content"`
			Published string     `xml:"published"`
			Updated   string     `xml:"updated"`
		} `xml:"entry"`
	}
	if xml.Unmarshal(b, &doc) != nil {
		return nil, &WebError{Code: "discovery_feed_unavailable"}
	}
	items := []DiscoveryItem{}
	add := func(title, link, body, published, media, kind string) {
		if len(items) >= 40 || validPublicURL(link) != nil || title == "" {
			return
		}
		if strings.HasPrefix(media, "//") {
			media = "https:" + media
		}
		if media != "" && validPublicURL(media) != nil {
			media = ""
		}
		if s.Kind == "image" && media == "" {
			m := feedImage.FindStringSubmatch(body)
			if len(m) > 1 {
				u, _ := url.Parse(s.URL)
				ref, _ := url.Parse(m[1])
				if ref != nil {
					media = u.ResolveReference(ref).String()
				}
				if validPublicURL(media) != nil {
					media = ""
				}
			}
		}
		items = append(items, DiscoveryItem{Title: trimBytes(extractHTMLText(title), 400), Summary: trimBytes(extractHTMLText(body), 900), URL: link, MediaURL: media, Kind: kind, Source: s.Name, Published: published})
	}
	for _, e := range doc.Channel.Items {
		body := e.Description + " " + e.Content
		kind := s.Kind
		media := e.Enclosure.URL
		if strings.HasPrefix(e.Enclosure.Type, "audio/") {
			kind = "voice"
		} else if strings.HasPrefix(e.Enclosure.Type, "video/") {
			kind = "video"
		} else if strings.HasPrefix(e.Enclosure.Type, "image/") {
			kind = "image"
		}
		add(e.Title, strings.TrimSpace(e.Link), body, e.Published, media, kind)
	}
	for _, e := range doc.Entries {
		link, media := "", ""
		for _, l := range e.Links {
			if l.Rel == "alternate" || l.Rel == "" {
				link = l.Href
			}
			if l.Rel == "enclosure" {
				media = l.Href
			}
		}
		stamp := e.Published
		if stamp == "" {
			stamp = e.Updated
		}
		add(e.Title, link, e.Summary+" "+e.Content, stamp, media, s.Kind)
	}
	if len(items) == 0 {
		return nil, &WebError{Code: "discovery_feed_unavailable"}
	}
	return items, nil
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

// Bilibili popular/ranking shells are content lists, not article bodies.
func discoveryListPage(ctx context.Context, raw string) (string, bool, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() != "www.bilibili.com" && u.Hostname() != "bilibili.com" {
		return "", false, nil
	}
	if u.Path != "/v/popular/all" && u.Path != "/v/popular/rank/all" && u.Path != "/ranking" {
		return "", false, nil
	}
	s := DefaultDiscoverySources()[0]
	items, err := DiscoverPublic(ctx, s, "")
	if err != nil {
		return "", true, err
	}
	b, _ := json.Marshal(map[string]any{"source": s.URL, "metadata_only": true, "candidates": items, "instruction": "这是公开列表中的视频标题与简介，不是已观看的视频；选择具体BV页读取或下载。"})
	return string(b), true, nil
}
