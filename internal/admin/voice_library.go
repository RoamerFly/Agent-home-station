package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"wechat-bot/internal/workspacefs"
)

var voiceObjectName = regexp.MustCompile(`^[a-f0-9]{64}\.(wav|mp3|flac|m4a|txt|pth|ckpt|index)$`)

func (s *Server) voiceLibraryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /voice-library", s.protected(func(w http.ResponseWriter, r *http.Request) {
		page, err := assets.ReadFile("static/voices.html")
		if err != nil {
			fail(w, 500, "语音库页面不可读")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(page)
	}))
	mux.HandleFunc("GET /api/voice-library", s.protected(func(w http.ResponseWriter, r *http.Request) {
		path := s.manager.privateVoiceLibrary()
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			value, mergeErr := mergeVoiceCatalogue(map[string]any{"items": []any{}}, filepath.Join(s.manager.managedRoot(), "public_workspace", "models", "voice-library"))
			if mergeErr != nil {
				fail(w, 500, "公共语音库索引不可读")
				return
			}
			respond(w, 200, value)
			return
		}
		root, err := workspacefs.EnsurePath(path)
		if os.IsNotExist(err) {
			respond(w, 200, map[string]any{"items": []any{}})
			return
		}
		if err != nil {
			fail(w, 500, "语音库目录不可读")
			return
		}
		defer root.Close()
		f, err := root.Open("catalog.json")
		if err != nil {
			fail(w, 500, "语音库索引不可读")
			return
		}
		defer f.Close()
		var value map[string]any
		decoder := json.NewDecoder(io.LimitReader(f, 8<<20))
		if decoder.Decode(&value) != nil {
			fail(w, 500, "语音库索引无效")
			return
		}
		value, err = mergeVoiceCatalogue(value, filepath.Join(s.manager.managedRoot(), "public_workspace", "models", "voice-library"))
		if err != nil {
			fail(w, 500, "公共语音库索引不可读")
			return
		}
		respond(w, 200, value)
	}))
	mux.HandleFunc("GET /api/voice-library/files/{name}", s.protected(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !voiceObjectName.MatchString(name) {
			fail(w, 400, "语音库文件名无效")
			return
		}
		directory, relative, err := s.manager.voiceLibraryObject(name)
		if err != nil {
			fail(w, 404, "未找到语音库文件")
			return
		}
		root, err := workspacefs.EnsurePath(directory)
		if err != nil {
			fail(w, 404, "未找到语音库文件")
			return
		}
		defer root.Close()
		f, err := workspacefs.OpenRegular(root, relative)
		if err != nil {
			fail(w, 404, "未找到语音库文件")
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			fail(w, 400, "语音库文件不可读")
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, name, info.ModTime(), f)
	}))
}
