package linuxnative

import (
	"context"
	"errors"
	"net/url"
)

// ChannelsSource is private transfer data, never model context or log content.
type ChannelsSource struct {
	State         string `json:"state"`
	MessageID     string `json:"messageId"`
	AccountID     string `json:"accountId"`
	ThreadID      string `json:"threadId"`
	MediaURL      string `json:"mediaUrl"`
	DecodeKey     string `json:"decodeKey"`
	ObjectID      string `json:"objectId"`
	ObjectNonceID string `json:"objectNonceId"`
	Reason        string `json:"reason"`
}

func (s *Sender) ChannelsMedia(ctx context.Context, id string) (ChannelsSource, error) {
	var value ChannelsSource
	if id == "" || len(id) > 8192 {
		return value, errors.New("invalid channels message")
	}
	err := s.readJSON(ctx, s.accountPath()+"/messages/"+url.PathEscape(id)+"/channels-media", &value)
	if err != nil {
		return ChannelsSource{}, errors.New("channels_source_unavailable")
	}
	if value.MessageID != id || value.AccountID != s.account || value.ThreadID == "" {
		return ChannelsSource{}, errors.New("channels_source_scope_mismatch")
	}
	return value, nil
}
