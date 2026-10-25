package linuxnative

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
)

type CatalogPage struct {
	Items      []map[string]any `json:"items"`
	NextCursor *string          `json:"next_cursor"`
	Coverage   any              `json:"coverage"`
}

func (s *Sender) Catalog(ctx context.Context, kind, thread, search, cursor string) (CatalogPage, error) {
	path := ""
	fields := []string{}
	switch kind {
	case "contacts":
		path = "/contacts"
		fields = []string{"id", "username", "displayName", "nickname", "remark", "alias", "relation"}
	case "groups":
		path = "/groups"
		fields = []string{"id", "roomId", "displayName", "name", "remark", "ownerId", "announcement", "muted", "myDisplayName", "isInGroup"}
	case "threads":
		path = "/threads"
		fields = []string{"id", "peerId", "kind", "displayName", "unreadCount"}
	case "messages":
		if !safeThreadID(thread) {
			return CatalogPage{}, errors.New("invalid thread")
		}
		path = "/threads/" + url.PathEscape(thread) + "/messages"
		fields = []string{"id", "threadId", "serverId", "senderId", "type", "direction", "body", "createdAt"}
	case "members":
		if !safeThreadID(thread) {
			return CatalogPage{}, errors.New("invalid group")
		}
		path = "/groups/" + url.PathEscape(thread) + "/members"
		fields = []string{"id", "username", "displayName", "roomDisplayName"}
	default:
		return CatalogPage{}, errors.New("unsupported catalog")
	}
	if len(search) > 256 || len(cursor) > 8192 {
		return CatalogPage{}, errors.New("catalog query too long")
	}
	query := url.Values{"limit": []string{"50"}}
	if search != "" {
		query.Set("search", search)
	}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	raw, err := s.toolJSON(ctx, "GET", s.accountPath()+path+"?"+query.Encode(), "", nil, "")
	if err != nil {
		return CatalogPage{}, err
	}
	var page struct {
		Items      []map[string]any `json:"items"`
		NextCursor *string          `json:"nextCursor"`
		Coverage   any              `json:"coverage"`
	}
	if json.Unmarshal(raw, &page) != nil || len(page.Items) > 50 {
		return CatalogPage{}, errors.New("invalid catalog page")
	}
	out := CatalogPage{Items: []map[string]any{}, NextCursor: page.NextCursor, Coverage: page.Coverage}
	for _, item := range page.Items {
		if item["deleted"] == true {
			continue
		}
		row := map[string]any{}
		for _, field := range fields {
			if value, ok := item[field]; ok {
				row[field] = value
			}
		}
		if kind == "messages" && row["type"] != float64(1) {
			row["body"] = "[非文字消息，可通过附件功能处理]"
		}
		out.Items = append(out.Items, row)
	}
	return out, nil
}
