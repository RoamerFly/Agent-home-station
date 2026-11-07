//go:build !windows

package station

import "wechat-bot/internal/apphost"

func stationLiveOutput(_, _ string, _ *localLivePrograms) (apphost.LiveOutput, string) {
	return nil, "unsupported"
}
