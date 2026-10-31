package channels

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ObjectBridgeURL is deliberately loopback-only: this adapter talks to an
// administrator's wx_channel desktop bridge, never a random public parser.
func ObjectBridgeURL(raw string) (string, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") || u.Port() == "" {
		return "", errors.New("channels_object_bridge_invalid")
	}
	n, e := strconv.Atoi(u.Port())
	if e != nil || n < 1 || n > 65535 {
		return "", errors.New("channels_object_bridge_invalid")
	}
	return strings.TrimSuffix(u.String(), "/"), nil
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
	return parseObjectVideo(b, id)
}

func parseObjectVideo(b []byte, id string) (ResolvedVideo, error) {
	var out struct {
		Code int `json:"code"`
		Data struct {
			Object struct {
				ID      json.RawMessage `json:"id"`
				Contact struct {
					Nickname string `json:"nickname"`
				} `json:"contact"`
				Desc struct {
					Title string `json:"description"`
					Media []struct {
						URL   string          `json:"url"`
						Token string          `json:"urlToken"`
						Key   json.RawMessage `json:"decodeKey"`
					} `json:"media"`
				} `json:"objectDesc"`
			} `json:"object"`
		} `json:"data"`
	}
	var v ResolvedVideo
	if json.Unmarshal(b, &out) != nil || out.Code != 0 || len(out.Data.Object.Desc.Media) == 0 {
		return v, errors.New("channels_object_bridge_response_invalid")
	}
	actual := strings.Trim(string(out.Data.Object.ID), "\"")
	if actual != id {
		return v, errors.New("channels_resolved_video_mismatch")
	}
	m := out.Data.Object.Desc.Media[0]
	u, e := url.Parse(m.URL + m.Token)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return v, errors.New("channels_media_locator_missing")
	}
	v = ResolvedVideo{URL: u.String(), ObjectID: actual, Title: out.Data.Object.Desc.Title, Author: out.Data.Object.Contact.Nickname}
	if len(m.Key) > 0 && string(m.Key) != "null" {
		v.DecodeKey = strings.Trim(string(m.Key), "\"")
		if _, e = strconv.ParseUint(v.DecodeKey, 10, 64); e != nil {
			return ResolvedVideo{}, errors.New("channels_decode_key_invalid")
		}
	}
	return v, nil
}

// ParseObjectBridgeResponse verifies an already fetched bounded loopback bridge
// response. It exposes no transport or URL authority to callers.
func ParseObjectBridgeResponse(b []byte, id string) (ResolvedVideo, error) {
	return parseObjectVideo(b, id)
}
