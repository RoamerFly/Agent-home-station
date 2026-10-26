package mcptools

import (
	"context"
	"html"
	"net/url"
	"regexp"
	"strings"
)

type PublicShare struct {
	Kind, PageURL, Title, Text, ContentType string
	Data                                    []byte
}

var shareMetaTag = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
var shareMetaAttr = regexp.MustCompile(`(?is)([a-zA-Z_:][\w:.-]*)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
var shareTitle = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title>`)

// A general public-page fallback. A title, an app-launch shell or an embedded
// player URL is never accepted as evidence of watching a video.
func ReadPublicShare(ctx context.Context, raw string, limit int64, videoRequired bool) (PublicShare, error) {
	var out PublicShare
	if len(raw) > 8192 || validPublicURL(raw) != nil || limit < 1 || limit > 64<<20 {
		return out, &WebError{Code: "invalid_url"}
	}
	data, typ, final, err := publicRead(ctx, raw, limit)
	if err != nil {
		return out, err
	}
	out.PageURL = cleanShareURL(final)
	typ = strings.ToLower(strings.Split(typ, ";")[0])
	out.ContentType = typ
	if strings.HasPrefix(typ, "video/") {
		if _, err = ValidateMediaType("video", typ, data); err != nil {
			return PublicShare{}, err
		}
		out.Kind, out.Data = "video", data
		return out, nil
	}
	if strings.HasPrefix(typ, "image/") && !videoRequired {
		if _, err = ValidateMediaType("image", typ, data); err != nil {
			return PublicShare{}, err
		}
		out.Kind, out.Data = "image", data
		return out, nil
	}
	if len(data) > 2<<20 {
		return PublicShare{}, &WebError{Code: "response_too_large"}
	}
	if !strings.Contains(typ, "html") && !strings.HasPrefix(typ, "text/") {
		return out, &WebError{Code: "share_content_unsupported"}
	}
	title, body, media := parseSharedHTML(data, final)
	out.Title = title
	for _, candidate := range media[:min(2, len(media))] {
		b, t, e := GetPublicMedia(ctx, candidate, limit)
		if e == nil {
			t, e = ValidateMediaType("video", t, b)
		}
		if e == nil {
			out.Kind, out.Data, out.ContentType = "video", b, t
			return out, nil
		}
		if ctx.Err() != nil {
			return PublicShare{}, ctx.Err()
		}
	}
	if videoRequired || len(media) > 0 {
		return PublicShare{}, &WebError{Code: "share_requires_browser", Stage: "share_content"}
	}
	if len([]rune(body)) < 120 {
		return PublicShare{}, &WebError{Code: "share_requires_browser", Stage: "share_content"}
	}
	out.Kind, out.Text = "page", trimBytes(body, 14000)
	return out, nil
}

func parseSharedHTML(data []byte, base string) (string, string, []string) {
	s := string(data)
	title := ""
	if m := shareTitle.FindStringSubmatch(s); len(m) > 1 {
		title = trimBytes(cleanText(m[1]), 500)
	}
	urls := []string{}
	seen := map[string]bool{}
	for _, tag := range shareMetaTag.FindAllString(s, 100) {
		if len(tag) > 16384 {
			continue
		}
		attrs := map[string]string{}
		for _, m := range shareMetaAttr.FindAllStringSubmatch(tag, 10) {
			v := m[2]
			if v == "" {
				v = m[3]
			}
			attrs[strings.ToLower(m[1])] = html.UnescapeString(v)
		}
		prop := strings.ToLower(attrs["property"])
		if prop == "" {
			prop = strings.ToLower(attrs["name"])
		}
		if prop != "og:video" && prop != "og:video:url" && prop != "og:video:secure_url" {
			continue
		}
		u, e := url.Parse(attrs["content"])
		b, be := url.Parse(base)
		if e != nil || be != nil {
			continue
		}
		u = b.ResolveReference(u)
		raw := u.String()
		if len(raw) <= 8192 && validPublicURL(raw) == nil && !seen[raw] {
			seen[raw] = true
			urls = append(urls, raw)
		}
	}
	return title, extractHTMLText(s), urls
}
func cleanShareURL(raw string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return ""
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil
	return u.String()
}
