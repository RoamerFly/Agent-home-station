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
