package admin

// This file is the host-only bridge for mobile-control's device capabilities.
// The isolated package can request a single already-bound operation, but it
// never sees raw screenshots, recording chunks, an ADB path, or the legacy
// bridge configuration.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"wechat-bot/internal/devicebroker"
	"wechat-bot/internal/mobile"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

const mobileDevicePluginID = "mobile-control"

var mobileDeviceCapabilities = []string{
	"device.list", "device.observe", "device.act", "device.record", "device.read_artifact",
}

// mobileDeviceBrokerOpener is a package seam for synthetic host-policy tests.
// Production always uses the fixed devicebroker implementation.
type mobileDeviceBrokerOpener func(context.Context, devicebroker.Options) (devicebroker.Bridge, error)

var openMobileDeviceBroker mobileDeviceBrokerOpener = devicebroker.Open

func (m *Manager) loadMobileNativeRegistration() *devicebroker.RuntimeRegistration {
	if m == nil {
		return nil
	}
	value, _ := m.pluginRuntimeBindings.Load("mobile-control:native")
	registration, _ := value.(*devicebroker.RuntimeRegistration)
	return registration
}

// mobileDeviceReady is deliberately non-starting. Discovery and task queueing
// must not start a native process, enable a package, or widen a device grant.
func (m *Manager) mobileDeviceReady(ctx context.Context) error {
	if err := m.loadMobileNativeRegistration().Validate(ctx, m.native.Account()); err != nil {
		return errors.New("device_broker_runtime_registration_required")
	}
	store, err := m.packageStore()
	if err != nil {
		return errors.New("device_plugin_required")
	}
	installed, err := store.Installed(ctx, mobileDevicePluginID)
	if err != nil || !installed.Enabled || installed.Removed || store.CheckConfiguration(ctx, mobileDevicePluginID) != nil {
		return errors.New("device_plugin_required")
	}
	capabilities, err := store.Capabilities(ctx, mobileDevicePluginID)
	if err != nil {
		return errors.New("device_plugin_required")
	}
	ready := map[string]bool{}
	for _, cap := range capabilities {
		if cap.Implementation == "ready" && cap.Discoverable && !cap.Async && reflect.DeepEqual(cap.Permissions, []string{"device"}) {
			ready[cap.ID] = true
		}
	}
	for _, capability := range mobileDeviceCapabilities {
		if !ready[capability] {
			return errors.New("device_plugin_required")
		}
	}
	account := m.native.Account()
	invocation := pluginhost.Invocation{Account: account, Thread: "host:mobile", Requester: "administrator", Purpose: "open mobile device broker", RequestKey: "mobile-device-ready"}
	if pluginhost.Authorize(installed.Package, pluginhost.Grant{Account: account, Plugin: installed.Package.ID, Version: installed.Package.Version, Permissions: installed.Permissions}, invocation, []string{"device"}) != nil {
		return errors.New("device_plugin_required")
	}
	controller, runtimeReady, err := m.installedPluginRuntime()
	if err != nil || !runtimeReady {
		return errors.New("device_plugin_required")
	}
	if state := controller.Status(mobileDevicePluginID).State; state != "stopped" && state != "running" {
		return errors.New("device_plugin_required")
	}
	deployment, err := runtimeDeploymentOptions()
	if err != nil || !mobileDeviceDeploymentPath(deployment.Python, false) || !mobileDeviceDeploymentPath(deployment.CatalogDirectory, true) {
		return errors.New("device_plugin_deployment_unavailable")
	}
	for _, root := range deployment.DeviceToolsRoots {
		if mobileDeviceDeploymentPath(root, true) {
			return nil
		}
	}
	return errors.New("device_plugin_tools_unregistered")
}

func mobileDeviceDeploymentPath(path string, directory bool) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir() == directory
}

// mobileDevicePluginBridge keeps raw native bridge replies only in the host.
// One bridge is opened for a durable task or narrow management listing and is
// closed by mobile.Control; the isolated plugin process remains controller-
// owned and is never closed per task.
type mobileDevicePluginBridge struct {
	manager      *Manager
	store        *pluginhost.Store
	controller   *pluginruntime.Controller
	installed    pluginhost.InstalledPackage
	task         mobile.Task
	settings     mobile.Settings
	packageDir   string
	options      pluginruntime.Options
	stateDir     string
	brokerOpener mobileDeviceBrokerOpener
	// Host-only injection. The existing ADB bridge has not been migrated to the
	// mandatory isolated protocol; no production registration is inferred from
	// a profile, package presence, fake opener, or deployment command.
	nativeRegistration *devicebroker.RuntimeRegistration

	mu           sync.Mutex
	broker       devicebroker.Bridge
	closed       bool
	sequence     uint64
	observations map[string]mobile.Observation
	artifacts    map[string]mobile.Artifact
}

// newMobilePluginBridge is passed to Control.SetBrokerFactory by the manager
// wiring. It has no legacy fallback: an unavailable v0.3 device package means
// the task cannot be sent to any native bridge.
func (m *Manager) newMobilePluginBridge(ctx context.Context, task mobile.Task, settings mobile.Settings) (mobile.Bridge, error) {
	if err := m.mobileDeviceReady(ctx); err != nil {
		return nil, err
	}
	store, err := m.packageStore()
	if err != nil {
		return nil, errors.New("device_plugin_required")
	}
	installed, err := store.Installed(ctx, mobileDevicePluginID)
	if err != nil || !installed.Enabled || installed.Removed {
		return nil, errors.New("device_plugin_required")
	}
	if err = m.mobileDeviceCurrent(task, settings); err != nil {
		return nil, err
	}
	controller, ready, err := m.installedPluginRuntime()
	if err != nil || !ready {
		return nil, errors.New("device_plugin_required")
	}
	deployment, err := runtimeDeploymentOptions()
	if err != nil || deployment.Python == "" || deployment.CatalogDirectory == "" {
		return nil, errors.New("device_plugin_deployment_unavailable")
	}
	packageDir, err := store.PackageDirectory(installed.Digest)
	if err != nil {
		return nil, errors.New("device_plugin_required")
	}
	// The existing mobile directory is the only compatible private state root:
	// legacy --storage remains usable when it is already beneath this directory.
	// An external ledger is not copied or silently widened; devicebroker reports
	// it as a migration requirement instead.
	stateDir := filepath.Join(m.dir, "mobile")
	if err = os.MkdirAll(stateDir, 0700); err != nil {
		return nil, errors.New("device_plugin_state_unavailable")
	}
	return &mobileDevicePluginBridge{
		manager: m, store: store, controller: controller, installed: installed,
		task: task, settings: settings, packageDir: packageDir, options: deployment,
		stateDir: stateDir, brokerOpener: openMobileDeviceBroker, nativeRegistration: m.loadMobileNativeRegistration(),
		observations: map[string]mobile.Observation{}, artifacts: map[string]mobile.Artifact{},
	}, nil
}

func (m *Manager) mobileDeviceCurrent(task mobile.Task, settings mobile.Settings) error {
	if m == nil || m.loadMobileNativeRegistration().Validate(context.Background(), m.native.Account()) != nil {
		return errors.New("device_broker_runtime_registration_required")
	}
	if m == nil || m.mobile == nil || !settings.Enabled {
		return errors.New("device_plugin_authorization_changed")
	}
	current := m.mobile.Settings()
	if current.Revision != settings.Revision || !current.Enabled || !reflect.DeepEqual(nonNilMobileSlice(current.Devices), nonNilMobileSlice(settings.Devices)) || !reflect.DeepEqual(nonNilMobileSlice(current.Actions), nonNilMobileSlice(settings.Actions)) {
		return errors.New("device_plugin_settings_changed")
	}
	if task.ID == "" {
		if !task.Source.Admin || task.Source.Account != "" || task.Source.Thread != "" || task.Source.Sender != "" {
			return errors.New("device_plugin_authorization_changed")
		}
		return nil
	}
	currentTask, ok := m.mobile.Get(task.ID)
	if !ok || (currentTask.State != "queued" && currentTask.State != "running") || currentTask.ID != task.ID || currentTask.RequestHash != task.RequestHash || currentTask.DeviceID != task.DeviceID || currentTask.Goal != task.Goal || currentTask.Steps != task.Steps || currentTask.ObservationID != task.ObservationID || !reflect.DeepEqual(currentTask.Source, task.Source) || !m.mobileSourceAllowed(currentTask.Source) {
		return errors.New("device_plugin_authorization_changed")
	}
	return nil
}

func (b *mobileDevicePluginBridge) Close() error {
	b.mu.Lock()
	// Closing permanently rejects new calls, but a failed native Close must
	// retain the broker reference so an explicit later Close can retry and so a
	// physical-device lease is not released merely by garbage collection.
	b.closed = true
	broker := b.broker
	b.mu.Unlock()
	if broker == nil {
		return nil
	}
	if err := broker.Close(); err != nil {
		return err
	}
	b.mu.Lock()
	if b.broker == broker {
		b.broker = nil
	}
	b.mu.Unlock()
	return nil
}

func (b *mobileDevicePluginBridge) Call(ctx context.Context, name string, request json.RawMessage) (string, error) {
	policy, err := b.newPolicy(ctx, name, request)
	if err != nil {
		return "", err
	}
	if err = b.mobilePluginCurrent(ctx, policy.task, policy.settings, policy.boundBinding, policy.invocation, policy.capability); err != nil {
		return "", err
	}
	if b.controller.Status(mobileDevicePluginID).State == "stopped" {
		if err = b.controller.Start(ctx, mobileDevicePluginID, b.installed.Revision); err != nil {
			return "", errors.New("device_plugin_required")
		}
		// Start records an enabled-runtime reservation and can advance the
		// installed revision. Refresh the immutable package snapshot before the
		// first policy Check; otherwise the just-started legitimate callback
		// would look like an unauthorized package replacement.
		current, readErr := b.store.Installed(ctx, mobileDevicePluginID)
		if readErr != nil || !current.Enabled || current.Removed || current.Digest != b.installed.Digest || current.Package.Version != b.installed.Package.Version || !reflect.DeepEqual(current.Permissions, b.installed.Permissions) {
			return "", errors.New("device_plugin_version_changed")
		}
		b.mu.Lock()
		b.installed = current
		b.mu.Unlock()
	}
	call, err := b.controller.Invoke(ctx, pluginruntime.CallRequest{Plugin: mobileDevicePluginID, Capability: policy.capability, Context: policy.invocation, Arguments: policy.arguments}, policy)
	if err != nil || call.Job.State != "completed" {
		return "", errors.New("device_plugin_needs_review")
	}
	private, err := b.store.PrivateJobResult(ctx, call.Job, func(ctx context.Context, job pluginhost.Job) error {
		return policy.Check(ctx, job, pluginhost.Capability{ID: policy.capability, Permissions: []string{"device"}})
	})
	if err != nil {
		return "", errors.New("device_plugin_result_unavailable")
	}
	var result struct {
		Raw           json.RawMessage `json:"raw"`
		SourceVersion string          `json:"source_version"`
		Operation     string          `json:"operation"`
	}
	if mobilePlanStrictJSON(private, &result) != nil || result.SourceVersion != policy.sourceVersion || result.Operation != policy.capability || !json.Valid(result.Raw) {
		return "", errors.New("device_plugin_result_unavailable")
	}
	return string(result.Raw), nil
}

func (b *mobileDevicePluginBridge) newPolicy(ctx context.Context, name string, raw json.RawMessage) (*mobileDevicePluginPolicy, error) {
	if !json.Valid(raw) {
		return nil, errors.New("device_request_invalid")
	}
	var request map[string]any
	if mobilePlanStrictJSON(raw, &request) != nil || request == nil {
		return nil, errors.New("device_request_invalid")
	}
	capability, err := mobileDeviceCapability(name, request)
	if err != nil {
		return nil, err
	}
	// Control advances the durable task between observations and actions. Read
	// that current task for this *new* operation before taking its immutable
	// snapshot; already-created policies keep their own prior snapshot.
	if b.task.ID != "" {
		current, ok := b.manager.mobile.Get(b.task.ID)
		if !ok || (current.State != "queued" && current.State != "running") {
			return nil, errors.New("device_plugin_authorization_changed")
		}
		b.mu.Lock()
		b.task = current
		b.mu.Unlock()
	}
	if err = b.manager.mobileDeviceCurrent(b.task, b.settings); err != nil {
		return nil, err
	}
	// Freeze the exact task and settings after the live authorization check. A
	// later policy must not make this policy follow a newer observation/step.
	task := cloneMobileTask(b.task)
	settings := cloneMobileSettings(b.settings)
	if err = b.validateNativeRequest(task, settings, capability, request); err != nil {
		return nil, err
	}
	broker, err := b.ensureBroker(ctx, task, settings)
	if err != nil {
		return nil, err
	}
	binding, err := broker.Binding()
	if err != nil || binding == "" {
		return nil, errors.New("device_plugin_binding_changed")
	}
	if err = b.manager.mobileDeviceCurrent(task, settings); err != nil {
		return nil, err
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, errors.New("device_plugin_closed")
	}
	sequence := b.sequence
	if capability != "device.act" && capability != "device.record" {
		b.sequence++
	} else {
		// The caller-supplied task request_key is the durable idempotency
		// identity for an action. A later observation or artifact read must not
		// turn the same action into a new native submission.
		sequence = 0
	}
	b.mu.Unlock()
	version := b.sourceVersion(task, settings, binding, capability, raw, sequence)
	thread, sender := task.Source.Thread, task.Source.Sender
	if thread == "" {
		thread = "host:mobile"
	}
	if sender == "" {
		sender = "administrator"
	}
	invocation := pluginhost.Invocation{Account: b.manager.native.Account(), Thread: thread, Requester: sender, Purpose: task.Goal, RequestKey: "mobile-device-" + agentID(task.ID, capability, task.RequestHash, version), SourceVersions: map[string]int{task.ID: task.Steps + 1}, Authorization: version}
	if task.ID == "" {
		invocation.SourceVersions = map[string]int{}
	}
	args, _ := json.Marshal(map[string]any{"source_version": version, "request": request})
	policy := &mobileDevicePluginPolicy{bridge: b, task: task, settings: settings, boundBinding: binding, capability: capability, sourceVersion: version, invocation: invocation, arguments: args, nativeName: name, request: request}
	return policy, nil
}

func mobileDeviceCapability(name string, request map[string]any) (string, error) {
	switch name {
	case "devices":
		if len(request) != 0 {
			return "", errors.New("device_request_invalid")
		}
		return "device.list", nil
	case "observe":
		return "device.observe", nil
	case "act":
		action, _ := request["action"].(string)
		if action == "capture" {
			return "device.record", nil
		}
		return "device.act", nil
	case "read_artifact":
		return "device.read_artifact", nil
	default:
		return "", errors.New("device_request_invalid")
	}
}

func (b *mobileDevicePluginBridge) sourceVersion(task mobile.Task, settings mobile.Settings, binding, capability string, raw json.RawMessage, sequence uint64) string {
	payload, _ := json.Marshal([]any{task.ID, task.Source, task.RequestHash, task.Goal, task.DeviceID, task.Steps, task.ObservationID, settings.Revision, b.installed.Digest, binding, capability, raw, sequence})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func cloneMobileTask(value mobile.Task) mobile.Task {
	clone := value
	clone.Artifacts = append([]mobile.Artifact(nil), value.Artifacts...)
	clone.Events = append([]mobile.Event(nil), value.Events...)
	return clone
}

func cloneMobileSettings(value mobile.Settings) mobile.Settings {
	clone := value
	clone.Devices = append([]string(nil), value.Devices...)
	clone.Actions = append([]string(nil), value.Actions...)
	clone.Bridge.Args = append([]string(nil), value.Bridge.Args...)
	clone.Bridge.Tools = append([]string(nil), value.Bridge.Tools...)
	clone.Bridge.Env = make(map[string]string, len(value.Bridge.Env))
	for key, item := range value.Bridge.Env {
		clone.Bridge.Env[key] = item
	}
	clone.Bridge.Headers = make(map[string]string, len(value.Bridge.Headers))
	for key, item := range value.Bridge.Headers {
		clone.Bridge.Headers[key] = item
	}
	return clone
}

func (b *mobileDevicePluginBridge) validateNativeRequest(task mobile.Task, settings mobile.Settings, capability string, request map[string]any) error {
	if capability == "device.list" {
		return nil
	}
	if task.ID == "" {
		return errors.New("device_task_required")
	}
	device, _ := request["device_id"].(string)
	if device == "" || device != task.DeviceID {
		return errors.New("device_request_invalid")
	}
	if capability == "device.observe" {
		return nil
	}
	if capability == "device.read_artifact" {
		id, _ := request["artifact_id"].(string)
		key, _ := request["request_key"].(string)
		b.mu.Lock()
		artifact, ok := b.artifacts[id]
		b.mu.Unlock()
		if !ok || artifact.DeviceID != device || artifact.RequestKey != key || !strings.HasPrefix(key, task.ID+"-step-") {
			return errors.New("device_artifact_not_authorized")
		}
		return nil
	}
	observationID, _ := request["observation_id"].(string)
	key, _ := request["request_key"].(string)
	if observationID == "" || observationID != task.ObservationID || !strings.HasPrefix(key, task.ID+"-step-") {
		return errors.New("device_request_invalid")
	}
	b.mu.Lock()
	observation, ok := b.observations[observationID]
	b.mu.Unlock()
	if !ok {
		return errors.New("device_observation_not_authorized")
	}
	step, ok := deviceRequestStep(request)
	if !ok || !mobile.ValidStep(step, observation) || (step.Action != "capture" && !contains(settings.Actions, step.Action)) {
		return errors.New("device_action_not_permitted")
	}
	return nil
}

func deviceRequestStep(request map[string]any) (mobile.Step, bool) {
	action, ok := request["action"].(string)
	if !ok {
		return mobile.Step{}, false
	}
	args := map[string]any{}
	for key, value := range request {
		switch key {
		case "device_id", "observation_id", "request_key", "action":
		default:
			args[key] = value
		}
	}
	return mobile.Step{Action: action, Arguments: args}, true
}

func (b *mobileDevicePluginBridge) mobilePluginCurrent(ctx context.Context, task mobile.Task, settings mobile.Settings, binding string, invocation pluginhost.Invocation, capability string) error {
	if b.nativeRegistration == nil || b.nativeRegistration != b.manager.loadMobileNativeRegistration() {
		return errors.New("device_broker_runtime_registration_changed")
	}
	if err := b.manager.mobileDeviceCurrent(task, settings); err != nil {
		return err
	}
	installed, err := b.store.Installed(ctx, mobileDevicePluginID)
	if err != nil || !installed.Enabled || installed.Removed || installed.Digest != b.installed.Digest || installed.Revision != b.installed.Revision {
		return errors.New("device_plugin_version_changed")
	}
	if b.store.CheckConfiguration(ctx, mobileDevicePluginID) != nil {
		return errors.New("device_plugin_configuration_changed")
	}
	if pluginhost.Authorize(installed.Package, pluginhost.Grant{Account: b.manager.native.Account(), Plugin: installed.Package.ID, Version: installed.Package.Version, Permissions: installed.Permissions}, invocation, []string{"device"}) != nil {
		return errors.New("device_plugin_permissions_changed")
	}
	if err := b.bindingCurrent(binding); err != nil {
		return err
	}
	return nil
}

func (b *mobileDevicePluginBridge) bindingCurrent(want string) error {
	b.mu.Lock()
	broker := b.broker
	b.mu.Unlock()
	if broker == nil {
		return errors.New("device_plugin_binding_changed")
	}
	got, err := broker.Binding()
	if err != nil || got == "" || got != want {
		return errors.New("device_plugin_binding_changed")
	}
	return nil
}

func (b *mobileDevicePluginBridge) ensureBroker(ctx context.Context, task mobile.Task, settings mobile.Settings) (devicebroker.Bridge, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, errors.New("device_plugin_closed")
	}
	if b.broker != nil {
		return b.broker, nil
	}
	if err := b.manager.mobileDeviceCurrent(task, settings); err != nil {
		return nil, err
	}
	workspace := filepath.Dir(b.options.CatalogDirectory)
	leaseDir := filepath.Join(workspace, ".device-broker-leases")
	// This deployment-only list is not copied into LaunchSpec/RuntimeRoots, so
	// the isolated package never receives an ADB path or a broader mount.
	adbRoots := append([]string{}, b.options.DeviceToolsRoots...)
	options := devicebroker.Options{
		Registration: b.nativeRegistration,
		Python:       b.options.Python, PackageDirectory: b.packageDir,
		// CatalogDirectory is <workspace>/plugins. The old profile can only
		// reference this fixed deployment compatibility entry; accepting an
		// arbitrary Legacy.Args script would silently re-authorize a different
		// native program during migration.
		CompatibilityEntry: filepath.Join(workspace, "scripts", "mobile_bridge.py"),
		WorkspaceDirectory: workspace, StateDirectory: b.stateDir,
		GlobalLeaseDirectory: leaseDir, ADBRoots: adbRoots, Account: b.manager.native.Account(),
		Legacy: settings.Bridge, Devices: nonNilMobileSlice(settings.Devices), Actions: nonNilMobileSlice(settings.Actions),
	}
	broker, err := b.brokerOpener(ctx, options)
	if err != nil {
		return nil, errors.New("device_plugin_broker_unavailable")
	}
	b.broker = broker
	return broker, nil
}

type mobileDevicePluginPolicy struct {
	bridge        *mobileDevicePluginBridge
	task          mobile.Task
	settings      mobile.Settings
	boundBinding  string
	capability    string
	sourceVersion string
	invocation    pluginhost.Invocation
	arguments     json.RawMessage
	nativeName    string
	request       map[string]any

	mu      sync.Mutex
	used    bool
	receipt json.RawMessage
	raw     json.RawMessage
	status  string
}

func (p *mobileDevicePluginPolicy) Check(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability) (err error) {
	if p == nil || p.bridge == nil || cap.ID != p.capability || job.Plugin != mobileDevicePluginID || job.Tool != p.capability || !bytes.Equal(job.Arguments, p.arguments) {
		return errors.New("device_plugin_authorization_changed")
	}
	if want, got := mustJSON(p.invocation), mustJSON(job.Context); !bytes.Equal(want, got) || job.Context.Authorization != p.sourceVersion {
		return errors.New("device_plugin_authorization_changed")
	}
	if err := p.bridge.mobilePluginCurrent(ctx, p.task, p.settings, p.boundBinding, p.invocation, p.capability); err != nil {
		return err
	}
	if p.task.ID != "" && job.Context.SourceVersions[p.task.ID] != p.task.Steps+1 {
		return errors.New("device_plugin_source_changed")
	}
	if job.ID != "" {
		live, err := p.bridge.store.Job(ctx, job.ID)
		if err != nil || live.CancelRequested || live.Revision != job.Revision {
			return errors.New("device_plugin_job_changed")
		}
	}
	return nil
}

func (p *mobileDevicePluginPolicy) Service(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability, method string, raw json.RawMessage) (json.RawMessage, error) {
	p.mu.Lock()
	if p.used || method != "wechat/device_request" {
		p.mu.Unlock()
		return nil, errors.New("device_plugin_host_service_unavailable")
	}
	if err := p.Check(ctx, job, cap); err != nil {
		p.mu.Unlock()
		return nil, err
	}
	var in struct {
		Operation     string         `json:"operation"`
		SourceVersion string         `json:"source_version"`
		Request       map[string]any `json:"request"`
	}
	if mobilePlanStrictJSON(raw, &in) != nil || in.Operation != p.capability || in.SourceVersion != p.sourceVersion || !reflect.DeepEqual(in.Request, p.request) {
		p.mu.Unlock()
		return nil, errors.New("device_plugin_request_changed")
	}
	p.used = true // Unknown native outcomes must never be replayed.
	p.mu.Unlock()
	broker, err := p.bridge.ensureBroker(ctx, p.task, p.settings)
	if err != nil {
		return nil, err
	}
	// Opening/reusing a broker can block on a protected profile. Re-check the
	// immutable task snapshot and seal immediately before the native side effect.
	if err = p.Check(ctx, job, cap); err != nil {
		return nil, err
	}
	rawText, err := broker.Call(ctx, p.nativeName, mustJSON(p.request))
	if err != nil || !json.Valid([]byte(rawText)) {
		return nil, errors.New("device_plugin_broker_unavailable")
	}
	receipt, err := p.bridge.safeReceipt(p.task, p.capability, p.nativeName, json.RawMessage(rawText), p.request)
	if err != nil {
		return nil, err
	}
	if err = p.Check(ctx, job, cap); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.raw, p.receipt = append(json.RawMessage(nil), rawText...), receipt
	p.status = mobileReceiptStatus(p.capability, receipt)
	p.mu.Unlock()
	return json.Marshal(map[string]any{"receipt": json.RawMessage(receipt), "source_version": p.sourceVersion, "operation": p.capability})
}

func (p *mobileDevicePluginPolicy) Validate(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	if err := p.Check(ctx, job, cap); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var out struct {
		Receipt       json.RawMessage `json:"receipt"`
		SourceVersion string          `json:"source_version"`
		Operation     string          `json:"operation"`
	}
	if !p.used || len(p.receipt) == 0 || len(p.raw) == 0 || mobilePlanStrictJSON(raw, &out) != nil || out.SourceVersion != p.sourceVersion || out.Operation != p.capability || !sameJSON(out.Receipt, p.receipt) {
		return pluginruntime.VerifiedResult{}, errors.New("device_plugin_result_unverified")
	}
	private, _ := json.Marshal(map[string]any{"raw": json.RawMessage(p.raw), "source_version": p.sourceVersion, "operation": p.capability})
	status := p.status
	if status == "" {
		status = "completed"
	}
	public, _ := json.Marshal(map[string]any{"operation": p.capability, "status": status, "preview": json.RawMessage(p.receipt)})
	return pluginruntime.VerifiedResult{Result: public, Private: private}, nil
}

func mobileReceiptStatus(capability string, receipt json.RawMessage) string {
	if capability != "device.act" && capability != "device.record" {
		return "completed"
	}
	var value struct {
		State string `json:"state"`
	}
	if json.Unmarshal(receipt, &value) != nil || (value.State != "submitted" && value.State != "unknown") {
		return ""
	}
	return value.State
}

func sameJSON(left, right json.RawMessage) bool {
	a, err := decodeShareJSON(left)
	if err != nil {
		return false
	}
	b, err := decodeShareJSON(right)
	return err == nil && reflect.DeepEqual(a, b)
}

func (b *mobileDevicePluginBridge) safeReceipt(task mobile.Task, capability, name string, raw json.RawMessage, request map[string]any) (json.RawMessage, error) {
	switch capability {
	case "device.list":
		var value struct {
			Devices []struct {
				ID       string   `json:"id"`
				Name     string   `json:"name"`
				State    string   `json:"state"`
				Packages []string `json:"packages"`
				Actions  []string `json:"actions"`
				Browsers []string `json:"browsers"`
				WebHosts []string `json:"web_hosts"`
			} `json:"devices"`
		}
		if mobileDeviceWireJSON(raw, &value) != nil {
			return nil, errors.New("device_plugin_receipt_invalid")
		}
		return json.Marshal(value)
	case "device.observe":
		var value struct {
			Text  string `json:"text"`
			Media []struct {
				MIME string `json:"mime_type"`
				Data string `json:"data_base64"`
			} `json:"media"`
		}
		var observation struct {
			ID              string            `json:"id"`
			DeviceID        string            `json:"device_id"`
			Package         string            `json:"package"`
			Width           int               `json:"width"`
			Height          int               `json:"height"`
			AllowedPackages []string          `json:"allowed_packages"`
			Browsers        []string          `json:"browsers"`
			WebHosts        []string          `json:"web_hosts"`
			Nodes           []json.RawMessage `json:"nodes"`
		}
		if mobileDeviceWireJSON(raw, &value) != nil || mobileDeviceWireJSON([]byte(value.Text), &observation) != nil || len(value.Media) != 1 || value.Media[0].MIME != "image/png" || observation.ID == "" || observation.DeviceID != task.DeviceID || observation.Width < 1 || observation.Height < 1 {
			return nil, errors.New("device_plugin_receipt_invalid")
		}
		verified := mobile.Observation{ID: observation.ID, DeviceID: observation.DeviceID, Package: observation.Package, Width: observation.Width, Height: observation.Height, AllowedPackages: nonNilMobileSlice(observation.AllowedPackages), Browsers: nonNilMobileSlice(observation.Browsers), WebHosts: nonNilMobileSlice(observation.WebHosts), Nodes: nonNilMobileSlice(observation.Nodes)}
		b.mu.Lock()
		b.observations[verified.ID] = verified
		b.mu.Unlock()
		return json.Marshal(map[string]any{"observation_id": verified.ID, "device_id": verified.DeviceID, "package": verified.Package, "width": verified.Width, "height": verified.Height, "ui_nodes": verified.Nodes})
	case "device.act", "device.record":
		var value struct {
			State  string `json:"state"`
			Result struct {
				Action    string `json:"action"`
				DeviceID  string `json:"device_id"`
				Reobserve bool   `json:"reobserve_required"`
				Artifact  *struct {
					ID               string  `json:"id"`
					DeviceID         string  `json:"device_id"`
					RequestKey       string  `json:"request_key"`
					Kind             string  `json:"kind"`
					MIME             string  `json:"mime"`
					Bytes            int     `json:"bytes"`
					SHA256           string  `json:"sha256"`
					RequestedSeconds int     `json:"requested_seconds"`
					Audio            bool    `json:"audio"`
					Original         bool    `json:"original_media"`
					CreatedAt        float64 `json:"created_at"`
				} `json:"artifact"`
			} `json:"result"`
		}
		if mobileDeviceWireJSON(raw, &value) != nil || (value.State != "submitted" && value.State != "unknown") || value.State == "submitted" && value.Result.DeviceID != task.DeviceID || value.State == "unknown" && value.Result.DeviceID != "" && value.Result.DeviceID != task.DeviceID {
			return nil, errors.New("device_plugin_receipt_invalid")
		}
		action, _ := request["action"].(string)
		if value.State == "submitted" && (value.Result.Action != action || (capability == "device.record" && action != "capture")) {
			return nil, errors.New("device_plugin_receipt_invalid")
		}
		var artifact *mobile.Artifact
		if value.State == "unknown" && value.Result.Artifact != nil {
			return nil, errors.New("device_plugin_receipt_invalid")
		}
		if value.Result.Artifact != nil {
			item := value.Result.Artifact
			converted := mobile.Artifact{ID: item.ID, DeviceID: item.DeviceID, RequestKey: item.RequestKey, Kind: item.Kind, MIME: item.MIME, Bytes: item.Bytes, SHA256: item.SHA256, RequestedSeconds: item.RequestedSeconds, Audio: item.Audio, Original: item.Original}
			key, _ := request["request_key"].(string)
			if converted.ID == "" || converted.DeviceID != task.DeviceID || converted.RequestKey != key {
				return nil, errors.New("device_plugin_receipt_invalid")
			}
			b.mu.Lock()
			b.artifacts[converted.ID] = converted
			b.mu.Unlock()
			artifact = &converted
		}
		return json.Marshal(map[string]any{"state": value.State, "action": value.Result.Action, "device_id": value.Result.DeviceID, "reobserve_required": value.Result.Reobserve, "artifact": artifact})
	case "device.read_artifact":
		var value struct {
			ID     string `json:"id"`
			Offset int    `json:"offset"`
			Bytes  int    `json:"bytes"`
			SHA256 string `json:"sha256"`
			EOF    bool   `json:"eof"`
			Data   string `json:"data_base64"`
		}
		if mobileDeviceWireJSON(raw, &value) != nil || value.ID == "" {
			return nil, errors.New("device_plugin_receipt_invalid")
		}
		return json.Marshal(map[string]any{"artifact_id": value.ID, "offset": value.Offset, "bytes": value.Bytes, "sha256": value.SHA256, "eof": value.EOF})
	}
	return nil, errors.New("device_plugin_receipt_invalid")
}

// Native bridge payloads include host-private timestamps, screenshot hashes,
// and base64 data. Verify unique JSON keys, then project only receipt fields.
func mobileDeviceWireJSON(raw []byte, out any) error {
	if _, err := decodeShareJSON(raw); err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

var _ mobile.Bridge = (*mobileDevicePluginBridge)(nil)
var _ pluginruntime.CallPolicy = (*mobileDevicePluginPolicy)(nil)
var _ pluginruntime.ServicePolicy = (*mobileDevicePluginPolicy)(nil)
