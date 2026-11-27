package station

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"wechat-bot/internal/identity"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

// Work files remain in their application workspaces. Registered artifacts are
// separate immutable snapshots and retain their original provenance rules.
type localWorkItem struct {
	ID           string `json:"id"`
	Application  string `json:"application"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	Source       string `json:"source"`
	Bytes        int64  `json:"bytes"`
	ModifiedAt   string `json:"modified_at"`
	MIME         string `json:"mime"`
	Version      string `json:"version,omitempty"`
	Previewable  bool   `json:"previewable"`
	PreviewURL   string `json:"preview_url,omitempty"`
	DownloadURL  string `json:"download_url,omitempty"`
	CanRename    bool   `json:"can_rename"`
	CanTrash     bool   `json:"can_trash"`
	CanRestore   bool   `json:"can_restore"`
	RestoreURL   string `json:"restore_url,omitempty"`
	OriginalPath string `json:"original_path,omitempty"`
}

type localWorksCursor struct {
	Application string `json:"application"`
	Query       string `json:"q"`
	Status      string `json:"status"`
	ModifiedAt  string `json:"modified_at"`
	ID          string `json:"id"`
}

type localWorkRef struct{ kind, app, rel string }

func workID(kind, app, rel string) string {
	return kind + "-" + base64.RawURLEncoding.EncodeToString([]byte(app+"\n"+rel))
}

func parseWorkID(id string) (localWorkRef, bool) {
	if strings.HasPrefix(id, "artifact-") {
		artifact := strings.TrimPrefix(id, "artifact-")
		return localWorkRef{kind: "artifact", rel: artifact}, artifactID(artifact, "work-")
	}
	for _, kind := range []string{"file", "trash"} {
		if !strings.HasPrefix(id, kind+"-") {
			continue
		}
		encoded := strings.TrimPrefix(id, kind+"-")
		if len(encoded) > 4096 {
			return localWorkRef{}, false
		}
		raw, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || len(raw) > 3072 {
			return localWorkRef{}, false
		}
		parts := strings.SplitN(string(raw), "\n", 2)
		if len(parts) != 2 || !identity.ValidPathID(parts[0]) || parts[0] != strings.ToLower(parts[0]) {
			return localWorkRef{}, false
		}
		if kind == "trash" {
			return localWorkRef{kind: kind, app: parts[0], rel: parts[1]}, artifactID("sha-"+parts[1], "sha-")
		}
		if !validWorkRelative(parts[1]) {
			return localWorkRef{}, false
		}
		return localWorkRef{kind: kind, app: parts[0], rel: parts[1]}, true
	}
	return localWorkRef{}, false
}

func validWorkRelative(rel string) bool {
	if rel == "" || path.Clean(rel) != rel || strings.HasPrefix(rel, "/") || strings.ContainsAny(rel, "\\:\x00") || len(rel) > 3000 {
		return false
	}
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." || hiddenWorkName(part) || i < len(parts)-1 && hiddenWorkDirectory(part) {
			return false
		}
	}
	return true
}

func hiddenWorkName(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, ".") || strings.HasSuffix(lower, ".tmp") || strings.HasSuffix(lower, ".part") ||
		strings.HasSuffix(lower, ".lock") || strings.HasSuffix(lower, "~") || strings.Contains(lower, "secret") ||
		strings.Contains(lower, "credential") || strings.Contains(lower, "password") || strings.Contains(lower, "cookie") ||
		strings.Contains(lower, "token") || lower == "config.json" || lower == "account.json" || lower == "session.json" {
		return true
	}
	return false
}

func hiddenWorkDirectory(name string) bool {
	lower := strings.ToLower(name)
	return hiddenWorkName(name) || hiddenToolFileDirectory(name) || slices.Contains([]string{
		"conversations", "messages", "guides", "global", "diary", "assets", "jobs", "runtime-logs",
		"user", "web-login", "cache", "tmp", "temp", "connected apps", "project tools", "public_workspace",
	}, lower)
}

func classifiedWork(app, rel string) (visible, manageable bool) {
	if !validWorkRelative(rel) {
		return false, false
	}
	if strings.HasPrefix(rel, "works/") {
		return true, true
	}
	if app == "home" && (strings.HasPrefix(rel, "projects/") || strings.HasPrefix(rel, "workflows/")) {
		return true, false
	}
	if toolFileKind(filepath.FromSlash(rel)) == "产物" {
		return true, true
	}
	return false, false
}

func workMIME(name string) (string, bool) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png", true
	case ".jpg", ".jpeg":
		return "image/jpeg", true
	case ".webp":
		return "image/webp", true
	case ".gif":
		return "image/gif", true
	case ".mp3":
		return "audio/mpeg", true
	case ".wav":
		return "audio/wav", true
	case ".flac":
		return "audio/flac", true
	case ".ogg", ".opus":
		return "audio/ogg", true
	case ".m4a":
		return "audio/mp4", true
	case ".mp4":
		return "video/mp4", true
	case ".webm":
		return "video/webm", true
	case ".pdf":
		return "application/pdf", true
	case ".txt", ".md", ".csv", ".json", ".yaml", ".yml":
		return "text/plain; charset=utf-8", true
	default:
		return "application/octet-stream", false
	}
}

func workFileVersion(app, rel string, info os.FileInfo) string {
	return localCheckpointKey("local-work-file-v1", app, rel, info.Size(), info.ModTime().UnixNano())
}

func workFileItem(app, rel string, info os.FileInfo) localWorkItem {
	id := workID("file", app, rel)
	mimeType, preview := workMIME(path.Base(rel))
	_, manage := classifiedWork(app, rel)
	item := localWorkItem{ID: id, Application: app, Name: path.Base(rel), Path: app + "/" + rel,
		Source: "workspace", Bytes: info.Size(), ModifiedAt: info.ModTime().UTC().Format("2006-01-02T15:04:05.000000000Z"),
		MIME: mimeType, Version: workFileVersion(app, rel, info), Previewable: preview,
		DownloadURL: "/api/local/works/" + id + "/download", CanRename: manage, CanTrash: manage}
	if preview {
		item.PreviewURL = "/api/local/works/" + id + "/preview"
	}
	return item
}

func (s *localArtifactStore) workApplications() []string {
	apps := []string{"home"}
	if s.chat != nil {
		for _, app := range s.chat.applications.Applications() {
			if identity.ValidPathID(app.ID) && app.ID == strings.ToLower(app.ID) && app.ID != "home" {
				apps = append(apps, app.ID)
			}
		}
	}
	sort.Strings(apps)
	return slices.Compact(apps)
}

func (s *localArtifactStore) walkWorkFiles(ctx context.Context, app string, appRoot *os.Root, rel string, add func(localWorkItem)) error {
	directory, err := workspacefs.OpenExistingDirectory(appRoot, filepath.FromSlash(rel))
	if err != nil {
		return err
	}
	defer directory.Close()
	file, err := workspacefs.OpenDirectory(directory, ".")
	if err != nil {
		return err
	}
	entries, err := file.ReadDir(-1)
	file.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if hiddenWorkName(name) {
			continue
		}
		info, err := directory.Lstat(name)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		child := name
		if rel != "." {
			child = rel + "/" + name
		}
		if info.IsDir() {
			if hiddenWorkDirectory(name) {
				continue
			}
			if err := s.walkWorkFiles(ctx, app, appRoot, child, add); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		visible, _ := classifiedWork(app, child)
		if !visible {
			continue
		}
		opened, err := workspacefs.OpenRegular(directory, name)
		if err != nil {
			continue
		}
		actual, err := opened.Stat()
		opened.Close()
		if err == nil && actual.Mode().IsRegular() {
			add(workFileItem(app, child, actual))
		}
	}
	return nil
}

func (s *localArtifactStore) activeWorks(ctx context.Context, apps []string) ([]localWorkItem, error) {
	root, err := workspacefs.OpenPath(s.home.root)
	if os.IsNotExist(err) {
		return []localWorkItem{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	items := []localWorkItem{}
	for _, app := range apps {
		appRoot, err := workspacefs.OpenExistingDirectory(root, app)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		err = s.walkWorkFiles(ctx, app, appRoot, ".", func(item localWorkItem) { items = append(items, item) })
		appRoot.Close()
		if err != nil {
			return nil, err
		}
	}
	if slices.Contains(apps, "home") {
		snapshots, err := s.registeredWorks(ctx)
		if err != nil {
			return nil, err
		}
		items = append(items, snapshots...)
	}
	return items, nil
}

func (s *localArtifactStore) registeredWorks(ctx context.Context) ([]localWorkItem, error) {
	root, err := workspacefs.OpenPath(filepath.Join(s.home.dataDir, "artifacts"))
	if os.IsNotExist(err) {
		return []localWorkItem{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir, err := workspacefs.OpenDirectory(root, ".")
	if err != nil {
		return nil, err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return nil, err
	}
	items := []localWorkItem{}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !entry.IsDir() || !artifactID(entry.Name(), "work-") {
			continue
		}
		a, err := s.read(entry.Name())
		if err != nil {
			continue
		}
		child, err := workspacefs.OpenExistingDirectory(root, entry.Name())
		if err != nil {
			continue
		}
		info, err := child.Stat("record.json")
		child.Close()
		if err != nil {
			continue
		}
		id := "artifact-" + a.ID
		mimeType, preview := workMIME(a.Name)
		item := localWorkItem{ID: id, Application: "home", Name: a.Name, Path: "已登记成品/" + a.Name,
			Source: "snapshot", Bytes: a.Bytes, ModifiedAt: info.ModTime().UTC().Format("2006-01-02T15:04:05.000000000Z"),
			MIME: mimeType, Previewable: preview, DownloadURL: "/api/local/works/" + id + "/download"}
		if preview {
			item.PreviewURL = "/api/local/works/" + id + "/preview"
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *localArtifactStore) workFile(ref localWorkRef, grant localWorkspaceGrant) (*os.File, os.FileInfo, error) {
	if ref.kind != "file" || !slices.Contains(s.workApplications(), ref.app) {
		return nil, nil, os.ErrNotExist
	}
	visible, _ := classifiedWork(ref.app, ref.rel)
	if !visible {
		return nil, nil, os.ErrNotExist
	}
	root, err := workspacefs.OpenPath(filepath.Join(s.home.root, ref.app))
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	parent, err := workspacefs.OpenExistingDirectory(root, filepath.FromSlash(path.Dir(ref.rel)))
	if err != nil {
		return nil, nil, err
	}
	defer parent.Close()
	file, err := workspacefs.OpenRegular(parent, path.Base(ref.rel))
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !(&localWorkspaceAccess{home: s.home}).current(grant) {
		file.Close()
		return nil, nil, os.ErrNotExist
	}
	return file, info, nil
}

func (s *localArtifactStore) serveWork(w http.ResponseWriter, r *http.Request, preview bool) {
	ref, ok := parseWorkID(r.PathValue("id"))
	if !ok || ref.kind == "trash" {
		websession.Fail(w, 404, "作品不存在或已移走")
		return
	}
	var file *os.File
	var name string
	var err error
	if ref.kind == "artifact" {
		a, e := s.read(ref.rel)
		if e != nil {
			websession.Fail(w, 404, "已登记作品不存在或来源已变化")
			return
		}
		name = a.Name
		file, err = s.open(r.Context(), a, func() bool { _, ok := s.access(a); return ok })
	} else {
		grant, e := (&localWorkspaceAccess{home: s.home}).resolve()
		if e != nil {
			websession.Fail(w, 403, "家目录授权不可用")
			return
		}
		name = path.Base(ref.rel)
		file, _, err = s.workFile(ref, grant)
	}
	if err != nil {
		websession.Fail(w, 404, "作品不可读取或已变化")
		return
	}
	defer file.Close()
	mimeType, canPreview := workMIME(name)
	if preview && !canPreview {
		websession.Fail(w, 415, "此文件类型不支持站内预览，请下载查看")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if preview {
		w.Header().Set("Content-Type", mimeType)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": name}))
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	}
	http.ServeContent(w, r, name, time.Time{}, file)
}

func (s *localArtifactStore) listWorks(w http.ResponseWriter, r *http.Request) {
	grant, err := (&localWorkspaceAccess{home: s.home}).resolve()
	if err != nil {
		websession.Fail(w, 403, "请先启用家目录授权")
		return
	}
	params := r.URL.Query()
	app := params.Get("application")
	if app == "" {
		app = "all"
	}
	apps := s.workApplications()
	if app != "all" && !slices.Contains(apps, app) {
		websession.Fail(w, 400, "应用不存在或未接入")
		return
	}
	if app != "all" {
		apps = []string{app}
	}
	status := params.Get("status")
	if status == "" {
		status = "active"
	}
	if status != "active" && status != "trash" {
		websession.Fail(w, 400, "作品状态无效")
		return
	}
	query := strings.ToLower(strings.TrimSpace(params.Get("q")))
	if len(query) > 200 {
		websession.Fail(w, 400, "搜索词过长")
		return
	}
	limit := 40
	if raw := params.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 1000 {
			websession.Fail(w, 400, "单页数量须在 1 至 1000 之间，请用游标继续读取")
			return
		}
	}
	var cursor localWorksCursor
	if raw := params.Get("cursor"); raw != "" {
		decoded, e := base64.RawURLEncoding.DecodeString(raw)
		if e != nil || len(decoded) > 4096 || json.Unmarshal(decoded, &cursor) != nil || cursor.Application != app || cursor.Query != query || cursor.Status != status || cursor.ID == "" {
			websession.Fail(w, 400, "作品分页游标无效，请重新读取首页")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var items []localWorkItem
	if status == "trash" {
		items, err = s.trashedWorks(ctx, apps)
	} else {
		items, err = s.activeWorks(ctx, apps)
	}
	if err != nil {
		websession.Fail(w, 503, "作品目录暂不可读取，请按应用缩小范围后重试")
		return
	}
	if !(&localWorkspaceAccess{home: s.home}).current(grant) {
		websession.Fail(w, 409, "家目录授权已变化，请刷新")
		return
	}
	filtered := items[:0]
	for _, item := range items {
		if query == "" || strings.Contains(strings.ToLower(item.Name+" "+item.Path), query) {
			filtered = append(filtered, item)
		}
	}
	items = filtered
	sort.Slice(items, func(i, j int) bool {
		if items[i].ModifiedAt == items[j].ModifiedAt {
			return items[i].ID < items[j].ID
		}
		return items[i].ModifiedAt > items[j].ModifiedAt
	})
	start := 0
	if cursor.ID != "" {
		for start < len(items) && (items[start].ModifiedAt > cursor.ModifiedAt || items[start].ModifiedAt == cursor.ModifiedAt && items[start].ID <= cursor.ID) {
			start++
		}
	}
	end := min(len(items), start+limit)
	page := items[start:end]
	next := ""
	if end < len(items) && len(page) > 0 {
		last := page[len(page)-1]
		raw, _ := json.Marshal(localWorksCursor{Application: app, Query: query, Status: status, ModifiedAt: last.ModifiedAt, ID: last.ID})
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	w.Header().Set("Cache-Control", "no-store")
	websession.Respond(w, 200, map[string]any{"items": page, "next_cursor": next, "has_more": next != ""})
}

func (s *localArtifactStore) worksRoutes(owner *websession.Store, mux *http.ServeMux) {
	mux.HandleFunc("GET /api/local/works", owner.Protect(s.listWorks))
	mux.HandleFunc("GET /api/local/works/{id}/preview", owner.Protect(func(w http.ResponseWriter, r *http.Request) { s.serveWork(w, r, true) }))
	mux.HandleFunc("GET /api/local/works/{id}/download", owner.Protect(func(w http.ResponseWriter, r *http.Request) { s.serveWork(w, r, false) }))
	mux.HandleFunc("PATCH /api/local/works/{id}", owner.Protect(s.renameWork))
	mux.HandleFunc("POST /api/local/works/{id}/trash", owner.Protect(s.trashWork))
	mux.HandleFunc("POST /api/local/works/{id}/restore", owner.Protect(s.restoreWork))
}
