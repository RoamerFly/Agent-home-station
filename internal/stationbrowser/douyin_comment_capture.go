package stationbrowser

import "errors"

// ErrDouyinCommentTargetPending means the one naturally emitted list response
// did not contain the notification's target CID. It says nothing about other
// pages and does not justify scanning the work for new notifications.
var ErrDouyinCommentTargetPending = errors.New("目标评论尚未在当前页面响应中核对")

// DouyinCommentRelationRequest is a targeted, read-only verification request
// for a comment already identified by a notification. RootID is empty for a
// main comment and is the exact main-comment ID for a child comment. It does
// not discover notifications by walking a work's comment section.
type DouyinCommentRelationRequest struct {
	WorkID   string
	RootID   string
	TargetID string
}
