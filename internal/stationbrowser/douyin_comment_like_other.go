//go:build !windows

package stationbrowser

import "context"

func RunDouyinCommentLike(context.Context, string, DouyinCommentLikeRequest, func() error) (DouyinCommentLikeResult, error) {
	return DouyinCommentLikeResult{State: DouyinInteractionUnknown}, ErrDouyinInteractionUnavailable
}
