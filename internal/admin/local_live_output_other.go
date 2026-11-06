//go:build !windows

package admin

import "wechat-bot/internal/apphost"

func stationLiveOutput(_, _ string) (apphost.LiveOutput, string) { return nil, "unsupported" }
