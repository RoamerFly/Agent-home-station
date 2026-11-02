package linuxnative

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
)

func (s *Sender) RetryMediaJob(ctx context.Context, id string) (ToolStatus, error) {
	if !safeID(id) {
		return ToolStatus{}, errors.New("invalid media job")
	}
	raw, err := s.toolJSON(ctx, "POST", s.accountPath()+"/jobs/"+url.PathEscape(id)+"/retry", "", bytes.NewReader([]byte("{}")), "application/json")
	if err != nil {
		return ToolStatus{}, err
	}
	return s.decodeToolStatus(raw)
}
func (s *Sender) CancelMediaJob(ctx context.Context, id string) (ToolStatus, error) {
	if !safeID(id) {
		return ToolStatus{}, errors.New("invalid media job")
	}
	raw, err := s.toolJSON(ctx, "POST", s.accountPath()+"/jobs/"+url.PathEscape(id)+"/cancel", "", bytes.NewReader([]byte("{}")), "application/json")
	if err != nil {
		return ToolStatus{}, err
	}
	return s.decodeToolStatus(raw)
}

// ResumeMissingMediaJob creates a new native voice job containing only the
// verified, unsubmitted suffix of a user-cancelled segmented job.  It never
// retries or mutates the old job; the caller must supply a fresh idempotency
// key for the explicit continuation request.
func (s *Sender) ResumeMissingMediaJob(ctx context.Context, id, key string) (ToolStatus, error) {
	if !safeID(id) || !safeID(key) || len(key) > 128 {
		return ToolStatus{}, errors.New("invalid media resume request")
	}
	body, err := json.Marshal(map[string]string{"idempotencyKey": key})
	if err != nil {
		return ToolStatus{}, err
	}
	raw, err := s.toolJSON(ctx, "POST", s.accountPath()+"/tools/media/"+url.PathEscape(id)+"/resume-missing", key, bytes.NewReader(body), "application/json")
	if err != nil {
		return ToolStatus{}, err
	}
	return s.decodeToolStatus(raw)
}
