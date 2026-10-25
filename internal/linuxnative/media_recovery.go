package linuxnative

import (
	"bytes"
	"context"
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
