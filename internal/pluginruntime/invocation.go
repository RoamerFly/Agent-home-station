package pluginruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"wechat-bot/internal/pluginhost"
)

// CallPolicy is implemented by the trusted host adapter. Check must validate
// current request authority and source versions. Validate must resolve and check
// actual files/lineage before returning asset IDs; a plugin claim is insufficient.
// Neither callback comes from tool arguments or plugin code.
type CallPolicy interface {
	Check(context.Context, pluginhost.Job, pluginhost.Capability) error
	Validate(context.Context, pluginhost.Job, pluginhost.Capability, json.RawMessage) (VerifiedResult, error)
}
type VerifiedResult struct {
	RemoteID string
	Assets   []string
	Failed   bool
	// Result must be sanitized for this account/conversation by Validate.
	Result json.RawMessage
	// Only a trusted host validator supplies this handoff, never raw tool output.
	// Stored atomically outside the public Job record; excluded from serialization.
	Private json.RawMessage `json:"-"`
}
type CallRequest struct {
	Plugin     string
	Capability string
	Context    pluginhost.Invocation
	Arguments  json.RawMessage
}
type CallResult struct {
	Job    pluginhost.Job `json:"job"`
	Reused bool           `json:"reused"`
}

// Invoke persists intent before any process call. An uncertain submission is
// never repeated, including after a crash, timeout, source change or lost result.
// It does not start disabled plugins, send WeChat messages, or infer delivery.
func (c *Controller) Invoke(ctx context.Context, req CallRequest, policy CallPolicy) (CallResult, error) {
	var out CallResult
	if policy == nil || req.Context.Account != c.store.Account() {
		return out, errors.New("host invocation policy required")
	}
	// Reserve lifecycle across store snapshots and checkpointing, including the
	// gaps before Host.Call and after its active-call counter has been released.
	if err := c.begin(req.Plugin); err != nil {
		return out, err
	}
	defer c.endCall(req.Plugin)
	i, err := c.store.Installed(ctx, req.Plugin)
	if err != nil {
		return out, err
	}
	if !i.Enabled || i.Removed || c.host.Status(req.Plugin).State != "running" {
		return out, errors.New("plugin not running")
	}
	caps, err := c.store.Capabilities(ctx, req.Plugin)
	if err != nil {
		return out, err
	}
	var cap pluginhost.Capability
	for _, v := range caps {
		if v.ID == req.Capability && v.Discoverable && v.Implementation == "ready" {
			cap = v
			break
		}
	}
	if cap.ID == "" {
		return out, errors.New("capability unavailable")
	}
	grant := pluginhost.Grant{Account: c.store.Account(), Plugin: i.Package.ID, Version: i.Package.Version, Permissions: i.Permissions}
	if err = pluginhost.Authorize(i.Package, grant, req.Context, cap.Permissions); err != nil {
		return out, err
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(req.Arguments, &object) != nil || object == nil {
		return out, errors.New("tool arguments must be an object")
	}
	config, err := c.store.Configuration(ctx, req.Plugin)
	if err != nil {
		return out, err
	}
	j := pluginhost.Job{Plugin: i.Package.ID, Version: i.Package.Version, PackageDigest: i.Digest, ConfigRevision: strconv.Itoa(config.Revision), Tool: cap.ID, Context: req.Context, Arguments: req.Arguments}
	if err = policy.Check(ctx, j, cap); err != nil {
		return out, err
	}
	j, created, err := c.store.CreateJob(ctx, j)
	if err != nil {
		return out, err
	}
	out.Job, out.Reused = j, !created
	// A pending record can be continued only after successful ClaimJob CAS;
	// submitting/running/terminal records are returned without process replay.
	if j.State != "pending" {
		return out, nil
	}
	if err = policy.Check(ctx, j, cap); err != nil {
		return out, err
	}
	j, err = c.store.ClaimJob(ctx, j.ID, j.Revision)
	if err != nil {
		return out, err
	}
	out.Job = j
	// Execute the exact canonical request read back by ClaimJob, not caller-owned
	// maps or alternate JSON text that differs from the durable fingerprint.
	raw, callErr := c.host.Call(serviceContext(ctx, policy, j, cap), req.Plugin, j.Context, cap.ID, j.Arguments, cap.Permissions)
	var verified VerifiedResult
	if callErr == nil {
		callErr = policy.Check(ctx, j, cap)
	}
	if callErr == nil {
		verified, callErr = policy.Validate(ctx, j, cap, raw)
	}
	if callErr == nil && ((!verified.Failed && cap.Async && verified.RemoteID == "") || (!cap.Async && verified.RemoteID != "") || (verified.RemoteID != "" && (verified.Failed || len(verified.Assets) > 0 || len(verified.Result) > 0 || len(verified.Private) > 0)) || (verified.Failed && (len(verified.Assets) > 0 || len(verified.Private) > 0))) {
		callErr = errors.New("plugin result does not match capability execution mode")
	}
	// Checkpoint with a separate bounded context even when the caller cancelled.
	checkpoint, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		current, readErr := c.store.Job(checkpoint, j.ID)
		if readErr != nil {
			return out, errors.Join(callErr, readErr)
		}
		var next pluginhost.Job
		var writeErr error
		switch {
		case callErr != nil:
			next, writeErr = c.store.FinishJob(checkpoint, j.ID, current.Revision, "needs_review", nil)
		case verified.RemoteID != "":
			next, writeErr = c.store.AcknowledgeJob(checkpoint, j.ID, current.Revision, verified.RemoteID)
		case verified.Failed:
			next, writeErr = c.store.FailJob(checkpoint, j.ID, current.Revision, verified.Result)
		default:
			next, writeErr = c.store.CompleteJobWithPrivate(checkpoint, j.ID, current.Revision, verified.Assets, verified.Result, verified.Private)
		}
		if errors.Is(writeErr, pluginhost.ErrRevision) && checkpoint.Err() == nil {
			continue
		}
		if writeErr == nil {
			out.Job = next
		}
		return out, errors.Join(callErr, writeErr)
	}
}

func (c *Controller) endCall(id string) {
	c.mu.Lock()
	delete(c.busy, id)
	c.mu.Unlock()
}
