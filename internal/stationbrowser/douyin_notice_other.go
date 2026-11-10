//go:build !windows

package stationbrowser

import "context"

func DouyinNoticeAccount(context.Context, string, func() error) (string, error) {
	return "", ErrBrowserUnavailable
}

func ReadDouyinNotice(context.Context, string, DouyinNoticeRequest, func() error) (DouyinNoticePage, error) {
	return DouyinNoticePage{}, ErrBrowserUnavailable
}
