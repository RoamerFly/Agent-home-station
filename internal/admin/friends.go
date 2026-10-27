package admin

import (
	"context"
	"errors"
	"strings"
	"time"
)

type friendEntry struct {
	Allowed bool
	Until   time.Time
}

func (m *Manager) cachedFriends() []string {
	m.friendsMu.Lock()
	defer m.friendsMu.Unlock()
	ids := []string{}
	now := time.Now()
	for id, v := range m.friends {
		if v.Allowed && now.Before(v.Until) {
			ids = append(ids, id)
		}
	}
	return ids
}
func privateContactID(id string) bool {
	return validID(id) && !strings.HasSuffix(id, "@chatroom") && id != "filehelper" && !strings.HasPrefix(id, "gh_")
}
func catalogContactAllowed(row map[string]any) bool {
	id, _ := row["id"].(string)
	if id == "" {
		id, _ = row["username"].(string)
	}
	if !privateContactID(id) || row["deleted"] == true {
		return false
	}
	relation, _ := row["relation"].(string)
	switch strings.ToLower(relation) {
	case "stranger", "blocked", "deleted", "not_friend", "group_member":
		return false
	}
	// Native profile currently reports relation=unknown for rows of its contact
	// table (separate from stranger). Directory membership is the available
	// evidence; do not pretend it is a verified friend-bit from the protocol.
	return true
}
func (m *Manager) cacheContact(id string, allowed bool) {
	m.friendsMu.Lock()
	defer m.friendsMu.Unlock()
	if m.friends == nil {
		m.friends = map[string]friendEntry{}
	}
	ttl := 5 * time.Minute
	if !allowed {
		ttl = 20 * time.Second
	}
	m.friends[id] = friendEntry{allowed, time.Now().Add(ttl)}
}
func (m *Manager) authorizeConversation(ctx context.Context, id string) (bool, error) {
	c := m.Config().Config
	if !c.PrivateEnabled || !c.PrivateAllFriends || !privateContactID(id) {
		return permittedRecipient(c, id), nil
	}
	m.friendsMu.Lock()
	v, ok := m.friends[id]
	m.friendsMu.Unlock()
	if ok && time.Now().Before(v.Until) {
		return v.Allowed, nil
	}
	reader, ok := m.native.(nativeCatalog)
	if !ok {
		return false, errors.New("当前微信后台不支持联系人目录核对")
	}
	cursor := ""
	seen := map[string]bool{}
	for {
		page, e := reader.Catalog(ctx, "contacts", "", id, cursor)
		if e != nil {
			return false, e
		}
		for _, row := range page.Items {
			peer, _ := row["id"].(string)
			username, _ := row["username"].(string)
			if peer == id || username == id {
				allowed := catalogContactAllowed(row)
				m.cacheContact(id, allowed)
				return allowed, nil
			}
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		cursor = *page.NextCursor
		if seen[cursor] {
			return false, errors.New("联系人目录游标重复，稍后重新核对")
		}
		seen[cursor] = true
	}
	m.cacheContact(id, false)
	return false, nil
}
func (m *Manager) warmFriends(ctx context.Context) {
	c := m.Config().Config
	if !c.PrivateEnabled || !c.PrivateAllFriends {
		return
	}
	reader, ok := m.native.(nativeCatalog)
	if !ok {
		return
	}
	cursor := ""
	seen := map[string]bool{}
	for {
		page, e := reader.Catalog(ctx, "contacts", "", "", cursor)
		if e != nil {
			return
		}
		for _, row := range page.Items {
			id, _ := row["id"].(string)
			if id == "" {
				id, _ = row["username"].(string)
			}
			if privateContactID(id) {
				m.cacheContact(id, catalogContactAllowed(row))
			}
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			return
		}
		cursor = *page.NextCursor
		if seen[cursor] {
			return
		}
		seen[cursor] = true
	}
}
