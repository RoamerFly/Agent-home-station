package plugins

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// FetchResultMedia fetches a verified provider result URL using only the
// owner-declared service origins. It never forwards API credentials, cookies
// or browser state, and never follows a redirect to another origin.
func (b *ExternalBridge) FetchResultMedia(ctx context.Context, name, rawURL string) (MediaResult, error) {
	var empty MediaResult
	tool, ok := b.runtime.tools[name]
	if !ok || tool.MediaKind == "" || len(rawURL) > 2048 || validEndpoint(rawURL, false) != nil {
		return empty, errors.New("external_media_url_unavailable")
	}
	target, err := url.Parse(rawURL)
	if err != nil {
		return empty, errors.New("external_media_url_unavailable")
	}
	origin := target.Scheme + "://" + strings.ToLower(target.Host)
	allowed := false
	for _, declared := range append([]string{tool.URL, pollURL(tool)}, tool.MediaOrigins...) {
		u, err := url.Parse(declared)
		if err == nil && origin == u.Scheme+"://"+strings.ToLower(u.Host) {
			allowed = true
			break
		}
	}
	if !allowed {
		return empty, errors.New("external_media_origin_unapproved")
	}
	bound, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(bound, http.MethodGet, rawURL, nil)
	if err != nil {
		return empty, errors.New("external_media_url_unavailable")
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("external_media_redirect_refused")
	}}
	response, err := client.Do(request)
	if err != nil {
		return empty, errors.New("external_media_fetch_unconfirmed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return empty, errors.New("external_media_fetch_unconfirmed")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBinary+1))
	if err != nil || len(body) == 0 || len(body) > maxBinary {
		return empty, errors.New("external_media_size_invalid")
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType == "" {
		contentType, _, _ = mime.ParseMediaType(http.DetectContentType(body))
	}
	if contentType == "text/html" || strings.Contains(contentType, "json") || !externalMediaTypeAllowed(tool.MediaKind, contentType) {
		return empty, errors.New("external_media_type_invalid")
	}
	return MediaResult{Kind: tool.MediaKind, Data: body, MIMEType: contentType}, nil
}

func pollURL(tool APITool) string {
	if tool.Poll != nil {
		return tool.Poll.URL
	}
	return ""
}

func externalMediaTypeAllowed(kind, contentType string) bool {
	if contentType == "application/octet-stream" {
		return true
	}
	switch kind {
	case "image":
		return strings.HasPrefix(contentType, "image/")
	case "video":
		return strings.HasPrefix(contentType, "video/")
	case "voice":
		return strings.HasPrefix(contentType, "audio/")
	case "file":
		return contentType != "" && contentType != "text/html"
	}
	return false
}
