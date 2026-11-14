package devicebroker

// Host is the narrow compatibility adapter used by legacy admin wiring while
// the mobile plugin talks directly to this package. It keeps the old
// devicespec.IOHost boundary source-compatible without moving authority or
// path validation back into the legacy package.

import (
	"context"
	"encoding/json"

	"wechat-bot/internal/devicespec"
)

type Host struct{}

func legacyOptions(o devicespec.IOOptions) Options {
	return Options{
		Python: o.Python, PackageDirectory: o.PackageDirectory, CompatibilityEntry: o.CompatibilityEntry,
		WorkspaceDirectory: o.WorkspaceDirectory, StateDirectory: o.StateDirectory,
		GlobalLeaseDirectory: o.GlobalLeaseDirectory, ADBRoots: append([]string(nil), o.ADBRoots...),
		Account: o.Account, Legacy: o.Legacy, Devices: append([]string(nil), o.Devices...), Actions: append([]string(nil), o.Actions...),
	}
}

func (Host) Inspect(ctx context.Context, o devicespec.IOOptions) error {
	return InspectIOOptions(ctx, legacyOptions(o))
}

func (Host) Grants(ctx context.Context, o devicespec.IOOptions) ([]devicespec.IOGrant, error) {
	grants, err := InspectIOGrant(ctx, legacyOptions(o))
	if err != nil {
		return nil, err
	}
	result := make([]devicespec.IOGrant, 0, len(grants))
	for _, grant := range grants {
		result = append(result, devicespec.IOGrant{DeviceID: grant.DeviceID, Packages: append([]string(nil), grant.Packages...), Actions: append([]string(nil), grant.Actions...)})
	}
	return result, nil
}

func (Host) Open(ctx context.Context, o devicespec.IOOptions) (devicespec.IOBroker, error) {
	broker, err := OpenIO(ctx, legacyOptions(o))
	if err != nil {
		return nil, err
	}
	return legacyBroker{inner: broker}, nil
}

func (Host) Cleanup() error { return CleanupQuarantined() }

type legacyBroker struct{ inner IOBroker }

func (b legacyBroker) Binding() (string, error) { return b.inner.Binding() }
func (b legacyBroker) Begin(ctx context.Context, name string, raw json.RawMessage) (devicespec.IOCall, error) {
	if err := devicespec.CheckIOAuthority(ctx); err != nil {
		return nil, err
	}
	call, err := b.inner.Begin(ctx, name, raw)
	if err != nil {
		return nil, err
	}
	return legacyCall{inner: call}, nil
}
func (b legacyBroker) Close() error { return b.inner.Close() }

type legacyCall struct{ inner IOCall }

func (c legacyCall) Service(ctx context.Context, step, device string, raw json.RawMessage) (json.RawMessage, error) {
	if err := devicespec.CheckIOAuthority(ctx); err != nil {
		return nil, err
	}
	return c.inner.Service(ctx, step, device, raw)
}
func (c legacyCall) Complete(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if err := devicespec.CheckIOAuthority(ctx); err != nil {
		return nil, err
	}
	return c.inner.Complete(ctx, raw)
}
func (c legacyCall) Close() error { return c.inner.Close() }
