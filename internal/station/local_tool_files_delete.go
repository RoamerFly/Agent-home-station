package station

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"wechat-bot/internal/websession"
)

func regularToolDigest(f toolFile, dataRoot string) (string, error) {
	resolved, e := filepath.EvalSymlinks(f.Path)
	if e != nil || !strings.EqualFold(resolved, filepath.Clean(f.Path)) {
		return "", errors.New("文件路径已变化")
	}
	rel, e := filepath.Rel(dataRoot, resolved)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("文件不在数据目录")
	}
	file, e := os.Open(resolved)
	if e != nil {
		return "", e
	}
	defer file.Close()
	info, e := file.Stat()
	if e != nil || !info.Mode().IsRegular() {
		return "", errors.New("文件不是普通文件")
	}
	h := sha256.New()
	_, e = io.Copy(h, file)
	if e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (s *localProjectTools) toolFileDeleteRoute(owner *websession.Store, mux *http.ServeMux) {
	mux.HandleFunc("POST /api/local/tool-files/delete", owner.Protect(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			IDs     []string `json:"ids"`
			Preview bool     `json:"preview"`
		}
		if !websession.Decode(w, r, &in) {
			return
		}
		if len(in.IDs) == 0 {
			websession.Fail(w, 400, "请先选择文件")
			return
		}
		files, warnings := s.toolFiles(r.Context())
		byID := map[string]toolFile{}
		for _, f := range files {
			byID[f.ID] = f
		}
		hashes := []string{}
		locals := []toolFile{}
		seen := map[string]bool{}
		for _, id := range in.IDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			f, ok := byID[id]
			if !ok {
				websession.Fail(w, 409, "文件已变化，请刷新后重新选择")
				return
			}
			if f.State == "running" || f.State == "queued" {
				websession.Fail(w, 409, "任务仍在运行，请先停止")
				return
			}
			h, e := regularToolDigest(f, s.home.dataDir)
			if e != nil {
				websession.Fail(w, 409, e.Error())
				return
			}
			hashes = append(hashes, h)
			if !strings.Contains(filepath.ToSlash(f.Path), "/applications/wechat/legacy-admin/") {
				locals = append(locals, f)
			}
		}
		out := struct {
			Files    []string `json:"files"`
			Deleted  []string `json:"deleted"`
			Failures []string `json:"failures"`
			Warnings []string `json:"warnings"`
		}{Files: []string{}, Deleted: []string{}, Failures: []string{}, Warnings: warnings}
		if !in.Preview {
			for _, f := range locals {
				var e error
				p := filepath.ToSlash(f.Path)
				if (strings.Contains(p, "/publictools/rvc/") || strings.Contains(p, "/tools/rvc/")) && s.rvc != nil {
					e = s.rvc.Stop(r.Context())
				}
				if (strings.Contains(p, "/publictools/yue/") || strings.Contains(p, "/tools/yue/")) && s.yue != nil {
					e = s.yue.Stop(r.Context())
				}
				if e != nil {
					websession.Fail(w, 409, e.Error())
					return
				}
			}
		}
		if len(hashes) > 0 {
			client, e := s.legacyToolClient(r.Context())
			if e != nil {
				websession.Fail(w, 503, e.Error())
				return
			}
			raw, e := client.request(r.Context(), "POST", "/api/storage/media-delete", map[string]any{"hashes": hashes, "preview": in.Preview})
			if e != nil {
				websession.Fail(w, 409, e.Error())
				return
			}
			if json.Unmarshal(raw, &out) != nil {
				websession.Fail(w, 502, "删除结果不可读")
				return
			}
		}

		websession.Respond(w, 200, out)
	}))
}
