package admin

import (
	"context"
	"net/http"
	"strings"
	"time"
	"wechat-bot/internal/companion"
)

type proactiveTarget struct {
	ID    string                    `json:"id"`
	Name  string                    `json:"name"`
	Kind  string                    `json:"kind"`
	State companion.ProactiveStatus `json:"state"`
}

func (s *Server) proactiveTargets(w http.ResponseWriter, r *http.Request) {
	c := s.manager.Config()
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	items := []proactiveTarget{}
	for _, id := range c.allowed() {
		kind := "friend"
		if strings.HasSuffix(id, "@chatroom") {
			kind = "group"
		}
		if id == "filehelper" {
			kind = "filehelper"
		}
		items = append(items, proactiveTarget{ID: id, Name: s.manager.memoryLabel(ctx, id), Kind: kind, State: s.manager.memory.ProactiveState("wechat", s.manager.native.Account(), id, c.Companion, time.Now())})
	}
	_, today := s.manager.memory.Counters(c.Companion, time.Now())
	respond(w, 200, map[string]any{"items": items, "today": today, "max_per_day": c.Companion.ProactiveMaxPerDay})
}
