//go:build !windows

package stationbrowser

import "context"

func ReadDouyinCommentRelationTarget(context.Context, string, DouyinCommentRelationRequest, func() error) (DouyinCommentRelation, error) {
	return DouyinCommentRelation{}, ErrDouyinCommentsUnavailable
}
