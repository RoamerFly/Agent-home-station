// Package sharespec defines credential-free Channels share identifiers and
// bounded response parsing. It deliberately has no HTTP client or resolver.
package sharespec

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
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

// ObjectBridgeURL is deliberately loopback-only. It only validates a configured
// bridge URL; transport is owned by the installed share-reader host route.
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

// ParseObjectBridgeResponse verifies an already fetched bounded loopback
// response. It exposes no URL or transport authority to its caller.
func ParseObjectBridgeResponse(b []byte, id string) (ResolvedVideo, error) {
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
