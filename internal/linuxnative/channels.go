package linuxnative

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// ChannelsSource is private transfer data, never model context or log content.
type ChannelsSource struct {
	Title         string `json:"title"`
	Author        string `json:"author"`
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

// ChannelsShareURL resolves only the server-owned received card. The native
// service validates its object identity; no arbitrary object ID is submitted.
func (s *Sender) ChannelsShareURL(ctx context.Context, id, thread, objectID string) (string, error) {
	if id == "" || len(id) > 8192 || thread == "" || objectID == "" {
		return "", errors.New("channels_native_share_scope_invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	b, err := s.toolJSON(ctx, http.MethodPost, s.accountPath()+"/messages/"+url.PathEscape(id)+"/channels-share-url", "", nil, "")
	if err != nil {
		var rejection *ToolRejection
		if errors.As(err, &rejection) {
			return "", errors.New(rejection.Code)
		}
		return "", errors.New("channels_native_share_unavailable")
	}
	var value struct {
		AccountID string `json:"accountId"`
		MessageID string `json:"messageId"`
		ThreadID  string `json:"threadId"`
		ObjectID  string `json:"objectId"`
		URL       string `json:"shareUrl"`
	}
	if json.Unmarshal(b, &value) != nil || value.AccountID != s.account || value.MessageID != id || value.ThreadID != thread || value.ObjectID != objectID {
		return "", errors.New("channels_native_share_scope_mismatch")
	}
	return value.URL, nil
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
