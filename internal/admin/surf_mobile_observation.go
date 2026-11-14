package admin

// This file composes the narrowly-scoped phone observation capability into a
// private surf run.  It intentionally owns no ADB, screenshot, broker or
// device-planning code.  The mobile host validates the frozen app grant again
// while its state lock is held; this layer only proves that the live private
// surf claim is still current.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sort"
	"sync/atomic"
	"time"

	"wechat-bot/internal/mcptools"
)

const privateMobileObservationToolName = "mobile_observe_authorized_app"

// privateMobileObservationOrigin is never persisted.  The app grant is frozen
// at private-surf tool issuance, then independently revalidated by the mobile
// host immediately before and after an observation is committed.
type privateMobileObservationOrigin struct {
	surf    *privateSurfOrigin
	grant   mobileObservationAppGrant
	attempt atomic.Bool
}

func (o *privateMobileObservationOrigin) check(ctx context.Context, p *permittedTools) error {
	if o == nil || p == nil || o.surf == nil || p.privateSurfOrigin != o.surf {
		return errors.New("private_mobile_observation_authorization_required")
	}
	return o.surf.check(ctx, p.trace)
}

type privateMobileObservationCandidate struct {
	deviceID string
	packageN string
}

// bindPrivateMobileObservation makes selection deterministic for one private
// surf claim.  Overview is a non-starting snapshot; mobileDeviceReady is also
// deliberately non-starting.  A trace-hash rotation avoids permanently
// preferring the alphabetically first app when several apps are explicitly
// granted, while the selected grant remains fixed for the whole run.
func (p *permittedTools) bindPrivateMobileObservation(ctx context.Context) {
	if p == nil || p.manager == nil || p.privateSurfOrigin == nil || p.privateMobileObservation != nil {
		return
	}
	if p.privateSurfOrigin.check(ctx, p.trace) != nil {
		return
	}
	service, ok := p.manager.mobile.(*mobileWorkflowService)
	if !ok {
		return
	}
	overview := service.Overview()
	candidates := make([]privateMobileObservationCandidate, 0)
	for _, device := range overview.Devices {
		if device.DeviceID == "" || device.FishID == "" || device.BindingID == "" || device.Paused || device.Takeover {
			continue
		}
		for _, app := range device.ObservationApps {
			if app.Available && app.GrantID != "" && app.Package != "" {
				candidates = append(candidates, privateMobileObservationCandidate{deviceID: device.DeviceID, packageN: app.Package})
			}
		}
	}
	if len(candidates) == 0 {
		return
	}
	if p.manager.mobileDeviceReady(ctx) != nil {
		return
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].deviceID == candidates[j].deviceID {
			return candidates[i].packageN < candidates[j].packageN
		}
		return candidates[i].deviceID < candidates[j].deviceID
	})
	hash := sha256.Sum256([]byte(p.trace.TraceID + "\x00" + p.privateSurfOrigin.claim))
	start := int(uint64(binary.BigEndian.Uint32(hash[:4])) % uint64(len(candidates)))
	for offset := 0; offset < len(candidates); offset++ {
		candidate := candidates[(start+offset)%len(candidates)]
		grant, found, err := service.ObservationAppGrant(candidate.deviceID, candidate.packageN)
		if err != nil || !found || grant.GrantID == "" || grant.Account != p.trace.Account || grant.DeviceID != candidate.deviceID || grant.Package != candidate.packageN {
			continue
		}
		p.privateMobileObservation = &privateMobileObservationOrigin{surf: p.privateSurfOrigin, grant: grant}
		return
	}
}

func (p *permittedTools) privateMobileObservationTools() []mcptools.Tool {
	origin := p.privateMobileObservation
	if origin == nil || origin.attempt.Load() || origin.check(context.Background(), p) != nil {
		return nil
	}
	return []mcptools.Tool{agentTool(privateMobileObservationToolName,
		"Observe the current screen of one app that the owner explicitly granted to this fish for private, read-only autonomous observation. No arguments. This can run at most once in this private exploration; it cannot open another app, act on the device, send anything, or reveal screenshots, paths, credentials, or authorization details. The result only confirms safe observation metadata.",
		`{"type":"object","properties":{},"additionalProperties":false}`)}
}

func (p *permittedTools) callPrivateMobileObservation(ctx context.Context, _ string, raw json.RawMessage) (string, error) {
	if !bytes.Equal(bytes.TrimSpace(raw), []byte("{}")) {
		return "", errors.New("private_mobile_observation_arguments_forbidden")
	}
	origin := p.privateMobileObservation
	if origin == nil || origin.check(ctx, p) != nil {
		return "", errors.New("private_mobile_observation_authorization_required")
	}
	// Consume before asking the host to wake the broker.  A failed first attempt
	// is still an attempt: retrying a private surf claim must never create an
	// unbounded observation loop.
	if !origin.attempt.CompareAndSwap(false, true) {
		return "", errors.New("private_mobile_observation_already_attempted")
	}
	service, ok := p.manager.mobile.(*mobileWorkflowService)
	if !ok {
		return "", errors.New("private_mobile_observation_unavailable")
	}
	overview, err := service.observeAuthorizedApp(ctx, origin.grant, func(checkCtx context.Context) error {
		return origin.check(checkCtx, p)
	})
	if err != nil {
		return "", err
	}
	if err := origin.check(ctx, p); err != nil {
		return "", err
	}
	current, found, err := service.ObservationAppGrant(origin.grant.DeviceID, origin.grant.Package)
	if err != nil || !found || current != origin.grant {
		return "", errors.New("private_mobile_observation_authorization_changed")
	}
	// Return references and actual coverage. Pixels and authorization tokens
	// stay in the host for the later private-experience integration.
	result, _ := json.Marshal(map[string]any{
		"observed":             true,
		"device_id":            overview.DeviceID,
		"app_package":          origin.grant.Package,
		"observation_id":       overview.ObservationID,
		"task_id":              overview.TaskID,
		"observed_at":          overview.ObservedAt.UTC().Format(time.RFC3339),
		"observation_range":    "single_current_screenshot",
		"connection_state":     overview.ConnectionState,
		"current_app_verified": overview.CurrentApp == origin.grant.Package,
	})
	return string(result), nil
}
