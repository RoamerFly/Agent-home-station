package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/sharespec"
)

// configuredChannelsBridge reads only the installed share-reader
// configuration.  The legacy channels settings were an input to migration;
// they must not continue to grant a bridge endpoint after the plugin is
// installed. The host, not plugin arguments, selects the configured endpoint;
// a verified source_url is returned only after the bounded bridge read.
func (p *receivedSharePolicy) configuredChannelsBridge(ctx context.Context) (string, bool, error) {
	configuration, err := p.store.ConfigurationForExecution(ctx, "share-reader")
	if err != nil {
		return "", false, errors.New("share_bridge_configuration_unavailable")
	}
	raw, ok := configuration.Values["object_bridge_url"]
	if !ok {
		return "", false, nil
	}
	var configured string
	if json.Unmarshal(raw, &configured) != nil || configured == "" {
		return "", false, errors.New("share_bridge_configuration_invalid")
	}
	base, err := sharespec.ObjectBridgeURL(configured)
	if err != nil {
		return "", false, errors.New("share_bridge_configuration_invalid")
	}
	return base, true, nil
}

// bridgeTimeout binds the callback deadline to the host-created job arguments.
// A plugin may choose whether to call its host service, but it must not expand
// the caller's selected execution budget while doing so.
func bridgeTimeout(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 25, nil
	}
	var in struct {
		ObjectID       string `json:"object_id"`
		NonceID        string `json:"nonce_id,omitempty"`
		TimeoutSeconds *int64 `json:"timeout_seconds,omitempty"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		return 0, errors.New("share_bridge_timeout_invalid")
	}
	if in.TimeoutSeconds == nil {
		return 25, nil
	}
	if *in.TimeoutSeconds < 1 || *in.TimeoutSeconds > int64((time.Duration(1<<63-1))/time.Second) {
		return 0, errors.New("share_bridge_timeout_invalid")
	}
	return *in.TimeoutSeconds, nil
}

func (p *receivedSharePolicy) channelsBridge(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage) (json.RawMessage, error) {
	if p.binding == nil || p.message.AppMessageType == nil || *p.message.AppMessageType != 51 {
		return nil, errors.New("share_bridge_unavailable")
	}
	if err := p.Check(ctx, j, cap); err != nil {
		return nil, err
	}
	var in struct {
		ObjectID       string `json:"object_id"`
		NonceID        string `json:"nonce_id,omitempty"`
		TimeoutSeconds *int64 `json:"timeout_seconds,omitempty"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF || in.ObjectID != p.binding.object || in.NonceID != p.binding.nonce {
		return nil, errors.New("share_bridge_source_mismatch")
	}
	base, configured, err := p.configuredChannelsBridge(ctx)
	if err != nil || !configured {
		return nil, errors.New("share_bridge_unavailable")
	}
	expectedTimeout, err := bridgeTimeout(j.Arguments)
	if err != nil {
		return nil, err
	}
	actualTimeout := int64(25)
	if in.TimeoutSeconds != nil {
		actualTimeout = *in.TimeoutSeconds
	}
	// The caller may select any positive duration representable by Go's HTTP
	// client. The callback must use that exact effective value, rather than
	// silently increasing a budget selected by the Agent or host adapter.
	if actualTimeout < 1 || actualTimeout > int64((time.Duration(1<<63-1))/time.Second) || actualTimeout != expectedTimeout {
		return nil, errors.New("share_bridge_timeout_invalid")
	}
	// Reserve the one source-bound bridge read before opening the connection.
	// Unlike the credential route state machine this path has only one request,
	// so a failed/in-flight phase is sufficient to prevent a compromised plugin
	// session from replaying the same backend read concurrently or afterward.
	p.binding.mu.Lock()
	if p.binding.phase != "channels-bridge" {
		p.binding.mu.Unlock()
		return nil, errors.New("share_bridge_already_consumed")
	}
	p.binding.phase = "failed"
	p.binding.mu.Unlock()
	timeout := time.Duration(actualTimeout) * time.Second
	q := url.Values{"object_id": {in.ObjectID}, "nonce_id": {in.NonceID}}
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/channels/feed/profile?"+q.Encode(), nil)
	// This transport is scoped to one bound read; do not leave an unreachable
	// keep-alive pool alive after the source operation completes.
	client := &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("share_bridge_unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("share_bridge_rejected")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(body) > 2<<20 {
		return nil, errors.New("share_bridge_response_invalid")
	}
	video, err := sharespec.ParseObjectBridgeResponse(body, in.ObjectID)
	if err != nil {
		return nil, errors.New("share_bridge_response_invalid")
	}
	if err = p.Check(ctx, j, cap); err != nil {
		return nil, err
	}
	p.binding.mu.Lock()
	p.binding.share = base
	p.binding.resolved = &linuxnative.ChannelsSource{AccountID: p.binding.account, ThreadID: p.binding.thread, MessageID: p.binding.message, ObjectID: video.ObjectID, MediaURL: video.URL, DecodeKey: video.DecodeKey, Title: video.Title, Author: video.Author, State: "available"}
	p.binding.phase = "complete"
	p.binding.mu.Unlock()
	return json.Marshal(map[string]any{"source_url": base, "feed": json.RawMessage(body)})
}
