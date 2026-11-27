package mcptools

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var douyinVideoPath = regexp.MustCompile(`^/video/([0-9]{10,25})/?$`)
var douyinShortPath = regexp.MustCompile(`^/[A-Za-z0-9_-]{4,40}/?$`)

// ResolveDouyinLink follows a public short link and returns the exact video
// page identity. It does not claim to have downloaded or watched its media.
func ResolveDouyinLink(ctx context.Context, raw string) (string, string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return "", "", &WebError{Code: "invalid_url"}
	}
	if u.Hostname() == "www.douyin.com" || u.Hostname() == "douyin.com" {
		if match := douyinVideoPath.FindStringSubmatch(u.Path); match != nil {
			return "https://www.douyin.com/video/" + match[1], match[1], nil
		}
	}
	if u.Hostname() != "v.douyin.com" || !douyinShortPath.MatchString(u.Path) {
		return "", "", &WebError{Code: "video_page_unsupported"}
	}
	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, _, final, err := publicRead(readCtx, raw, 1<<20)
	if err != nil {
		return "", "", err
	}
	resolved, err := url.Parse(final)
	if err != nil || resolved.Scheme != "https" || resolved.User != nil || resolved.Port() != "" || (resolved.Hostname() != "www.douyin.com" && resolved.Hostname() != "douyin.com") {
		return "", "", &WebError{Code: "video_page_unsupported"}
	}
	match := douyinVideoPath.FindStringSubmatch(resolved.Path)
	if match == nil {
		return "", "", &WebError{Code: "video_page_unsupported"}
	}
	return "https://www.douyin.com/video/" + match[1], match[1], nil
}
