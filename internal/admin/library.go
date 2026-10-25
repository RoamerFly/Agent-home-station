package admin

import (
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"wechat-bot/internal/companion"
)

type libraryItem struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

func (s *toolStore) library(account string) []libraryItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []libraryItem{}
	for _, source := range s.data.Uploads {
		if source.Account != account || source.Kind != "image" {
			continue
		}
		title := source.AITitle
		if title == "" {
			title = source.Filename
		}
		out = append(out, libraryItem{source.ID, title, source.AIDescription, true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (s *toolStore) imageAssets(account string) []companion.ImageAsset {
	out := []companion.ImageAsset{}
	for _, item := range s.library(account) {
		if len(out) < 20 {
			out = append(out, companion.ImageAsset{ID: item.ID, Title: item.Title, Description: item.Description})
		}
	}
	return out
}
func (s *toolStore) recent(account string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := []toolRecord{}
	for _, record := range s.data.Sends {
		if record.Account == account {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Created.After(records[j].Created) })
	if len(records) > 20 {
		records = records[:20]
	}
	out := []map[string]any{}
	for _, record := range records {
		out = append(out, map[string]any{"key": record.Key, "status": record.Status, "created": record.Created})
	}
	return out
}
func (s *Server) libraryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/library", s.protected(func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]any{"items": s.manager.tools.library(s.manager.native.Account()), "send_image_permission": s.manager.Config().Companion.Permissions.SendImage})
	}))
	mux.HandleFunc("POST /api/library/{id}", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Enabled     bool   `json:"enabled"`
		}
		if !decode(w, r, &input) {
			return
		}
		input.Title = strings.TrimSpace(input.Title)
		input.Description = strings.TrimSpace(input.Description)
		if input.Title == "" || len(input.Title) > 256 || len(input.Description) > 1024 || !utf8.ValidString(input.Title+input.Description) {
			fail(w, 400, "标题最多 256 字节，图片描述最多 1024 字节")
			return
		}
		m := s.manager
		m.op.Lock()
		defer m.op.Unlock()
		source, exists := m.tools.source(r.PathValue("id"), m.native.Account(), "image")
		if !exists {
			fail(w, 404, "未找到本账号已上传的图片")
			return
		}
		c := m.runtimeConfig()
		restart := c.Enabled && c.Companion.Enabled
		if restart {
			if err := m.stop(); err != nil {
				fail(w, 409, err.Error())
				return
			}
			defer m.start(c)
		}
		source.AITitle, source.AIDescription, source.AIEnabled = input.Title, input.Description, true
		if err := m.tools.upload(source); err != nil {
			fail(w, 500, "图片素材配置保存失败")
			return
		}
		m.log("image_library_updated")
		respond(w, 200, map[string]bool{"ok": true})
	}))
}
