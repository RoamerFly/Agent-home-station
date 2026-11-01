package devicebroker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"

	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

// RuntimeEnvironment is trusted deployment state, never MCP Command/Args/Env.
// Registration grants no socket, ADB mount, registry capability or raw command.
type RuntimeEnvironment struct {
	Python, Pythonw, WindowsHelper string
	RuntimeRoots                   []string
}

// RuntimeRegistration has no JSON construction path or mutable exported fields.
// The current native bridge requiring direct ADB/CLI access is not this protocol.
type RuntimeRegistration struct {
	directory, digest string
	pack              pluginhost.Package
	grant             pluginhost.Grant
	environment       RuntimeEnvironment
	seals             []fileSeal
}

func NewRuntimeRegistration(ctx context.Context, directory string, grant pluginhost.Grant, environment RuntimeEnvironment) (*RuntimeRegistration, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	directory, err := canonicalDirectory(directory)
	if err != nil {
		return nil, ErrNeedsMigration
	}
	pack, err := pluginhost.Verify(directory)
	if err != nil || pack.Runtime.Transport != "mcp-stdio" || pack.Runtime.Entrypoint == "" || pack.Files[pack.Runtime.Entrypoint] == "" || !identifier.MatchString(grant.Account) {
		return nil, ErrNeedsMigration
	}
	device := false
	for _, permission := range grant.Permissions {
		device = device || permission == "device"
	}
	required := append(append([]string(nil), pack.Permissions...), "device")
	if !device || pluginhost.Authorize(pack, grant, pluginhost.Invocation{Account: grant.Account, Thread: "host:device-broker", Requester: "administrator", Purpose: "register fixed native broker", RequestKey: "registration"}, required) != nil {
		return nil, ErrNeedsMigration
	}
	raw, err := os.ReadFile(filepath.Join(directory, "plugin.json"))
	if err != nil {
		return nil, ErrNeedsMigration
	}
	sum := sha256.Sum256(raw)
	r := &RuntimeRegistration{directory: directory, digest: hex.EncodeToString(sum[:]), pack: pack, grant: grant, environment: environment}
	r.grant.Permissions = append([]string(nil), grant.Permissions...)
	r.environment.RuntimeRoots = append([]string(nil), environment.RuntimeRoots...)
	paths := []string{environment.Python}
	if environment.Pythonw != "" || environment.WindowsHelper != "" {
		paths = append(paths, environment.Pythonw, environment.WindowsHelper, filepath.Join(filepath.Dir(environment.WindowsHelper), "windows_lpac.py"))
	}
	for _, path := range paths {
		seal, err := makeSeal(path)
		if err != nil {
			return nil, ErrNeedsMigration
		}
		r.seals = append(r.seals, seal)
	}
	if len(environment.RuntimeRoots) == 0 {
		return nil, ErrNeedsMigration
	}
	for _, root := range environment.RuntimeRoots {
		if _, err := canonicalDirectory(root); err != nil {
			return nil, ErrNeedsMigration
		}
	}
	return r, nil
}

func (r *RuntimeRegistration) Validate(ctx context.Context, account string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil || r.grant.Account != account {
		return ErrNeedsMigration
	}
	for _, seal := range r.seals {
		if !seal.matches() {
			return errChanged
		}
	}
	pack, err := pluginhost.Verify(r.directory)
	if err != nil || !reflect.DeepEqual(pack, r.pack) {
		return errChanged
	}
	raw, err := os.ReadFile(filepath.Join(r.directory, "plugin.json"))
	if err != nil {
		return errChanged
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != r.digest {
		return errChanged
	}
	return nil
}

func (r *RuntimeRegistration) launcher(account, storage string, check func() bool) mcptools.StdioLauncher {
	return func(ctx context.Context, c mcptools.ServerConfig) (mcptools.StdioProcess, error) {
		if c.ID != "device" || c.PluginID != r.pack.ID || c.Command != "" || len(c.Args) != 0 || len(c.Env) != 0 || c.URL != "" || len(c.Headers) != 0 || !check() {
			return nil, errChanged
		}
		if err := r.Validate(ctx, account); err != nil {
			return nil, err
		}
		e := &pluginruntime.Executor{Resolve: func(ctx context.Context, p pluginhost.Package, g pluginhost.Grant) (pluginruntime.LaunchSpec, error) {
			if !reflect.DeepEqual(p, r.pack) || !reflect.DeepEqual(g, r.grant) {
				return pluginruntime.LaunchSpec{}, errors.New("device_broker_registration_changed")
			}
			if err := r.Validate(ctx, account); err != nil {
				return pluginruntime.LaunchSpec{}, err
			}
			return pluginruntime.LaunchSpec{PackageDir: r.directory, DataDir: storage, Python: r.environment.Python, Pythonw: r.environment.Pythonw, WindowsHelper: r.environment.WindowsHelper, RuntimeRoots: append([]string(nil), r.environment.RuntimeRoots...)}, nil
		}}
		stream, err := e.StartStream(ctx, r.pack, r.grant)
		if err != nil {
			return nil, err
		}
		if err = r.Validate(ctx, account); err != nil || !check() {
			_ = stream.Close()
			return nil, errChanged
		}
		return stream, nil
	}
}
