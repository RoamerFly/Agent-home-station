package admin

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

// One resolver chain for one host-verified native message. Never construct from
// model tool arguments. This guard is independent of the plugin's own parsing.
type shareRouteBinding struct {
	mu    sync.Mutex
	route wechatapp.ChannelsRoute
}

func newShareRouteBinding(message linuxnative.Message, source linuxnative.ChannelsSource, ref companion.SourceRef, verifiedShare string) (*shareRouteBinding, error) {
	identity := wechatapp.ChannelsEvent{Account: ref.Account, Thread: ref.Thread, Message: ref.MessageID, EventID: ref.EventID, Revision: ref.Revision}
	route, err := wechatapp.NewChannelsRoute(message, source, identity, verifiedShare)
	if err != nil {
		return nil, err
	}
	return &shareRouteBinding{route: route}, nil
}

// check must re-read current authorization and source revision. send is the
// host route proxy/transport. Uncertain submissions poison this chain; it never
// silently replays a POST or falls back to another received share.
func (b *shareRouteBinding) Call(ctx context.Context, store *pluginhost.Store, j pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage, check func(context.Context) error, send pluginruntime.RouteTransport) (json.RawMessage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if check == nil || send == nil || j.Context.Account != b.route.Account || j.Context.Thread != b.route.Thread || j.Context.SourceVersions[b.route.Event] != b.route.Revision || j.Tool != "share.resolve_channels" || cap.ID != j.Tool {
		return nil, errors.New("share_source_binding_changed")
	}
	if err := check(ctx); err != nil {
		return nil, err
	}
	phase, err := b.route.ExpectedRequest(raw)
	if err != nil {
		return nil, err
	}
	b.route.Phase = "failed"
	result, err := pluginruntime.ProxyHTTPRoute(ctx, store, j, cap, raw, func(string, json.RawMessage) error { return nil }, send)
	if err != nil {
		return nil, err
	}
	if err = check(ctx); err != nil {
		return nil, err
	}
	if err = b.route.Accept(phase, result); err != nil {
		return nil, err
	}
	return result, nil
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
	if check == nil || b.route.Phase != "complete" || b.route.Resolved == nil || j.Context.Account != b.route.Account || j.Context.Thread != b.route.Thread || j.Context.SourceVersions[b.route.Event] != b.route.Revision || j.Tool != "share.resolve_channels" {
		return fail()
	}
	if err := check(ctx); err != nil {
		return fail()
	}
	checkpoint, source, err := b.route.ValidateResult(raw)
	if err != nil {
		return fail()
	}
	if err := check(ctx); err != nil {
		return fail()
	}
	private, err := json.Marshal(source)
	if err != nil {
		return fail()
	}
	return pluginruntime.VerifiedResult{Result: checkpoint, Private: private}, source, nil
}
