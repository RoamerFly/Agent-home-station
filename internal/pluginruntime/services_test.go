package pluginruntime

import (
	"context"
	"encoding/json"
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
