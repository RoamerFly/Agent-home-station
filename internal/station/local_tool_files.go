package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"wechat-bot/internal/websession"
)

type toolFile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Folder   string `json:"folder"`
	Kind     string `json:"kind"`
	Source   string `json:"source"`
	JobID    string `json:"job_id,omitempty"`
	State    string `json:"state,omitempty"`
	Bytes    int64  `json:"bytes"`
	Modified string `json:"modified"`
}

type toolFileRoot struct{ path, source string }
type toolFileJob struct {
	ID, State, File string
	Recipe          struct{ Title string }
}

func (s *localProjectTools) fileRoots() []toolFileRoot {
	data := s.home.dataDir
	roots := []toolFileRoot{
		{stationPublicToolsDir(data), "工作站公共工具"},
		{filepath.Join(data, "workspace", "home"), "工作站工作区"},
		{filepath.Join(data, "applications", "wechat", "legacy-admin", "managed-workspaces"), "微信工具工作区（历史请求来源未记录）"},
		{filepath.Join(data, "applications", "wechat", "legacy-admin", "workspace"), "微信请求素材与产物"},
	}
	toolData := stationToolDataDir(data, "")
	if filepath.Clean(toolData) != filepath.Clean(stationPublicToolsDir(data)) {
		roots = append(roots, toolFileRoot{toolData, "公共工具运行数据"})
	}
	return roots
}

func toolFileKind(path string) string {
	p := strings.ToLower(filepath.ToSlash(path))
	for _, part := range strings.Split(p, "/") {
		if strings.HasSuffix(part, "-voice-work") || strings.HasPrefix(part, "validation-") || strings.Contains(part, "managed-test") || part == "scratch" || part == "comfy-input" || part == "input" || part == "base" {
			return ""
		}
	}
	if strings.HasPrefix(strings.ToLower(filepath.Base(path)), "manual-") {
		return ""
	}
	for _, part := range []string{"/models/", "/voice-library/", "/private-voice-library/", "/catalog/", "/registrations/", "/secrets/", "/node_modules/", "/site-packages/", "/.git/"} {
		if strings.Contains(p, part) {
			return ""
		}
	}
	if strings.Contains(p, "/assets/objects/") {
		return "素材"
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".wav", ".mp3", ".flac", ".m4a", ".ogg", ".opus", ".mp4", ".webm", ".png", ".jpg", ".jpeg", ".webp", ".pdf", ".docx", ".xlsx", ".pptx":
		if strings.Contains(p, "/input/") || strings.Contains(p, "/base/") || strings.Contains(p, "/assets/") || strings.Contains(p, "/downloads/") {
			return "素材"
		}
		return "产物"
	case ".tmp", ".part":
		return ""
	}
	return ""
}

func (s *localProjectTools) toolFiles(ctx context.Context) ([]toolFile, []string) {
	out := []toolFile{}
	warnings := []string{}
	seen := map[string]bool{}
	for _, root := range s.fileRoots() {
		if _, err := os.Stat(root.path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		err := filepath.WalkDir(root.path, func(path string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				warnings = append(warnings, path+": 不可读")
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			resolved, e := filepath.EvalSymlinks(path)
			if e != nil || !strings.EqualFold(filepath.Clean(path), resolved) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				switch strings.ToLower(d.Name()) {
				case "models", "voice-library", "private-voice-library", "catalog", "registrations", "secrets", "site-packages", "node_modules", ".git":
					return filepath.SkipDir
				}
				return nil
			}
			kind := toolFileKind(path)
			if kind == "" || seen[strings.ToLower(path)] {
				return nil
			}
			info, e := d.Info()
			if e != nil || !info.Mode().IsRegular() {
				return nil
			}
			seen[strings.ToLower(path)] = true
			sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", path, info.Size(), info.ModTime().UnixNano())))
			item := toolFile{ID: hex.EncodeToString(sum[:]), Name: d.Name(), Path: path, Folder: filepath.Dir(path), Kind: kind, Source: root.source, Bytes: info.Size(), Modified: info.ModTime().UTC().Format(time.RFC3339)}
			if strings.Contains(path, "validation-") {
				item.Source = "本机功能验证（非微信请求）"
			}
			out = append(out, item)
			return nil
		})
		if err != nil {
			warnings = append(warnings, root.path+": 扫描未完成")
		}
	}
	// Read private provenance on demand; never copy conversation metadata into public packages.
	var assets []struct {
		Hash, Title, Filename string
		StorageName           string `json:"stored_filename"`
		FirstRequest          *struct {
			Name string `json:"requester_name"`
			Text string `json:"request"`
			Task string `json:"task_id"`
		} `json:"first_request"`
	}
	index := filepath.Join(s.home.dataDir, "applications", "wechat", "legacy-admin", "media-assets.json")
	if info, e := os.Stat(index); e == nil && info.Size() < 16<<20 {
		if b, e := os.ReadFile(index); e == nil {
			_ = json.Unmarshal(b, &assets)
		}
	}
	byHash := map[string]int{}
	for i, a := range assets {
		if a.Hash != "" {
			byHash[a.Hash] = i
			if a.StorageName != "" {
				byHash[a.StorageName] = i
			}
		}
	}
	for i := range out {
		f := &out[i]
		if a, ok := byHash[f.Name]; ok {
			item := assets[a]
			if item.Filename != "" {
				f.Name = item.Filename
				if item.StorageName != "" {
					f.Name = item.StorageName
				}
			}
			if item.FirstRequest != nil {
				f.Source = "微信请求 · " + item.FirstRequest.Name + " · " + item.FirstRequest.Text
				f.JobID = item.FirstRequest.Task
			}
		}
	}
	// Existing bridge job ledgers describe actual outputs; no synthetic provenance.
	ledgers := map[string]map[string]toolFileJob{}
	for i := range out {
		item := &out[i]
		ledger := filepath.Join(item.Folder, "jobs.json")
		jobs, ok := ledgers[ledger]
		if !ok {
			jobs = map[string]toolFileJob{}
			if info, err := os.Stat(ledger); err == nil && info.Size() < 16<<20 {
				if b, e := os.ReadFile(ledger); e == nil {
					_ = json.Unmarshal(b, &jobs)
				}
			}
			ledgers[ledger] = jobs
		}
		for id, job := range jobs {
			if job.File != "" && strings.HasPrefix(filepath.Base(item.Path), id) && filepath.Base(job.File) != filepath.Base(item.Path) {
				item.Kind = ""
			}
			if job.File != "" && (filepath.Clean(job.File) == item.Path || filepath.Base(job.File) == item.Name) {
				item.JobID = id
				item.State = job.State
				if strings.HasPrefix(job.Recipe.Title, "本机目录验证-") || strings.HasPrefix(job.Recipe.Title, "目录迁移本机验证") || strings.HasPrefix(job.Recipe.Title, "bridge-smoke") {
					item.Kind = ""
				}
				break
			}
		}
	}
	formal := out[:0]
	for _, f := range out {
		if f.Kind != "" {
			formal = append(formal, f)
		}
	}
	out = formal
	sort.Slice(out, func(i, j int) bool {
		if out[i].Modified == out[j].Modified {
			return out[i].Path < out[j].Path
		}
		return out[i].Modified > out[j].Modified
	})
	return out, warnings
}

func (s *localProjectTools) toolFileRoutes(owner *websession.Store, mux *http.ServeMux) {
	s.toolFileDeleteRoute(owner, mux)
	mux.HandleFunc("GET /api/local/tool-files", owner.Protect(func(w http.ResponseWriter, r *http.Request) {
		files, warnings := s.toolFiles(r.Context())
		query := strings.ToLower(r.URL.Query().Get("q"))
		kind := r.URL.Query().Get("kind")
		filtered := []toolFile{}
		var totalBytes int64
		for _, f := range files {
			if kind != "" && kind != f.Kind {
				continue
			}
			if query != "" && !strings.Contains(strings.ToLower(f.Name+" "+f.Path+" "+f.Source+" "+f.JobID), query) {
				continue
			}
			filtered = append(filtered, f)
			totalBytes += f.Bytes
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit := 100
		if raw := r.URL.Query().Get("limit"); raw != "" {
			var err error
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 1 {
				websession.Fail(w, 400, "分页数量无效")
				return
			}
		}
		if offset < 0 || offset > len(filtered) {
			websession.Fail(w, 400, "分页位置无效")
			return
		}
		end := len(filtered)
		if limit < end-offset {
			end = offset + limit
		}
		next := -1
		if end < len(filtered) {
			next = end
		}
		w.Header().Set("Cache-Control", "no-store")
		websession.Respond(w, 200, map[string]any{"files": filtered[offset:end], "total": len(filtered), "bytes": totalBytes, "next_offset": next, "warnings": warnings})
	}))
	mux.HandleFunc("POST /api/local/tool-files/open", owner.Protect(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID string `json:"id"`
		}
		if !websession.Decode(w, r, &in) {
			return
		}
		files, _ := s.toolFiles(r.Context())
		for _, f := range files {
			if f.ID != in.ID {
				continue
			}
			if runtime.GOOS != "windows" {
				websession.Fail(w, 501, "当前平台不支持文件资源管理器")
				return
			}
			system := os.Getenv("SystemRoot")
			if !filepath.IsAbs(system) {
				websession.Fail(w, 503, "系统目录不可用")
				return
			}
			cmd := exec.Command(filepath.Join(system, "explorer.exe"), "/select,", f.Path)
			if err := cmd.Start(); err != nil {
				websession.Fail(w, 503, "文件夹打开失败")
				return
			}
			go cmd.Wait()
			websession.Respond(w, 200, map[string]any{"opened": true, "folder": f.Folder, "selected_file": f.Path})
			return
		}
		websession.Fail(w, 404, "文件已移动，请刷新")
	}))
}
