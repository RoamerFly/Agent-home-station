package channels

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"wechat-bot/internal/sharespec"
)

// ObjectBridgeURL is deliberately loopback-only: this adapter talks to an
// administrator's wx_channel desktop bridge, never a random public parser.
func ObjectBridgeURL(raw string) (string, error) {
	return sharespec.ObjectBridgeURL(raw)
}

func ResolveObject(ctx context.Context, base, id, nonce string) (ResolvedVideo, error) {
	var v ResolvedVideo
	base, e := ObjectBridgeURL(base)
	if e != nil {
		return v, e
	}
	if _, e = strconv.ParseUint(id, 10, 64); e != nil {
		return v, errors.New("channels_object_id_invalid")
	}
	if len(nonce) > 4096 {
		return v, errors.New("channels_object_id_invalid")
	}
	q := url.Values{"object_id": {id}, "nonce_id": {nonce}}
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/channels/feed/profile?"+q.Encode(), nil)
	client := &http.Client{Timeout: 25 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Do(req)
	if e != nil {
		return v, errors.New("channels_object_bridge_unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return v, errors.New("channels_object_bridge_rejected")
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if e != nil || len(b) > 2<<20 {
		return v, errors.New("channels_object_bridge_response_invalid")
	}
	return sharespec.ParseObjectBridgeResponse(b, id)
}

// ParseObjectBridgeResponse verifies an already fetched bounded loopback bridge
// response. It exposes no transport or URL authority to callers.
func ParseObjectBridgeResponse(b []byte, id string) (ResolvedVideo, error) {
	return sharespec.ParseObjectBridgeResponse(b, id)
}

// parseObjectVideo remains for same-package legacy tests; production callers
// use sharespec directly and do not link this resolver package.
func parseObjectVideo(b []byte, id string) (ResolvedVideo, error) {
	return sharespec.ParseObjectBridgeResponse(b, id)
}
