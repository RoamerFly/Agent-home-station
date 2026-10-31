package pluginruntime

import (
	"context"
	"encoding/json"
	"errors"

	"wechat-bot/internal/pluginhost"
)

// ServicePolicy is an optional, host-owned authority proxy. Plugins receive no
// raw network/file/credential authority. Requests are bound to the active call;
// initialize, health checks and idle processes cannot use this proxy.
type ServicePolicy interface {
	Service(context.Context, pluginhost.Job, pluginhost.Capability, string, json.RawMessage) (json.RawMessage, error)
}
type serviceKey struct{}
type serviceCall func(context.Context, string, json.RawMessage) (json.RawMessage, error)

func serviceContext(ctx context.Context, policy CallPolicy, job pluginhost.Job, cap pluginhost.Capability) context.Context {
	service, ok := policy.(ServicePolicy)
	if !ok {
		return ctx
	}
	return context.WithValue(ctx, serviceKey{}, serviceCall(func(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		allowed := false
		for _, p := range cap.Permissions {
			if p == "network" && (method == "wechat/http_read" || method == "wechat/http_route") {
				allowed = true
			}
		}
		if !allowed {
			return nil, errors.New("host service not permitted by capability")
		}
		if err := policy.Check(ctx, job, cap); err != nil {
			return nil, err
		}
		result, err := service.Service(ctx, job, cap, method, params)
		if err == nil {
			err = policy.Check(ctx, job, cap)
		}
		return result, err
	}))
}

func (s *session) serviceReply(ctx context.Context, id json.RawMessage, method string, params json.RawMessage) error {
	var requestID any
	if json.Unmarshal(id, &requestID) != nil || requestID == nil {
		return errors.New("invalid plugin service request identity")
	}
	switch requestID.(type) {
	case string, float64:
	default:
		return errors.New("invalid plugin service request identity")
	}
	var result json.RawMessage
	err := errors.New("host service unavailable")
	if handler, ok := ctx.Value(serviceKey{}).(serviceCall); ok {
		result, err = handler(ctx, method, params)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	frame := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id)}
	if err != nil {
		// Arbitrary errors can contain URLs, credentials or host paths.
		frame["error"] = map[string]any{"code": -32001, "message": "host_service_failed"}
	} else {
		if !json.Valid(result) {
			return errors.New("invalid host service response")
		}
		frame["result"] = result
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if len(raw)+1 > s.maxFrame {
		return errors.New("host service response exceeds transport frame; request smaller page")
	}
	_, err = s.in.Write(append(raw, '\n'))
	return err
}
