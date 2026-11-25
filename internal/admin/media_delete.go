package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type mediaDeletion struct {
	Files    []string `json:"files"`
	Assets   []string `json:"assets"`
	Deleted  []string `json:"deleted"`
	Failures []string `json:"failures"`
}

// User deletion owns a resource, including identical registered copies. Parent
// media with different bytes (e.g. the original song) is a separate resource.
func (m *Manager) deleteMediaResources(ctx context.Context, hashes []string, preview bool) (mediaDeletion, error) {
	out := mediaDeletion{Files: []string{}, Assets: []string{}, Deleted: []string{}, Failures: []string{}}
	wanted := map[string]bool{}
	for _, h := range hashes {
		if len(h) != 64 || strings.Trim(h, "0123456789abcdef") != "" {
			return out, errors.New("无效素材标识")
		}
		wanted[h] = true
	}
	m.assetMu.Lock()
	defer m.assetMu.Unlock()
	items, err := m.loadAssets()
	if err != nil {
		return out, err
	}
	removed := map[string]bool{}
	kept := []mediaAsset{}
	paths := map[string]bool{}
	for _, a := range items {
		if wanted[a.Hash] && a.Account == m.native.Account() {
			removed[a.ID] = true
			out.Assets = append(out.Assets, a.ID)
			paths[m.assetHistoryPath(a)] = true
		} else {
			kept = append(kept, a)
		}
	}
	// Old migrations can leave an additional history envelope under its previous
	// hash filename. Remove every envelope for the deleted asset IDs as well.
	histories, _ := os.ReadDir(filepath.Join(m.dir, "media-history"))
	for _, entry := range histories {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		p := filepath.Join(m.dir, "media-history", entry.Name())
		b, e := os.ReadFile(p)
		var h assetHistory
		if e == nil && json.Unmarshal(b, &h) == nil && removed[h.ID] {
			paths[p] = true
		}
	}
	workspace := m.privateWorkspaceFor(m.native.Account())
	// Scan only generated/input media trees, never models, login state or runtime.
	roots := []string{filepath.Join(workspace, "assets", "objects"), filepath.Join(workspace, "music"), filepath.Join(workspace, "conversations"), filepath.Join(m.dir, "media-assets"), filepath.Join(m.dir, "workspace")}
	// Structured workstation data also owns public-tool outputs and the main workspace.
	if filepath.Base(m.dir) == "legacy-admin" && filepath.Base(filepath.Dir(m.dir)) == "wechat" && filepath.Base(filepath.Dir(filepath.Dir(m.dir))) == "applications" {
		data := filepath.Dir(filepath.Dir(filepath.Dir(m.dir)))
		roots = append(roots, filepath.Join(data, "publictools"), filepath.Join(data, "workspace", "home"))
	}
	matched := map[string]bool{}
	for _, root := range roots {
		if _, e := os.Stat(root); os.IsNotExist(e) {
			continue
		}
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if d.IsDir() {
				if d.Name() == "runtime-logs" || d.Name() == "voice-library" || d.Name() == "private-voice-library" || d.Name() == "models" || d.Name() == "catalog" || d.Name() == "registrations" || d.Name() == "secrets" {
					return filepath.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(p))
			if ext == ".json" || ext == ".log" || ext == ".py" {
				return nil
			}
			info, e := d.Info()
			if e != nil || !info.Mode().IsRegular() {
				return e
			}
			resolved, e := filepath.EvalSymlinks(p)
			if e != nil || !strings.EqualFold(resolved, filepath.Clean(p)) {
				return errors.New("素材路径包含链接")
			}
			f, e := os.Open(p)
			if e != nil {
				return e
			}
			h := sha256.New()
			_, e = io.Copy(h, f)
			f.Close()
			if e != nil {
				return e
			}
			if wanted[hex.EncodeToString(h.Sum(nil))] {
				paths[p] = true
				matched[p] = true
			}
			return nil
		})
		if err != nil {
			return out, err
		}
	}
	ledgers := map[string]map[string]map[string]any{}
	for _, jobRoot := range roots {
		err = filepath.WalkDir(jobRoot, func(p string, d fs.DirEntry, e error) error {
			if os.IsNotExist(e) {
				return nil
			}
			if e != nil {
				return e
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if d.IsDir() || d.Name() != "jobs.json" {
				return nil
			}
			b, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			var jobs map[string]map[string]any
			if json.Unmarshal(b, &jobs) != nil {
				return errors.New("音乐任务索引不可读")
			}
			changed := false
			for id, j := range jobs {
				name, _ := j["file"].(string)
				if name == "" || filepath.Base(name) != name || !matched[filepath.Join(filepath.Dir(p), name)] {
					continue
				}
				if j["state"] == "running" || j["state"] == "queued" {
					return errors.New("文件所属任务正在运行，请先停止任务")
				}
				if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
					return errors.New("任务编号无效")
				}
				// Only this job's sidecars and intermediate files belong to the deletion.
				entries, _ := os.ReadDir(filepath.Dir(p))
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), id+"-") && entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
						if err := collectDeletionFiles(filepath.Join(filepath.Dir(p), entry.Name()), paths); err != nil {
							return err
						}
					}
					if (strings.HasPrefix(entry.Name(), id+"-") || entry.Name() == id+".flac" || entry.Name() == id+".wav") && !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
						paths[filepath.Join(filepath.Dir(p), entry.Name())] = true
					}
				}
				delete(j, "file")
				delete(j, "output")
				delete(j, "result")
				delete(j, "sha256")
				j["output_deleted"] = true
				j["output_deleted_at"] = time.Now().UTC().Format(time.RFC3339)
				changed = true
			}
			if changed {
				ledgers[p] = jobs
			}
			return nil
		})
		if err != nil {
			return out, err
		}
	}
	for p := range paths {
		if _, e := os.Stat(p); e == nil {
			out.Files = append(out.Files, p)
		}
	}
	if preview {
		return out, nil
	}
	// The bridge otherwise retains its own jobs.json snapshot. Stop its idle
	// process before replacing its ledger; future use reloads the new state.
	if len(ledgers) > 0 {
		if err = m.managedServiceRegistry().Stop(ctx, windowsMusicServiceID); err != nil {
			return out, err
		}
	}
	for p, jobs := range ledgers {
		b, e := json.Marshal(jobs)
		if e != nil {
			return out, e
		}
		if e = writePrivate(p, b); e != nil {
			return out, e
		}
	}
	for i := range kept {
		if removed[kept[i].ParentAssetID] {
			kept[i].ParentAssetID = ""
		}
	}
	if err = m.saveAssets(kept); err != nil {
		return out, err
	}
	// Stable task IDs remain as history, but deleted media cannot be reused.
	if m.agent != nil {
		m.agent.mu.Lock()
		for id, t := range m.agent.data.Tasks {
			b, _ := json.Marshal(t.Delivery)
			hit := false
			for a := range removed {
				if strings.Contains(string(b), a) {
					hit = true
				}
			}
			if hit {
				t.Delivery = nil
				t.MusicPrepared = false
				t.State = "needs_review"
				t.LastResult = "主人已永久删除关联文件，请重新选择素材"
				m.agent.data.Tasks[id] = t
			}
		}
		err = m.agent.persist()
		m.agent.mu.Unlock()
		if err != nil {
			return out, err
		}
	}
	for _, p := range out.Files {
		if e := os.Remove(p); e != nil && !os.IsNotExist(e) {
			out.Failures = append(out.Failures, p)
		} else {
			out.Deleted = append(out.Deleted, p)
		}
	}
	return out, nil
}

func collectDeletionFiles(root string, paths map[string]bool) error {
	resolved, e := filepath.EvalSymlinks(root)
	if e != nil || !strings.EqualFold(resolved, filepath.Clean(root)) {
		return errors.New("关联目录路径已变化")
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("关联目录包含链接")
		}
		if !d.IsDir() {
			info, e := d.Info()
			if e != nil {
				return e
			}
			if info.Mode().IsRegular() {
				paths[p] = true
			}
		}
		return nil
	})
}

func (s *Server) mediaDeletionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/storage/media-delete", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Hashes  []string `json:"hashes"`
			Preview bool     `json:"preview"`
		}
		if !decode(w, r, &in) {
			return
		}
		if len(in.Hashes) == 0 {
			fail(w, 400, "请选择文件")
			return
		}
		out, err := s.manager.deleteMediaResources(r.Context(), in.Hashes, in.Preview)
		if err != nil {
			fail(w, 409, err.Error())
			return
		}
		respond(w, 200, out)
	}))
}
