package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ResolvedVideo contains private transfer material, never model context/logs.
type ResolvedVideo struct {
	URL, DecodeKey, Title, Author, ObjectID string
}

var sphPath = regexp.MustCompile(`^/sph/[A-Za-z0-9_-]{6,128}$`)

func ShareURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host != "weixin.qq.com" || u.User != nil || !sphPath.MatchString(u.Path) || u.RawPath != "" {
		return "", errors.New("channels_share_link_invalid")
	}
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

func ValidCookie(cookie string) bool {
	if len(cookie) < 16 || len(cookie) > 16384 || strings.ContainsAny(cookie, "\r\n\x00") {
		return false
	}
	r := &http.Request{Header: http.Header{"Cookie": []string{cookie}}}
	return len(r.Cookies()) > 0
}

type Resolver struct{ client *http.Client }

func NewResolver() *Resolver {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Resolver{client: &http.Client{Timeout: 25 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (r *Resolver) post(ctx context.Context, endpoint, referer, cookie string, payload any, out any) error {
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(b))
	if err != nil {
		return errors.New("channels_resolver_request_invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://"+req.URL.Host)
	req.Header.Set("Referer", referer)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/130.0.0.0 Safari/537.36")
	if cookie != "" && req.URL.Host == "yuanbao.tencent.com" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return errors.New("channels_resolver_network_unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return errors.New("channels_yuanbao_auth_expired")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("channels_resolver_upstream_rejected")
	}
	b, err = io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(b) > 2<<20 || json.Unmarshal(b, out) != nil {
		return errors.New("channels_resolver_response_invalid")
	}
	return nil
}

// Resolve only contacts two fixed Tencent HTTPS origins. The Yuanbao cookie
// is never forwarded to the preview API, redirects, or a media CDN.
func (r *Resolver) Resolve(ctx context.Context, raw, cookie string) (ResolvedVideo, error) {
	var result ResolvedVideo
	share, err := ShareURL(raw)
	if err != nil {
		return result, err
	}
	if !ValidCookie(cookie) {
		return result, errors.New("channels_yuanbao_cookie_missing")
	}
	var parsed struct {
		Code int `json:"code"`
		Data struct {
			PlayableURL string `json:"playable_url"`
			ExportID    string `json:"wx_export_id"`
		} `json:"data"`
	}
	err = r.post(ctx, "https://yuanbao.tencent.com/api/weixin/get_parse_result", "https://yuanbao.tencent.com/", cookie, map[string]any{"type": "video_channel_url", "url": share, "scene": 1}, &parsed)
	if err != nil {
		return result, err
	}
	if parsed.Code != 0 {
		return result, errors.New("channels_yuanbao_parse_failed")
	}
	play, err := url.Parse(parsed.Data.PlayableURL)
	if err != nil || play.Scheme != "https" || play.Host != "channels.weixin.qq.com" || play.User != nil || !strings.HasPrefix(play.Path, "/finder-preview/") {
		return result, errors.New("channels_playable_link_missing")
	}
	token, id := play.Query().Get("token"), play.Query().Get("eid")
	if id == "" {
		id = parsed.Data.ExportID
	}
	if token == "" || id == "" || len(token) > 8192 || len(id) > 4096 {
		return result, errors.New("channels_playable_link_missing")
	}
	var feed struct {
		Code int `json:"errCode"`
		Data struct {
			Author struct {
				Name string `json:"nickname"`
			} `json:"authorInfo"`
			Feed struct {
				Title string          `json:"description"`
				URL   string          `json:"videoUrl"`
				Key   json.RawMessage `json:"decodeKey"`
				ID    json.RawMessage `json:"objectId"`
				H264  struct {
					URL string `json:"videoUrl"`
				} `json:"h264VideoInfo"`
			} `json:"feedInfo"`
		} `json:"data"`
	}
	err = r.post(ctx, "https://channels.weixin.qq.com/finder-preview/api/feed/get_feed_info", play.String(), "", map[string]any{"baseReq": map[string]string{"generalToken": token}, "exportId": id}, &feed)
	if err != nil {
		return result, err
	}
	if feed.Code != 0 {
		return result, errors.New("channels_feed_lookup_failed")
	}
	result = ResolvedVideo{URL: feed.Data.Feed.H264.URL, Title: feed.Data.Feed.Title, Author: feed.Data.Author.Name}
	if result.URL == "" {
		result.URL = feed.Data.Feed.URL
	}
	u, err := url.Parse(result.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || len(result.URL) > 16384 {
		return ResolvedVideo{}, errors.New("channels_media_locator_missing")
	}
	for _, field := range []struct {
		raw json.RawMessage
		dst *string
	}{{feed.Data.Feed.Key, &result.DecodeKey}, {feed.Data.Feed.ID, &result.ObjectID}} {
		raw, dst := string(field.raw), field.dst
		if raw == "" || raw == "null" {
			continue
		}
		value := strings.Trim(raw, "\"")
		if _, e := strconv.ParseUint(value, 10, 64); e != nil {
			return ResolvedVideo{}, errors.New("channels_resolver_response_invalid")
		}
		*dst = value
	}
	return result, nil
}
