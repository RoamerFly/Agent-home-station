package admin

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"
	"wechat-bot/internal/historyarchive"
)

type storageArea struct {
	Name    string `json:"name"`
	Bytes   int64  `json:"bytes"`
	Files   int    `json:"files"`
	Skipped int    `json:"skipped"`
}

func scanStorage(ctx context.Context, name string, paths ...string) (storageArea, error) {
	out := storageArea{Name: name}
	for _, root := range paths {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if e := ctx.Err(); e != nil {
				return e
			}
			if errors.Is(err, os.ErrNotExist) && path == root {
				return nil
			}
			if err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				out.Skipped++
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if out.Files >= 100000 {
				return errors.New("存储目录文件过多，扫描已停止")
			}
			st, e := d.Info()
			if e != nil {
				return e
			}
			if !st.Mode().IsRegular() {
				out.Skipped++
				return nil
			}
			out.Files++
			out.Bytes += st.Size()
			return nil
		})
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func (m *Manager) storageStatus(ctx context.Context) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	dirs := []struct {
		name  string
		paths []string
	}{
		{"活跃记忆", []string{filepath.Join(m.dir, "companion", "memory.json")}},
		{"活跃日志", []string{filepath.Join(m.dir, "activity.json"), filepath.Join(m.dir, "audit.json")}},
		{"任务与素材索引", []string{filepath.Join(m.dir, "agent-state.json"), filepath.Join(m.dir, "tool-state.json"), filepath.Join(m.dir, "media-assets.json")}},
		{"保留的媒体原件", []string{filepath.Join(m.dir, "media-assets")}},
		{"只读归档", []string{filepath.Join(m.dir, "archive"), filepath.Join(m.dir, "companion", "archive")}},
		{"收件与交付状态", []string{m.stateDir}},
	}
	areas := []storageArea{}
	var total int64
	for _, dir := range dirs {
		area, err := scanStorage(ctx, dir.name, dir.paths...)
		if err != nil {
			return nil, err
		}
		areas = append(areas, area)
		total += area.Bytes
	}
	m.agent.mu.Lock()
	tasks, ops := len(m.agent.data.Tasks), len(m.agent.data.Operations)
	m.agent.mu.Unlock()
	m.tools.mu.Lock()
	sends, uploads := len(m.tools.data.Sends), len(m.tools.data.Uploads)
	m.tools.mu.Unlock()
	m.mu.Lock()
	journalCount, lastError := len(m.journal), m.storageError
	m.mu.Unlock()
	return map[string]any{"areas": areas, "total_bytes": total, "error": lastError, "limits": []map[string]any{{"name": "持续任务", "count": tasks, "limit": 500}, {"name": "操作幂等记录", "count": ops, "limit": 2000}, {"name": "网页发送记录", "count": sends, "limit": 1000}, {"name": "附件暂存记录", "count": uploads, "limit": 1000}, {"name": "活跃日志", "count": journalCount, "limit": 1000}}, "boundary": "仅统计管理端数据和收件状态，不含外部YuE环境、模型、微信原生库及其媒体。归档会增加总占用，不自动删除媒体原件。"}, nil
}
func (s *Server) storageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/storage", s.protected(func(w http.ResponseWriter, r *http.Request) {
		out, err := s.manager.storageStatus(r.Context())
		if err != nil {
			fail(w, 500, "容量扫描未完成，请检查目录权限和文件数量")
			return
		}
		respond(w, 200, out)
	}))
	resolve := func(r *http.Request) (string, error) {
		switch r.PathValue("kind") {
		case "activity":
			return activityScope(), nil
		case "memory":
			id := r.URL.Query().Get("scope")
			_, err := s.manager.memoryScope(id)
			return id, err
		default:
			return "", errors.New("归档类型不存在")
		}
	}
	mux.HandleFunc("GET /api/archives/{kind}", s.protected(func(w http.ResponseWriter, r *http.Request) {
		scope, err := resolve(r)
		if err != nil {
			fail(w, 404, "归档会话不存在")
			return
		}
		var items []historyarchive.Info
		var next string
		if r.PathValue("kind") == "memory" {
			items, next, err = s.manager.memory.ArchiveList(scope, r.URL.Query().Get("after"))
		} else {
			items, next, err = s.manager.historyArchive().List("activity", scope, r.URL.Query().Get("after"), 20)
		}
		if err != nil {
			fail(w, 400, "归档目录不可读或分页位置无效")
			return
		}
		respond(w, 200, map[string]any{"items": items, "next": next})
	}))
	mux.HandleFunc("GET /api/archives/{kind}/{id}", s.protected(func(w http.ResponseWriter, r *http.Request) {
		scope, err := resolve(r)
		if err != nil {
			fail(w, 404, "归档会话不存在")
			return
		}
		var data any
		if r.PathValue("kind") == "memory" {
			memory, e := s.manager.memory.ArchiveRead(scope, r.PathValue("id"))
			err = e
			memory.Account = ""
			data = memory
		} else {
			data, err = s.manager.archiveLogs(r.PathValue("id"))
		}
		if err != nil {
			fail(w, 404, "归档不存在、范围不符或校验未通过，未恢复任何记录")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Query().Get("download") == "1" {
			w.Header().Set("Content-Disposition", `attachment; filename="wechat-history-`+r.PathValue("id")[:12]+`.json"`)
		}
		respond(w, 200, map[string]any{"kind": r.PathValue("kind"), "data": data, "read_only": true, "replays_actions": false})
	}))
}
