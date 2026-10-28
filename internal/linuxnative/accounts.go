package linuxnative

import (
	"context"
	"errors"
	"wechat-bot/internal/identity"
)

// Accounts returns public account metadata only, never login/session material.
type AccountInfo struct {
	ID            string `json:"id"`
	Alias         string `json:"alias"`
	Authenticated bool   `json:"authenticated"`
	Connection    string `json:"connection"`
}

func (s *Sender) Accounts(ctx context.Context) ([]AccountInfo, error) {
	var page struct {
		Items []AccountInfo `json:"items"`
	}
	if err := s.readJSON(ctx, "/v1/accounts", &page); err != nil {
		return nil, err
	}
	for _, a := range page.Items {
		if !safeID(a.ID) || a.Alias != "" && !safeID(a.Alias) {
			return nil, errors.New("invalid native account identity")
		}
	}
	return page.Items, nil
}

// ForAccount shares only the immutable transport configuration. Every account
// owns its own identity/name cache and all upstream paths remain account scoped.
func (s *Sender) ForAccount(id string) (*Sender, error) {
	if !safeID(id) {
		return nil, errors.New("invalid native account identity")
	}
	return &Sender{base: s.base, token: s.token, account: id, pollInterval: s.pollInterval, readyTimeout: s.readyTimeout, client: s.client}, nil
}

func (s *Sender) DisplayName(ctx context.Context) (string, error) {
	name, err := s.cachedName(ctx, "self", func() (identity.Name, error) {
		// The native status deliberately omits credentials and profile internals.
		// An outgoing file-helper message proves its sender is this login's self ID.
		page, err := s.Catalog(ctx, "messages", "filehelper", "", "")
		if err != nil {
			return identity.Name{}, err
		}
		for _, row := range page.Items {
			direction, _ := row["direction"].(string)
			sender, _ := row["senderId"].(string)
			if direction != "outgoing" || row["threadId"] != "filehelper" || !safeID(sender) {
				continue
			}
			n, e := s.ResolveSpeaker(ctx, "filehelper", sender)
			if e == nil && identity.Valid(n) {
				return n, nil
			}
		}
		return identity.Name{}, errors.New("self nickname not yet synchronized")
	})
	return name.Text, err
}
