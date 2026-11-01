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

func deviceProxyCapability(capability string) bool {
	switch capability {
	case "device.list", "device.observe", "device.act", "device.record", "device.read_artifact":
		return true
	default:
		return false
	}
}

func serviceContext(ctx context.Context, policy CallPolicy, job pluginhost.Job, cap pluginhost.Capability) context.Context {
	service, ok := policy.(ServicePolicy)
	if !ok {
		return ctx
	}
	return context.WithValue(ctx, serviceKey{}, serviceCall(func(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		allowed := false
		artifactRead, modelInvoke := false, false
		for _, p := range cap.Permissions {
			artifactRead = artifactRead || p == "artifact.read"
			modelInvoke = modelInvoke || p == "model.invoke"
			if p == "network" && (method == "wechat/http_read" || method == "wechat/http_route" || cap.ID == "share.resolve_channels" && method == "wechat/channels_bridge") {
				allowed = true
			}
			if p == "model.invoke" && method == "wechat/model_request" {
				allowed = true
			}
			if p == "network" && job.Plugin == "music" && cap.ID == "music.bridge_request" && method == "wechat/music_bridge" {
				allowed = true
			}
			if p == "network" && job.Plugin == "music" && cap.ID == "music.service_control" && method == "wechat/service_control" {
				allowed = true
			}
			if p == "network" && job.Plugin == "external-tools" && (cap.ID == "external.http_request" || cap.ID == "external.job_request" || cap.ID == "external.poll_request") && method == "wechat/external_request" {
				allowed = true
			}
			if p == "network" && job.Plugin == "external-tools" && cap.ID == "external.mcp_request" && method == "wechat/external_mcp" {
				allowed = true
			}
			if job.Plugin == "project-development" && method == "wechat/workspace_request" &&
				(cap.ID == "project.workspace_read" && p == "workspace.read" || cap.ID == "project.workspace_write" && p == "workspace.write" || cap.ID == "project.workspace_execute" && p == "process.execute") {
				allowed = true
			}
			// Local transcription is a bounded host adapter for one verified
			// media reference. It does not grant the plugin a general process
			// executor: the host resolves its fixed runner and resource profile.
			if p == "artifact.read" && cap.ID == "media.transcribe" && method == "wechat/local_transcription" {
				allowed = true
			}
			// Device access is only a bound host broker request. It does not expose
			// a physical device, a process launcher, a socket, or a mount to the
			// plugin. In particular, device.plan has only model.invoke authority.
			if p == "device" && job.Plugin == "mobile-control" && deviceProxyCapability(cap.ID) && method == "wechat/device_io" {
				allowed = true
			}
		}
		// Named inference has one fixed host runner and a bound voice resource
		// set. This never authorizes an arbitrary process or workspace operation.
		if job.Plugin == "speech" && cap.ID == "speech.synthesize_named" && method == "wechat/local_speech" {
			allowed = artifactRead && modelInvoke
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

// InvokeHostService dispatches through the authority bound to this exact call.
// Executors cannot manufacture that authority: its context key is private to
// this package and serviceContext always performs the current policy checks.
func InvokeHostService(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if handler, ok := ctx.Value(serviceKey{}).(serviceCall); ok {
		return handler(ctx, method, params)
	}
	return nil, errors.New("host service unavailable")
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
	result, err := InvokeHostService(ctx, method, params)
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
