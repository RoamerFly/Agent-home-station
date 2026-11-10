// Package sending defines the boundary between the local API and a WeChat transport.
package sending

import (
	"context"
	"errors"
)

var (
	ErrNotReady       = errors.New("wechat transport is not ready")
	ErrRejected       = errors.New("wechat rejected the message")
	ErrOutcomeUnknown = errors.New("message outcome is unknown; do not retry automatically")
)

// OutcomeError preserves a safe native reason and intent identity without
// losing errors.Is semantics. Neither field contains remote response bodies.
type OutcomeError struct {
	Cause    error
	Reason   string
	IntentID string
}

func (e *OutcomeError) Error() string { return e.Cause.Error() }
func (e *OutcomeError) Unwrap() error { return e.Cause }
func FailureReason(err error) string {
	var outcome *OutcomeError
	if errors.As(err, &outcome) {
		return outcome.Reason
	}
	if errors.Is(err, ErrNotReady) {
		return "wechat_not_ready"
	}
	if errors.Is(err, ErrRejected) {
		return "wechat_rejected"
	}
	return "send_outcome_unknown"
}
func IntentID(err error) string {
	var outcome *OutcomeError
	if errors.As(err, &outcome) {
		return outcome.IntentID
	}
	return ""
}

type TextRequest struct {
	Recipient string `json:"recipient"`
	Text      string `json:"text"`
}

// Receipt must only be returned after a parsed and validated upstream acknowledgement.
// It means server acceptance; it does not claim that the recipient read the message.
type Receipt struct {
	Recipient       string `json:"recipient"`
	ClientMessageID string `json:"client_message_id"`
	ServerMessageID string `json:"server_message_id"`
}

type Engine interface {
	SendText(context.Context, TextRequest) (Receipt, error)
	Ready() bool
}

// MediaResumeRequest is a host-issued explicit operation, never model input.
// Its original key and new trace remain immutable across restarts.
type MediaResumeRequest struct {
	ID      string `json:"id"`
	Account string `json:"account"`
	Thread  string `json:"thread"`
	Key     string `json:"key"`
	Trace   string `json:"trace"`
}

type MediaResumeResult struct {
	State       string `json:"state"`
	Reason      string `json:"reason,omitempty"`
	DeliveryKey string `json:"delivery_key,omitempty"`
}
