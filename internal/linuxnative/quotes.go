package linuxnative

import (
	"context"
	"errors"
	"net/url"
)

type QuoteResolution struct {
	AccountID    string   `json:"accountId"`
	MessageID    string   `json:"messageId"`
	ThreadID     string   `json:"threadId"`
	State        string   `json:"state"`
	Message      *Message `json:"message"`
	EmbeddedText string   `json:"embeddedText,omitempty"`
}

func (s *Sender) QuotedMessage(ctx context.Context, id string) (QuoteResolution, error) {
	var out QuoteResolution
	if id == "" || len(id) > 8192 {
		return out, errors.New("invalid quote message")
	}
	err := s.readJSON(ctx, s.accountPath()+"/messages/"+url.PathEscape(id)+"/quote", &out)
	if err != nil {
		return out, err
	}
	if out.AccountID != s.account || out.MessageID != id || !safeThreadID(out.ThreadID) || out.Message != nil && (out.Message.ThreadID != out.ThreadID || out.Message.AccountID != s.account) {
		return QuoteResolution{}, errors.New("quote scope mismatch")
	}
	return out, nil
}
