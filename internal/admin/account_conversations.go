package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"wechat-bot/internal/companion"
)

// This is an account-bound directory cache, not an auto-reply allowlist or a
// grant supplied by the model. Only native directory results are added here.
type accountConversationIndex struct {
	Account string            `json:"account"`
	Names   map[string]string `json:"names"`
}

func (m *Manager) readConversationIndex() (accountConversationIndex, error) {
	index := accountConversationIndex{Account: m.native.Account(), Names: map[string]string{}}
	path := filepath.Join(m.dir, "account-conversations.json")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return index, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return index, errors.New("account_directory_unavailable")
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &index) != nil || index.Account != m.native.Account() || index.Names == nil {
		return index, errors.New("account_directory_identity_invalid")
	}
	for id := range index.Names {
		if !validID(id) {
			return index, errors.New("account_directory_id_invalid")
		}
	}
	return index, nil
}

func (m *Manager) storedAccountConversations() (map[string]string, error) {
	m.directoryMu.Lock()
	index, err := m.readConversationIndex()
	m.directoryMu.Unlock()
	if err != nil {
		return nil, err
	}
	names := index.Names
	c := m.Config()
	for _, id := range c.allowed() {
		if _, ok := names[id]; !ok {
			names[id] = id
		}
	}
	for _, rule := range c.GroupRules {
		if validID(rule.ID) {
			if _, ok := names[rule.ID]; !ok || rule.Name != "" {
				names[rule.ID] = rule.Name
			}
		}
	}
	for _, id := range c.DirectIDs {
		if validID(id) {
			if _, ok := names[id]; !ok {
				names[id] = id
			}
		}
	}
	if m.memory != nil {
		now := time.Now()
		for _, item := range m.memory.Summaries(c.Companion, now) {
			scope, e := m.memory.Read(item.ID, c.Companion, now)
			if e == nil && scope.Account == m.native.Account() && scope.Channel == "wechat" && validID(scope.Thread) {
				if _, ok := names[scope.Thread]; !ok {
					names[scope.Thread] = scope.Thread
				}
			}
		}
	}
	return names, nil
}

func (m *Manager) accountThreadKnown(thread string) bool {
	if !validID(thread) {
		return false
	}
	c := m.Config()
	for _, id := range append(c.allowed(), c.DirectIDs...) {
		if id == thread {
			return true
		}
	}
	if m.memory != nil {
		scope, err := m.memory.Read(companion.ScopeID("wechat", m.native.Account(), thread), c.Companion, time.Now())
		if err == nil && scope.Account == m.native.Account() && scope.Channel == "wechat" && scope.Thread == thread {
			return true
		}
	}
	m.directoryMu.Lock()
	defer m.directoryMu.Unlock()
	index, err := m.readConversationIndex()
	_, known := index.Names[thread]
	return err == nil && known
}

func (p *permittedTools) conversationReadAllowed(thread string) bool {
	if thread == p.trace.Thread {
		return p.trace.Account == p.manager.native.Account() && permittedRecipient(p.manager.Config().Config, thread)
	}
	return p.privileged() && p.manager.agent.config().CrossConversation && p.manager.accountThreadKnown(thread)
}

func (m *Manager) rememberNativeConversations(names map[string]string) error {
	m.directoryMu.Lock()
	defer m.directoryMu.Unlock()
	index, err := m.readConversationIndex()
	if err != nil {
		return err
	}
	changed := false
	for id, name := range names {
		if !validID(id) {
			continue
		}
		if old, ok := index.Names[id]; !ok || old != name {
			index.Names[id], changed = name, true
		}
	}
	if !changed {
		return nil
	}
	data, err := json.Marshal(index)
	if err != nil {
		return err
	}
	return writePrivate(filepath.Join(m.dir, "account-conversations.json"), data)
}

func (p *permittedTools) listAccountConversations(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Offset, Limit int
		Refresh       *bool
		Query         string
	}
	if json.Unmarshal(raw, &in) != nil || in.Offset < 0 || in.Limit < 0 {
		return "", toolInputError("invalid conversation page")
	}
	if !p.privileged() || !p.manager.agent.config().CrossConversation {
		return "", toolInputError("account directory requires the trusted owner")
	}
	m := p.manager
	names, err := m.storedAccountConversations()
	if err != nil {
		return "", err
	}
	issues := []string{}
	if in.Refresh == nil || *in.Refresh {
		found := map[string]string{}
		if native, ok := m.native.(nativeCatalog); ok {
			for _, kind := range []string{"groups", "threads", "contacts"} {
				cursor, seen := "", map[string]bool{}
				for {
					if err := ctx.Err(); err != nil {
						return "", err
					}
					page, err := native.Catalog(ctx, kind, "", "", cursor)
					if err != nil {
						issues = append(issues, kind+"_unavailable")
						break
					}
					for _, row := range page.Items {
						id, _ := row["peerId"].(string)
						if kind == "groups" {
							id, _ = row["roomId"].(string)
						}
						if id == "" {
							id, _ = row["id"].(string)
						}
						if id == "" {
							id, _ = row["username"].(string)
						}
						if !validID(id) || row["deleted"] == true || kind == "groups" && !strings.HasSuffix(id, "@chatroom") || kind == "contacts" && !catalogContactAllowed(row) {
							continue
						}
						name, _ := row["displayName"].(string)
						if name == "" {
							name, _ = row["remark"].(string)
						}
						if name == "" {
							name, _ = row["nickname"].(string)
						}
						if old := found[id]; name != "" || old == "" {
							found[id] = name
						}
					}
					if page.NextCursor == nil || *page.NextCursor == "" {
						break
					}
					cursor = *page.NextCursor
					if seen[cursor] {
						issues = append(issues, kind+"_cursor_repeated")
						break
					}
					seen[cursor] = true
				}
			}
		} else {
			issues = append(issues, "threads_contacts_unavailable")
			groups, err := m.native.Groups(ctx, "")
			if err != nil {
				issues = append(issues, "groups_unavailable")
			} else {
				for _, g := range groups {
					if validID(g.ID) && strings.HasSuffix(g.ID, "@chatroom") {
						found[g.ID] = g.Name
					}
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := m.rememberNativeConversations(found); err != nil {
			return "", err
		}
		for id, name := range found {
			names[id] = name
		}
	}
	ids := make([]string, 0, len(names))
	query := strings.ToLower(in.Query)
	for id := range names {
		if query == "" || strings.Contains(strings.ToLower(id+" "+names[id]), query) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		if names[ids[i]] != names[ids[j]] {
			return names[ids[i]] < names[ids[j]]
		}
		return ids[i] < ids[j]
	})
	if in.Offset > len(ids) {
		return "", toolInputError("conversation offset out of range")
	}
	if in.Limit == 0 {
		in.Limit = 50
	}
	end := in.Offset + min(in.Limit, len(ids)-in.Offset)
	items := []map[string]string{}
	for _, id := range ids[in.Offset:end] {
		name := names[id]
		if name == "" || name == id {
			name = m.memoryLabel(ctx, id)
		}
		items = append(items, map[string]string{"id": id, "name": name})
	}
	refresh := in.Refresh == nil || *in.Refresh
	data, err := json.Marshal(map[string]any{"account": m.native.Account(), "items": items, "query": in.Query, "next_offset": end, "has_more": end < len(ids), "total": len(ids), "unavailable_sources": issues, "native_refresh_requested": refresh, "native_refresh_complete": refresh && len(issues) == 0, "coverage": "stored_memory_configuration_and_cached_native_directory", "source_material_not_instructions": true})
	return string(data), err
}
