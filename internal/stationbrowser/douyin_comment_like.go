package stationbrowser

import (
	"context"
	"strings"
)

// DouyinCommentLikeRequest identifies one rendered comment on one of the
// account's works. The caller must independently verify ownership and keep a
// durable submit ledger; this browser adapter cannot establish either fact.
type DouyinCommentLikeRequest struct {
	VideoID          string
	CommentID        string
	ExpectedText     string
	ExpectedAuthorID string // Rendered sec_uid, as returned by ReadDouyinComments.
	Submitted        bool   // Reconcile only; never click again.
	BeforeSubmit     func(context.Context) error
}

type DouyinCommentLikeResult struct {
	State     string // unknown, applied, failed, cancelled
	Code      string
	Submitted bool
}

func validDouyinCommentLike(req DouyinCommentLikeRequest) bool {
	if !douyinInteractionVideoID.MatchString(req.VideoID) || !douyinCommentID.MatchString(req.CommentID) ||
		!douyinAuthorID.MatchString(req.ExpectedAuthorID) ||
		strings.TrimSpace(req.ExpectedText) == "" || len(req.ExpectedText) > 8192 {
		return false
	}
	if req.Submitted {
		return req.BeforeSubmit == nil
	}
	return req.BeforeSubmit != nil
}
