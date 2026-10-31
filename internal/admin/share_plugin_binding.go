package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"wechat-bot/internal/channels"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

// One resolver chain for one host-verified native message. Never construct from
// model tool arguments. This guard is independent of the plugin's own parsing.
type shareRouteBinding struct {
	mu                                                                          sync.Mutex
	account, thread, event, share, object, nonce, phase, token, export, referer string
	revision                                                                    int
	message                                                                     string
	resolved                                                                    *linuxnative.ChannelsSource
}

func newShareRouteBinding(message linuxnative.Message, source linuxnative.ChannelsSource, ref companion.SourceRef, verifiedShare string) (*shareRouteBinding, error) {
	if message.AccountID == "" || message.ThreadID == "" || message.ID == "" || source.AccountID != message.AccountID || source.ThreadID != message.ThreadID || source.MessageID != message.ID || ref.Account != message.AccountID || ref.Thread != message.ThreadID || ref.MessageID != message.ID || ref.EventID == "" || ref.Revision < 1 {
		return nil, errors.New("share_source_binding_invalid")
	}
	b := &shareRouteBinding{account: message.AccountID, thread: message.ThreadID, message: message.ID, event: ref.EventID, revision: ref.Revision, object: source.ObjectID, nonce: source.ObjectNonceID}
	if verifiedShare != "" {
		if verifiedShare == "bridge" {
			if _, e := strconv.ParseUint(b.object, 10, 64); e != nil || len(b.nonce) > 4096 {
				return nil, errors.New("share_native_shortlink_required")
			}
			b.phase = "channels-bridge"
			return b, nil
		}
		var err error
		b.share, err = channels.ShareURL(verifiedShare)
		if err != nil {
			return nil, errors.New("share_source_binding_invalid")
		}
		b.phase = "channels-parse"
	} else if strings.HasPrefix(b.object, "export/") && len(b.object) >= 12 && len(b.object) <= 4096 && len(b.nonce) > 0 && len(b.nonce) <= 4096 {
		b.phase = "channels-shortlink"
	} else {
		return nil, errors.New("share_native_shortlink_required")
	}
	return b, nil
}

// strictJSON rejects duplicates recursively: different upstream JSON parsers
// must never see different source IDs in the same approved request body.
func strictJSON(d *json.Decoder, depth int) (any, error) {
	if depth > 16 {
		return nil, errors.New("JSON nesting too deep")
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := t.(json.Delim); ok {
		switch delimiter {
		case '{':
			out := map[string]any{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return nil, e
				}
				name, ok := k.(string)
				if !ok {
					return nil, errors.New("invalid key")
				}
				if _, exists := out[name]; exists {
					return nil, errors.New("duplicate key")
				}
				v, e := strictJSON(d, depth+1)
				if e != nil {
					return nil, e
				}
				out[name] = v
			}
			_, err = d.Token()
			return out, err
		case '[':
			out := []any{}
			for d.More() {
				v, e := strictJSON(d, depth+1)
				if e != nil {
					return nil, e
				}
				out = append(out, v)
			}
			_, err = d.Token()
			return out, err
		default:
			return nil, errors.New("invalid delimiter")
		}
	}
	return t, nil
}
func decodeShareJSON(raw []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, e := strictJSON(d, 0)
	if e != nil {
		return nil, e
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	return v, nil
}
func sameShareBody(raw json.RawMessage, expected any) bool {
	actual, e := decodeShareJSON(raw)
	if e != nil {
		return false
	}
	b, _ := json.Marshal(expected)
	want, e := decodeShareJSON(b)
	return e == nil && reflect.DeepEqual(actual, want)
}

// check must re-read current authorization and source revision. send is the
// host route proxy/transport. Uncertain submissions poison this chain; it never
// silently replays a POST or falls back to another received share.
func (b *shareRouteBinding) Call(ctx context.Context, store *pluginhost.Store, j pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage, check func(context.Context) error, send pluginruntime.RouteTransport) (json.RawMessage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if check == nil || send == nil || j.Context.Account != b.account || j.Context.Thread != b.thread || j.Context.SourceVersions[b.event] != b.revision || j.Tool != "share.resolve_channels" || cap.ID != j.Tool {
		return nil, errors.New("share_source_binding_changed")
	}
	if err := check(ctx); err != nil {
		return nil, err
	}
	var request struct {
		Route   string          `json:"route"`
		Body    json.RawMessage `json:"body"`
		Referer string          `json:"referer"`
	}
	if json.Unmarshal(raw, &request) != nil || request.Route != b.phase {
		return nil, errors.New("share_route_out_of_order")
	}
	var expected any
	switch b.phase {
	case "channels-shortlink":
		expected = map[string]any{"exportId": b.object, "nonceId": b.nonce, "scene": 40}
	case "channels-parse":
		expected = map[string]any{"type": "video_channel_url", "url": b.share, "scene": 1}
	case "channels-feed":
		expected = map[string]any{"baseReq": map[string]string{"generalToken": b.token}, "exportId": b.export}
	default:
		return nil, errors.New("share_route_chain_finished")
	}
	if !sameShareBody(request.Body, expected) || request.Route == "channels-feed" && request.Referer != b.referer || request.Route != "channels-feed" && request.Referer != "" {
		return nil, errors.New("share_route_source_mismatch")
	}
	phase := b.phase
	b.phase = "failed"
	result, err := pluginruntime.ProxyHTTPRoute(ctx, store, j, cap, raw, func(string, json.RawMessage) error { return nil }, send)
	if err != nil {
		return nil, err
	}
	if err = check(ctx); err != nil {
		return nil, err
	}
	if _, err = decodeShareJSON(result); err != nil {
		return nil, errors.New("share_route_response_invalid")
	}
	if err = b.accept(phase, result); err != nil {
		return nil, err
	}
	return result, nil
}

func (b *shareRouteBinding) accept(phase string, raw json.RawMessage) error {
	fail := func() error { return errors.New("share_route_response_binding_failed") }
	switch phase {
	case "channels-shortlink":
		var r struct {
			Code *int `json:"errCode"`
			Data struct {
				URL string `json:"shortUrl"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &r) != nil || r.Code == nil || *r.Code != 0 {
			return fail()
		}
		share, e := channels.ShareURL(r.Data.URL)
		if e != nil {
			return fail()
		}
		b.share = share
		b.phase = "channels-parse"
	case "channels-parse":
		var r struct {
			Code *int `json:"code"`
			Data struct {
				URL    string `json:"playable_url"`
				Export string `json:"wx_export_id"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &r) != nil || r.Code == nil || *r.Code != 0 {
			return fail()
		}
		u, e := url.Parse(r.Data.URL)
		if e != nil || u.Scheme != "https" || u.Host != "channels.weixin.qq.com" || u.User != nil || u.Fragment != "" || !strings.HasPrefix(u.Path, "/finder-preview/") {
			return fail()
		}
		q, e := url.ParseQuery(u.RawQuery)
		if e != nil || len(q["token"]) != 1 || len(q["eid"]) > 1 {
			return fail()
		}
		token, id := q.Get("token"), q.Get("eid")
		if id == "" {
			id = r.Data.Export
		}
		if token == "" || len(token) > 8192 || id == "" || len(id) > 4096 {
			return fail()
		}
		if strings.HasPrefix(b.object, "export/") && id != b.object {
			return fail()
		}
		b.token, b.export, b.referer = token, id, u.String()
		b.phase = "channels-feed"
	case "channels-feed":
		var r struct {
			Code *int `json:"errCode"`
			Data struct {
				Author struct {
					Name string `json:"nickname"`
				} `json:"authorInfo"`
				Feed struct {
					ID    json.RawMessage `json:"objectId"`
					Key   json.RawMessage `json:"decodeKey"`
					URL   string          `json:"videoUrl"`
					Title string          `json:"description"`
					H264  struct {
						URL string `json:"videoUrl"`
					} `json:"h264VideoInfo"`
				} `json:"feedInfo"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &r) != nil || r.Code == nil || *r.Code != 0 {
			return fail()
		}
		id, e := shareUint64(r.Data.Feed.ID, true)
		if e != nil {
			return fail()
		}
		if b.object != "" && !strings.HasPrefix(b.object, "export/") && id != b.object {
			return fail()
		}
		key, e := shareUint64(r.Data.Feed.Key, false)
		if e != nil {
			return fail()
		}
		locator := r.Data.Feed.H264.URL
		if locator == "" {
			locator = r.Data.Feed.URL
		}
		u, e := url.Parse(locator)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || len(locator) > 16384 || strings.ContainsAny(locator, "\\\r\n\x00") {
			return fail()
		}
		b.resolved = &linuxnative.ChannelsSource{AccountID: b.account, ThreadID: b.thread, MessageID: b.message, ObjectID: id, MediaURL: locator, DecodeKey: key, Title: r.Data.Feed.Title, Author: r.Data.Author.Name, State: "available"}
		b.phase = "complete"
	default:
		return fail()
	}
	return nil
}

func shareUint64(raw json.RawMessage, required bool) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		if !required {
			return "", nil
		}
		return "", errors.New("missing identity")
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		value = string(raw)
	}
	if value == "" || len(value) > 20 {
		return "", errors.New("invalid identity")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return "", errors.New("invalid identity")
		}
	}
	if _, err := strconv.ParseUint(value, 10, 64); err != nil {
		return "", errors.New("invalid identity")
	}
	return value, nil
}

// ValidateResult returns a sanitized checkpoint and a private handoff from the
// host-observed feed, never from an unverified plugin locator. The caller must
// still download/decode/probe these bytes; "available" means a locator exists.
func (b *shareRouteBinding) ValidateResult(ctx context.Context, j pluginhost.Job, raw json.RawMessage, check func(context.Context) error) (pluginruntime.VerifiedResult, linuxnative.ChannelsSource, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fail := func() (pluginruntime.VerifiedResult, linuxnative.ChannelsSource, error) {
		return pluginruntime.VerifiedResult{}, linuxnative.ChannelsSource{}, errors.New("share_result_binding_failed")
	}
	if check == nil || b.phase != "complete" || b.resolved == nil || j.Context.Account != b.account || j.Context.Thread != b.thread || j.Context.SourceVersions[b.event] != b.revision || j.Tool != "share.resolve_channels" {
		return fail()
	}
	if err := check(ctx); err != nil {
		return fail()
	}
	if _, err := decodeShareJSON(raw); err != nil {
		return fail()
	}
	var result struct {
		OK         *bool    `json:"ok"`
		Platform   string   `json:"platform"`
		Source     string   `json:"source_url"`
		Object     string   `json:"object_id"`
		Title      string   `json:"title"`
		Author     string   `json:"author"`
		Locators   []string `json:"private_media_locators"`
		Key        string   `json:"private_decode_key"`
		Coverage   string   `json:"coverage"`
		Observed   *bool    `json:"video_observed"`
		Downloaded *bool    `json:"media_downloaded"`
		Untrusted  *bool    `json:"untrusted_external_content"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&result) != nil {
		return fail()
	}
	p := b.resolved
	if result.OK == nil || !*result.OK || result.Platform != "channels" || result.Source != b.share || result.Object != p.ObjectID || result.Title != p.Title || result.Author != p.Author || len(result.Locators) != 1 || result.Locators[0] != p.MediaURL || result.Key != p.DecodeKey || result.Coverage != "media_locator_only" || result.Observed == nil || *result.Observed || result.Downloaded == nil || *result.Downloaded || result.Untrusted == nil || !*result.Untrusted {
		return fail()
	}
	checkpoint, _ := json.Marshal(map[string]any{"ok": true, "platform": "channels", "source_url": b.share, "object_id": p.ObjectID, "title": p.Title, "author": p.Author, "coverage": "media_locator_only", "video_observed": false, "media_downloaded": false, "untrusted_external_content": true})
	if err := check(ctx); err != nil {
		return fail()
	}
	private, err := json.Marshal(p)
	if err != nil {
		return fail()
	}
	return pluginruntime.VerifiedResult{Result: checkpoint, Private: private}, *p, nil
}
