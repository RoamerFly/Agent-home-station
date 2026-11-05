package admin

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"sync"
	"time"

	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

const musicServiceControlCapability = "music.service_control"

type musicServiceControlCheckKey struct{}

func checkMusicServiceControl(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if check, ok := ctx.Value(musicServiceControlCheckKey{}).(func(context.Context) error); ok {
		return check(ctx)
	}
	return nil
}

type musicServiceControlPolicy struct {
	base      *localMusicPluginPolicy
	action    string
	arguments json.RawMessage
	mu        sync.Mutex
	requested bool
	observed  json.RawMessage
}

func (p *permittedTools) newMusicServiceControlPolicy(ctx context.Context, action string) (*musicServiceControlPolicy, error) {
	if action != "start" && action != "status" && action != "stop" {
		return nil, toolInputError("music_service_action_invalid")
	}
	authority, _ := ctx.Value(speechAdminTestContextKey{}).(*speechAdminTestAuthority)
	if authority == nil && !p.privileged() {
		return nil, toolInputError("music_service_control_requires_owner")
	}
	// A lifecycle operation has the same evidence requirements as a bounded
	// health observation, plus current owner/admin authority on every check.
	base, err := p.newLocalMusicPluginPolicy(ctx, "GET", "/health", nil, "")
	if err != nil {
		return nil, err
	}
	base.invocation.Purpose = "control registered local music service"
	args, _ := json.Marshal(map[string]string{"action": action})
	policy := &musicServiceControlPolicy{base: base, action: action, arguments: args}
	if err := policy.Check(ctx, pluginhost.Job{Plugin: "music", Tool: musicServiceControlCapability, Context: base.invocation}, pluginhost.Capability{}); err != nil {
		return nil, err
	}
	return policy, nil
}

func (p *musicServiceControlPolicy) Check(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability) error {
	if p.base.admin == nil && !p.base.p.privileged() {
		return errors.New("music_service_owner_authorization_changed")
	}
	return p.base.checkBoundCall(ctx, j, cap, musicServiceControlCapability, p.arguments)
}

func (p *musicServiceControlPolicy) Service(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, method string, raw json.RawMessage) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.Check(ctx, j, cap); err != nil {
		return nil, err
	}
	var in struct {
		Action string `json:"action"`
	}
	if method != "wechat/service_control" || p.requested || musicStrictJSON(raw, &in) != nil || in.Action != p.action || !sameJSON(raw, p.arguments) {
		return nil, errors.New("music_service_request_changed")
	}
	p.requested = true // An uncertain launch/stop is never automatically retried.
	m := p.base.p.manager
	var result json.RawMessage
	switch p.action {
	case "start":
		ctx = context.WithValue(ctx, musicServiceControlCheckKey{}, func(ctx context.Context) error { return p.Check(ctx, j, cap) })
		value, err := p.base.p.startMusicServiceHost(ctx)
		if err != nil {
			return nil, err
		}
		result = json.RawMessage(value)
	case "status":
		state := m.windowsMusicStatus()
		configured := m.localMusicPluginConfigured(ctx)
		if runtime.GOOS == "windows" {
			configured = configured && m.windowsMusicRegistered()
		}
		result, _ = json.Marshal(map[string]any{"platform": runtime.GOOS, "configured": configured, "running": state.Running, "busy": state.Busy, "stopping": state.Stopping, "coverage": "owned_supervisor_only", "model_ready_verified": false})
	case "stop":
		if runtime.GOOS != "windows" {
			return nil, toolInputError("managed_local_music_stop_not_applicable")
		}
		if m.windowsMusicBusy() {
			return nil, toolInputError("music_service_has_pending_jobs")
		}
		if err := p.Check(ctx, j, cap); err != nil {
			return nil, err
		}
		if err := m.managedServiceRegistry().Stop(ctx, windowsMusicServiceID); err != nil {
			return nil, err
		}
		result = json.RawMessage(`{"state":"stopped","coverage":"owned_supervisor_only","wechat_sends":0}`)
	}
	if err := p.Check(ctx, j, cap); err != nil {
		return nil, err
	}
	if !json.Valid(result) {
		return nil, errors.New("music_service_reply_invalid")
	}
	p.observed = append(json.RawMessage(nil), result...)
	return result, nil
}

func (p *musicServiceControlPolicy) Validate(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.Check(ctx, j, cap); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	if !p.requested || len(p.observed) == 0 || !sameJSON(raw, p.observed) {
		return pluginruntime.VerifiedResult{}, errors.New("music_service_result_unverified")
	}
	return pluginruntime.VerifiedResult{Result: append(json.RawMessage(nil), p.observed...)}, nil
}

func (p *permittedTools) controlMusicService(ctx context.Context, action string) (string, error) {
	policy, err := p.newMusicServiceControlPolicy(ctx, action)
	if err != nil {
		return "", err
	}
	c, ready, err := p.manager.installedPluginRuntime()
	if err != nil || !ready {
		return "", errors.New("music_service_plugin_runtime_unavailable")
	}
	i, err := policy.base.store.Installed(ctx, "music")
	if err != nil {
		return "", err
	}
	if err = c.EnsureStarted(ctx, "music", i.Revision); err != nil {
		return "", err
	}
	call, err := c.Invoke(ctx, pluginruntime.CallRequest{Plugin: "music", Capability: musicServiceControlCapability, Context: policy.base.invocation, Arguments: policy.arguments}, policy)
	if err != nil {
		return "", err
	}
	if call.Job.State != "completed" {
		return "", errors.New("music_service_control_needs_review")
	}
	if err = policy.Check(ctx, call.Job, pluginhost.Capability{}); err != nil {
		return "", err
	}
	if action == "start" && runtime.GOOS == "windows" {
		// Launch is asynchronous. Check the authenticated account endpoint outside
		// the plugin call so its stdio session is available for other operations.
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		base := policy.base
		ready := func(probeCtx context.Context) (yueHealth, int, error) {
			body, _, status, err := localMusicHTTP(probeCtx, "GET", base.endpoint+"/health", nil, "", base.token, accountDirectory(p.manager.native.Account()))
			var health yueHealth
			if err == nil && status == 200 && json.Unmarshal(body, &health) != nil {
				err = yueError("yue_response_limit")
			}
			return health, status, err
		}
		if err := waitWindowsMusicReady(ctx, func(checkCtx context.Context) error {
			return policy.Check(checkCtx, call.Job, pluginhost.Capability{})
		}, func() bool {
			return p.manager.windowsMusicStatus().Running
		}, ready, time.Second); err != nil {
			return "", err
		}
		return `{"state":"ready","backend":"windows-native","ready":true}`, nil
	}
	return string(call.Job.Result), nil
}

// waitWindowsMusicReady keeps launch asynchronous while making readiness part
// of one bounded, authorization-aware lifecycle operation. Conversion is not
// submitted until this returns nil.
func waitWindowsMusicReady(ctx context.Context, check func(context.Context) error, running func() bool, probe func(context.Context) (yueHealth, int, error), retryDelay time.Duration) error {
	for {
		if err := check(ctx); err != nil {
			return err
		}
		if !running() {
			return toolInputError("windows_music_supervisor_exited_check_runtime_logs")
		}
		probeCtx, stop := context.WithTimeout(ctx, 3*time.Second)
		health, status, err := probe(probeCtx)
		stop()
		if err == nil && status == 200 && health.Ready && health.BridgeVersion > 0 {
			return check(ctx)
		}
		// Scope, authentication and malformed replies are not warm-up states.
		// Retrying them hides a real configuration or account boundary.
		if err != nil {
			_, code := safeToolFailure(err)
			switch code {
			case "yue_account_scope_mismatch", "yue_bridge_auth_failed", "yue_response_limit":
				return err
			}
		}
		if status == 401 || status == 403 {
			return yueError("yue_bridge_auth_failed")
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return context.Canceled
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return toolInputError("windows_music_startup_health_timeout")
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return context.Canceled
			}
			return toolInputError("windows_music_startup_health_timeout")
		case <-time.After(retryDelay):
		}
	}
}

var _ pluginruntime.ServicePolicy = (*musicServiceControlPolicy)(nil)
