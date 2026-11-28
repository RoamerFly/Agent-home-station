package stationbrowser

import "errors"

// These fixed causes contain no creator account, request URL or response body.
// A response that was seen but could not be read or parsed is distinct from
// the absence of a usable work-list response.
var (
	ErrCreatorWorksListNotObserved    = errors.New("创作者作品列表响应未观察到")
	ErrCreatorWorksResponseUnverified = errors.New("创作者作品列表响应无法核对")
)
