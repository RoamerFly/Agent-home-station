package linuxnative

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"wechat-bot/internal/identity"
)

type nameCacheEntry struct {
	name    identity.Name
	expires time.Time
	failed  bool
}

func (s *Sender) cachedName(ctx context.Context, key string, load func() (identity.Name, error)) (identity.Name, error) {
	s.namesMu.Lock()
	item, ok := s.names[key]
	s.namesMu.Unlock()
	if ok && time.Now().Before(item.expires) {
		if item.failed {
			return identity.Name{}, errors.New("name directory temporarily unavailable")
		}
		return item.name, nil
	}
	name, err := load()
	ttl := 30 * time.Second
	if err != nil {
		ttl = 5 * time.Second
	}
	s.namesMu.Lock()
	if s.names == nil || len(s.names) >= 2048 {
		s.names = map[string]nameCacheEntry{}
	}
	s.names[key] = nameCacheEntry{name, time.Now().Add(ttl), err != nil}
	s.namesMu.Unlock()
	return name, err
}

type contactName struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	Remark      string `json:"remark"`
	Nickname    string `json:"nickname"`
	DisplayName string `json:"displayName"`
}

func exactNameIdentity(id, username, wanted string) bool {
	return (id != "" || username != "") && (id == "" || id == wanted) && (username == "" || username == wanted)
}

// ResolveSpeaker reads exact, account-scoped local directory entries. No fuzzy
// search, roster pagination or metadata-refresh operation is used.
func (s *Sender) ResolveSpeaker(ctx context.Context, thread, sender string) (identity.Name, error) {
	if !safeThreadID(thread) || !safeID(sender) {
		return identity.Name{}, errors.New("invalid speaker identity")
	}
	return s.cachedName(ctx, "speaker\x00"+thread+"\x00"+sender, func() (identity.Name, error) {
		var contact contactName
		raw, err := s.toolJSON(ctx, http.MethodGet, s.accountPath()+"/contacts/"+url.PathEscape(sender), "", nil, "")
		if err != nil && !errors.Is(err, ErrToolNotFound) {
			return identity.Name{}, err
		}
		if err == nil {
			if json.Unmarshal(raw, &contact) != nil || !exactNameIdentity(contact.ID, contact.Username, sender) {
				return identity.Name{}, errors.New("contact identity mismatch")
			}
			if name := identity.Choose(sender, contact.Remark, "", ""); name.Source == "remark" {
				return name, nil
			}
		}
		roomName, memberName := "", ""
		if strings.HasSuffix(thread, "@chatroom") {
			raw, err = s.toolJSON(ctx, http.MethodGet, s.accountPath()+"/groups/"+url.PathEscape(thread)+"/members/"+url.PathEscape(sender), "", nil, "")
			if err != nil && !errors.Is(err, ErrToolNotFound) {
				return identity.Name{}, err
			}
			if err == nil {
				var value struct {
					GroupID  string `json:"groupId"`
					Username string `json:"username"`
					State    string `json:"state"`
					Member   *struct {
						ID          string `json:"id"`
						Username    string `json:"username"`
						DisplayName string `json:"displayName"`
						RoomName    string `json:"roomDisplayName"`
					} `json:"member"`
				}
				if json.Unmarshal(raw, &value) != nil || value.GroupID != thread || value.Username != sender {
					return identity.Name{}, errors.New("member scope mismatch")
				}
				if value.Member != nil && value.State == "present" {
					if !exactNameIdentity(value.Member.ID, value.Member.Username, sender) {
						return identity.Name{}, errors.New("member identity mismatch")
					}
					roomName, memberName = value.Member.RoomName, value.Member.DisplayName
				}
			}
		}
		nickname := ""
		for _, candidate := range []string{contact.Nickname, contact.DisplayName, memberName} {
			if candidate = identity.Clean(candidate); candidate != "" && candidate != sender {
				nickname = candidate
				break
			}
		}
		return identity.Choose(sender, "", roomName, nickname), nil
	})
}

func (s *Sender) ResolveConversation(ctx context.Context, thread string) (identity.Name, error) {
	if thread == "filehelper" {
		return identity.Name{Text: "文件传输助手", Source: "nickname"}, nil
	}
	if !strings.HasSuffix(thread, "@chatroom") {
		return s.ResolveSpeaker(ctx, thread, thread)
	}
	if !safeThreadID(thread) {
		return identity.Name{}, errors.New("invalid conversation identity")
	}
	return s.cachedName(ctx, "thread\x00"+thread, func() (identity.Name, error) {
		raw, err := s.toolJSON(ctx, http.MethodGet, s.accountPath()+"/groups/"+url.PathEscape(thread), "", nil, "")
		if err != nil {
			return identity.Name{}, err
		}
		var value struct {
			ID      string `json:"id"`
			RoomID  string `json:"roomId"`
			Remark  string `json:"remark"`
			Name    string `json:"name"`
			Display string `json:"displayName"`
		}
		if json.Unmarshal(raw, &value) != nil || !exactNameIdentity(value.ID, value.RoomID, thread) {
			return identity.Name{}, errors.New("conversation identity mismatch")
		}
		name := value.Name
		if identity.Clean(name) == "" {
			name = value.Display
		}
		return identity.Choose(thread, value.Remark, "", name), nil
	})
}
