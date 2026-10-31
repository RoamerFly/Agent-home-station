package pluginruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"wechat-bot/internal/pluginhost"
)

func TestServiceProxyRequiresCurrentCallPermissionAndIdentity(t *testing.T) {
	ctx := context.Background()
	policy := &offlineWebPolicy{}
	j := pluginhost.Job{Context: invocation()}
	args := json.RawMessage(`{"url":"https://example.test/page","max_bytes":1024}`)
	noGrant := serviceContext(ctx, policy, j, pluginhost.Capability{})
	if _, e := noGrant.Value(serviceKey{}).(serviceCall)(ctx, "wechat/http_read", args); e == nil || policy.calls != 0 {
		t.Fatal("undeclared network proxy reached")
	}
	cap := pluginhost.Capability{Permissions: []string{"network"}}
	bound := serviceContext(ctx, policy, j, cap).Value(serviceKey{}).(serviceCall)
	if _, e := bound(ctx, "wechat/execute", args); e == nil || policy.calls != 0 {
		t.Fatal("unknown host method reached")
	}
	if _, e := bound(ctx, "wechat/http_read", args); e != nil || policy.calls != 1 {
		t.Fatal(e)
	}
	j.Context.Account = "foreign"
	foreign := serviceContext(ctx, policy, j, cap).Value(serviceKey{}).(serviceCall)
	if _, e := foreign(ctx, "wechat/http_read", args); e == nil || policy.calls != 1 {
		t.Fatal("foreign account reached")
	}
	if serviceContext(ctx, invocationPolicy{}, j, cap).Value(serviceKey{}) != nil {
		t.Fatal("non-service policy gained proxy")
	}
}

type syntheticModelDispatch struct{ offlineWebPolicy }

func (p *syntheticModelDispatch) Service(context.Context, pluginhost.Job, pluginhost.Capability, string, json.RawMessage) (json.RawMessage, error) {
	p.calls++
	return json.RawMessage(`{}`), nil
}

func TestModelServiceRequiresSeparatePermission(t *testing.T) {
	ctx := context.Background()
	p := &syntheticModelDispatch{}
	j := pluginhost.Job{Context: invocation()}
	for _, permissions := range [][]string{nil, {"network"}, {"artifact.read"}} {
		call := serviceContext(ctx, p, j, pluginhost.Capability{Permissions: permissions}).Value(serviceKey{}).(serviceCall)
		if _, err := call(ctx, "wechat/model_request", json.RawMessage(`{}`)); err == nil || p.calls != 0 {
			t.Fatal("model proxy granted without model permission")
		}
	}
	// The fake service handles any method. Reaching it proves dispatch only;
	// route/source validation has separate tests and is mandatory in production.
	call := serviceContext(ctx, p, j, pluginhost.Capability{Permissions: []string{"model.invoke"}}).Value(serviceKey{}).(serviceCall)
	_, _ = call(ctx, "wechat/model_request", json.RawMessage(`{}`))
	if p.calls != 1 {
		t.Fatal("declared model service unavailable")
	}
}

type syntheticDeviceDispatch struct {
	calls       int
	checks      int
	revoked     bool
	serviceFail error
}

func (p *syntheticDeviceDispatch) Check(_ context.Context, j pluginhost.Job, _ pluginhost.Capability) error {
	p.checks++
	if p.revoked || j.Context.Account != "a" || j.Context.Thread != "room" || j.Context.Requester != "person" {
		return errors.New("synthetic device authorization revoked")
	}
	return nil
}

func (p *syntheticDeviceDispatch) Validate(context.Context, pluginhost.Job, pluginhost.Capability, json.RawMessage) (VerifiedResult, error) {
	return VerifiedResult{}, nil
}

func (p *syntheticDeviceDispatch) Service(_ context.Context, _ pluginhost.Job, _ pluginhost.Capability, method string, _ json.RawMessage) (json.RawMessage, error) {
	p.calls++
	if method != "wechat/device_request" {
		return nil, errors.New("unexpected device method")
	}
	if p.serviceFail != nil {
		return nil, p.serviceFail
	}
	return json.RawMessage(`{"state":"synthetic"}`), nil
}

func TestDeviceServiceIsBoundToExplicitCapabilityAndCurrentJob(t *testing.T) {
	ctx := context.Background()
	args := json.RawMessage(`{"operation":"observe"}`)
	p := &syntheticDeviceDispatch{}
	j := pluginhost.Job{Context: invocation()}

	for _, cap := range []pluginhost.Capability{
		{ID: "device.observe"},
		{ID: "device.plan", Permissions: []string{"device"}},
		{ID: "media.observe", Permissions: []string{"device"}},
	} {
		call := serviceContext(ctx, p, j, cap).Value(serviceKey{}).(serviceCall)
		if _, err := call(ctx, "wechat/device_request", args); err == nil || p.calls != 0 {
			t.Fatalf("unauthorized device proxy reached dispatcher for %#v", cap)
		}
	}
	valid := pluginhost.Capability{ID: "device.observe", Permissions: []string{"device"}}
	if serviceContext(ctx, invocationPolicy{}, j, valid).Value(serviceKey{}) != nil {
		t.Fatal("missing device broker did not fail closed")
	}
	call := serviceContext(ctx, p, j, valid).Value(serviceKey{}).(serviceCall)
	if _, err := call(ctx, "wechat/execute", args); err == nil || p.calls != 0 {
		t.Fatal("arbitrary device execution reached dispatcher")
	}
	if _, err := call(ctx, "wechat/device_request", args); err != nil || p.calls != 1 || p.checks != 2 {
		t.Fatalf("bound current device request failed: err=%v calls=%d checks=%d", err, p.calls, p.checks)
	}

	foreign := j
	foreign.Context.Account = "foreign"
	if _, err := serviceContext(ctx, p, foreign, valid).Value(serviceKey{}).(serviceCall)(ctx, "wechat/device_request", args); err == nil || p.calls != 1 {
		t.Fatal("cross-account device request reached dispatcher")
	}
	p.revoked = true
	if _, err := call(ctx, "wechat/device_request", args); err == nil || p.calls != 1 {
		t.Fatal("revoked device request reached dispatcher")
	}
}

func TestDeviceServiceFailureIsNotMasked(t *testing.T) {
	ctx := context.Background()
	p := &syntheticDeviceDispatch{serviceFail: errors.New("synthetic device broker unavailable")}
	call := serviceContext(ctx, p, pluginhost.Job{Context: invocation()}, pluginhost.Capability{ID: "device.act", Permissions: []string{"device"}}).Value(serviceKey{}).(serviceCall)
	if _, err := call(ctx, "wechat/device_request", json.RawMessage(`{"operation":"tap"}`)); !errors.Is(err, p.serviceFail) || p.calls != 1 || p.checks != 1 {
		t.Fatalf("service failure was masked: err=%v calls=%d checks=%d", err, p.calls, p.checks)
	}
}
