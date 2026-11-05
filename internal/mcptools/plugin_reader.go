package mcptools

import (
	"context"
	"errors"
	"net"
	"net/url"
	"time"
)

type publicPluginMediaKey struct{}
type publicPluginMediaProbeKey struct{}
type publicPluginMediaOrigin struct{ origin, destination string }

// ReadPublicPluginShareSource resolves direct media without transporting the
// binary through MCP. Only its location/type is returned; download and content
// validation happen separately under the original task authorization.
func ReadPublicPluginShareSource(ctx context.Context, raw string, maxBytes int64, timeout time.Duration) ([]byte, string, string, error) {
	return ReadPublicPluginPage(context.WithValue(ctx, publicPluginMediaProbeKey{}, true), raw, maxBytes, timeout)
}

// GetPublicPluginMedia accepts only an already source-verified locator. It
// supplies the public source origin, never its query, cookie or credentials,
// and refuses cross-origin media redirects. All DNS/size checks still apply.
func GetPublicPluginMedia(ctx context.Context, raw, source string, maxBytes int64) ([]byte, string, error) {
	ctx, err := publicPluginMediaContext(ctx, raw, source)
	if err != nil {
		return nil, "", err
	}
	return GetPublicMedia(ctx, raw, maxBytes)
}

func publicPluginMediaContext(ctx context.Context, raw, source string) (context.Context, error) {
	if validPublicURL(raw) != nil || validPublicURL(source) != nil {
		return nil, errors.New("invalid plugin media source")
	}
	u, _ := url.Parse(source)
	d, _ := url.Parse(raw)
	for _, host := range []string{u.Hostname(), d.Hostname()} {
		if ip := net.ParseIP(host); ip != nil && privateIP(ip) {
			return nil, errors.New("invalid plugin media source")
		}
	}
	origin := publicPluginMediaOrigin{origin: u.Scheme + "://" + u.Host + "/", destination: d.Scheme + "://" + d.Host}
	return context.WithValue(ctx, publicPluginMediaKey{}, origin), nil
}

// ReadPublicPluginPage is the host's byte-level permission proxy. Extraction,
// search parsing and pagination belong to the plugin. Reuses DNS pinning,
// private-address rejection, TLS verification and redirect checks.
func ReadPublicPluginPage(ctx context.Context, url string, maxBytes int64, timeout time.Duration) ([]byte, string, string, error) {
	// A base64-wrapped response must fit the MCP 8 MiB frame without truncation.
	if maxBytes <= 0 || maxBytes > 4<<20 {
		return nil, "", "", errors.New("public page transport limit is 4 MiB; request smaller pages")
	}
	if timeout < 0 {
		return nil, "", "", errors.New("negative timeout")
	}
	if timeout == 0 {
		timeout = requestTimeout
	}
	return publicReadTimeout(ctx, url, maxBytes, timeout)
}
