package stationbrowser

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

func isDouyinCreatorImageHost(host string) bool {
	return strings.HasSuffix(strings.ToLower(host), ".douyinpic.com")
}

func validDouyinCreatorImageURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && len(raw) <= 8192 && u.Scheme == "https" && u.User == nil &&
		u.Port() == "" && u.Fragment == "" && u.Opaque == "" && u.Path != "" &&
		playerProxyHostSyntax(u.Host) && isDouyinCreatorImageHost(u.Host)
}

// CreatorImageKey ignores ephemeral signatures while binding image identity to
// the CDN host and object path. Fetches must still use freshly observed URLs.
func CreatorImageKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !validDouyinCreatorImageURL(raw) {
		return ""
	}
	return strings.ToLower(u.Host) + u.EscapedPath()
}

func ReadDouyinCreatorImages(ctx context.Context, urls []string, authorize func() error) ([]DouyinWebMedia, error) {
	if len(urls) < 1 || len(urls) > 6 || authorize == nil {
		return nil, ErrCreatorWorksUnavailable
	}
	if err := authorize(); err != nil {
		return nil, err
	}
	client, closeClient, err := newDouyinCDNClient(ctx, isDouyinCreatorImageHost)
	if err != nil {
		return nil, err
	}
	defer closeClient()
	images := make([]DouyinWebMedia, 0, len(urls))
	for _, raw := range urls {
		image, err := fetchDouyinCreatorImage(ctx, raw, client, authorize, validDouyinCreatorImageURL)
		if err != nil {
			return nil, err
		}
		images = append(images, image)
	}
	return images, nil
}

func fetchDouyinCreatorImage(ctx context.Context, raw string, client *http.Client, authorize func() error, allowURL func(string) bool) (DouyinWebMedia, error) {
	return fetchDouyinCDNMedia(ctx, raw, client, authorize, allowURL, douyinDownloadFormat{
		accept: "image/jpeg, image/png, image/webp", maxBytes: 2 << 20,
		validType: func(typ string) bool { return typ == "image/jpeg" || typ == "image/png" || typ == "image/webp" },
		detect:    http.DetectContentType,
	})
}
