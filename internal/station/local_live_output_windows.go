//go:build windows

package station

import (
	"context"
	"errors"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/obsportable"
)

func stationLiveOutput(stationData, workspace string) (apphost.LiveOutput, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	installation, err := obsportable.Open(ctx, stationData, workspace)
	if errors.Is(err, obsportable.ErrMissing) {
		return nil, "missing"
	}
	if err != nil {
		return nil, "invalid"
	}
	return apphost.NewManagedLiveOutput(func(ctx context.Context, _ apphost.LiveReference) (apphost.LiveEncoder, error) {
		return installation.Encoder(ctx)
	}), "ready"
}
