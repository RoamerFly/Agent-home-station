package mcptools

import (
	"context"
	"errors"
	"time"
)

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
