package admin

import (
	"crypto/sha256"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/workspacefs"
)

// localHomeService owns the companion's home, not an application's account.
// Authentication is provided by the local owner's management session. No
// Native, account Manager, device bridge or model is constructed by this path.
type localHomeService struct {
	identity      *fishIdentityRegistry
	root          string
	dataDir       string
	chatAvailable bool
	appCatalog    string
	appInstallMu  sync.Mutex
	artifacts     *localArtifactStore
}

// NewLocalHomeHandler provides the first application-independent station
// surface. It deliberately does not mount the old WeChat management routes.
// The existing identity file is reused when dir points to an existing station;
// initialization never imports account data or revives application grants.
func NewLocalHomeHandler(dir, workspaceRoot, password, origin string, windows ...*agenthost.WindowsConfig) (*LocalHomeHandler, error) {
	return NewLocalHomeHandlerWithApps(dir, workspaceRoot, password, origin, apphost.Registry{}, windows...)
}

// NewLocalHomeHandlerWithApps attaches registrations already verified by the
// station host. Applications cannot register themselves through the HTTP API.
func NewLocalHomeHandlerWithApps(dir, workspaceRoot, password, origin string, registry apphost.Registry, windows ...*agenthost.WindowsConfig) (*LocalHomeHandler, error) {
	if dir == "" || workspaceRoot == "" || !validPassword(password) {
		return nil, errors.New("本地主人配置无效")
	}
	if origin != "" {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return nil, errors.New("本地主人地址无效")
		}
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	workspaceRoot, err = filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, err
	}
	root, err := workspacefs.EnsurePath(dir)
	if err != nil {
		return nil, errors.New("本机私有配置目录不可用")
	}
	if err = root.Close(); err != nil {
		return nil, err
	}
	s := &Server{cookieName: "ahs_local_session", passwordHash: sha256.Sum256([]byte(password)), origin: origin, sessions: map[string]session{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]any{"ok": true, "mode": "local", "wechat_required": false})
	})
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.protected(s.logout))
	mux.HandleFunc("GET /api/session", s.protected(func(w http.ResponseWriter, r *http.Request) {
		_, value, _ := s.session(r)
		respond(w, http.StatusOK, map[string]any{"authenticated": true, "csrf": value.CSRF})
	}))
	home := &localHomeService{identity: newFishIdentityRegistry(dir), root: workspaceRoot, dataDir: dir, chatAvailable: true}
	home.configureAppCatalog()
	home.appInstallRoutes(s, mux)
	mux.HandleFunc("GET /api/local/home", s.protected(home.status))
	mux.HandleFunc("POST /api/local/home", s.protected(home.update))
	chat := newLocalChatService(home)
	home.artifacts = &localArtifactStore{home: home, chat: chat}
	home.artifacts.routes(s, mux)
	handler := &LocalHomeHandler{Handler: mux, chat: chat, home: home}
	if len(windows) > 0 && windows[0] != nil {
		copy := *windows[0]
		copy.Spool = filepath.Join(dir, "local-execution-spool")
		if err := copy.Validate(); err != nil {
			return nil, errors.New("本机 Windows 运行器配置无效")
		}
		for _, path := range []string{copy.Pythonw, copy.Helper} {
			rel, err := filepath.Rel(workspaceRoot, path)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				return nil, errors.New("宿主运行器不能放在大肥鱼可修改的工作空间")
			}
			if resolved, err := filepath.EvalSymlinks(path); err == nil && filepath.Clean(resolved) != filepath.Clean(path) {
				return nil, errors.New("宿主运行器路径不能经过链接")
			}
		}
		chat.windows = &copy
	}
	registry, handler.wechat, err = bindStationWechat(registry, chat)
	if err != nil {
		return nil, err
	}
	s.appRoutes(mux, registry)
	if handler.wechat != nil {
		chat.wechat = handler.wechat
		handler.wechat.contactRoutes(s, mux)
		handler.wechat.groupActionRoutes(s, mux, home.identity)
		handler.wechat.recallRoutes(s, mux, home.identity)
		handler.wechat.legacyImportRoutes(s, mux)
		handler.wechat.attachmentRoutes(s, mux)
		handler.wechat.shareRoutes(s, mux)
		handler.wechat.artifactRoutes(s, mux)
		handler.wechat.deliveryControlRoutes(s, mux)
		handler.wechat.restore()
	}
	chat.routes(s, mux)
	// Desktop owns the inherited stop pipe. This authenticated endpoint only
	// proves which local-station process it started; it is intentionally not a
	// public stop surface and it never exposes application-account state.
	mux.HandleFunc("GET /api/local/runtime", s.protected(handler.runtime))
	for route, asset := range map[string]string{
		"/": "local-home.html", "/assets/local-home.js": "local-home.js", "/assets/local-home.css": "local-home.css",
		"/assets/whale-girl-cutout.png": "whale-girl-cutout.png", "/assets/night-sea.webp": "night-sea.webp",
	} {
		mux.HandleFunc("GET "+route, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != route {
				http.NotFound(w, r)
				return
			}
			data, err := assets.ReadFile("static/" + asset)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			switch filepath.Ext(asset) {
			case ".js":
				w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			case ".css":
				w.Header().Set("Content-Type", "text/css; charset=utf-8")
			case ".html":
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
			}
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
			_, _ = w.Write(data)
		})
	}
	return handler, nil
}

func (s *Server) localHomeRoutes(mux *http.ServeMux, identity *fishIdentityRegistry, root, dataDir string) {
	home := &localHomeService{identity: identity, root: root, dataDir: dataDir}
	home.configureAppCatalog()
	home.appInstallRoutes(s, mux)
	mux.HandleFunc("GET /api/local/home", s.protected(home.status))
	mux.HandleFunc("POST /api/local/home", s.protected(home.update))
}

func (h *localHomeService) directory(record fishIdentityRecord) (string, error) {
	if !validFishID(record.FishID) || record.LocalOwner == nil {
		return "", errors.New("请先为大肥鱼准备家目录")
	}
	// The home is a sibling of the legacy account-private subtree, never its
	// parent. No account or WeChat requester is part of this grant's identity.
	home := filepath.Join(h.root, "fish_home", record.FishID, "home")
	rel, err := filepath.Rel(home, h.dataDir)
	// A workspace on a different Windows volume cannot contain the private
	// settings directory; filepath.Rel quite correctly cannot represent it.
	if err != nil && !strings.EqualFold(filepath.VolumeName(home), filepath.VolumeName(h.dataDir)) {
		return home, nil
	}
	if err != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", errors.New("家目录不能包含宿主私有配置")
	}
	return home, nil
}

func (h *localHomeService) ensureDirectory(record fishIdentityRecord) error {
	path, err := h.directory(record)
	if err != nil {
		return err
	}
	root, err := workspacefs.EnsurePath(path)
	if err != nil {
		return errors.New("家目录暂不可用，身份已保留，请检查目录后重试")
	}
	defer root.Close()
	for _, name := range []string{"scratch", "diary", "projects", "works", "Project Tools", "Connected Apps"} {
		child, err := workspacefs.EnsureDirectory(root, name)
		if err != nil {
			return errors.New("家目录准备未完成，身份与已有文件保留")
		}
		if err := child.Close(); err != nil {
			return err
		}
	}
	return nil
}

func (h *localHomeService) response(w http.ResponseWriter, record fishIdentityRecord) {
	result := map[string]any{"initialized": record.LocalOwner != nil, "home_access": false, "mode": "local", "chat_available": h.chatAvailable}
	if record.LocalOwner != nil {
		result["fish_id"], result["owner_id"] = record.FishID, record.LocalOwner.ID
		result["revision"], result["home_access"] = record.LocalOwner.Revision, record.LocalOwner.HomeAccess
		path, err := h.directory(record)
		if err != nil {
			fail(w, http.StatusConflict, err.Error())
			return
		}
		result["directory"] = path
	}
	w.Header().Set("Cache-Control", "no-store")
	respond(w, http.StatusOK, result)
}

func (h *localHomeService) status(w http.ResponseWriter, r *http.Request) {
	record, err := h.identity.localIdentity()
	if err != nil {
		fail(w, http.StatusConflict, "本机身份记录不可读，原文件保留")
		return
	}
	h.response(w, record)
}

func (h *localHomeService) update(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action   string `json:"action"`
		Revision uint64 `json:"revision,omitempty"`
	}
	if !decode(w, r, &in) {
		return
	}
	var record fishIdentityRecord
	var err error
	switch in.Action {
	case "initialize":
		record, err = h.identity.initializeLocalIdentity()
	case "authorize", "pause":
		record, err = h.identity.setLocalHomeAccess(in.Revision, in.Action == "authorize")
	default:
		fail(w, http.StatusBadRequest, "请选择准备、暂停或恢复家目录授权")
		return
	}
	if err != nil {
		fail(w, http.StatusConflict, "身份或授权已变更，请重新读取后操作")
		return
	}
	if record.LocalOwner.HomeAccess {
		if err = h.ensureDirectory(record); err != nil {
			fail(w, http.StatusServiceUnavailable, err.Error())
			return
		}
	}
	// Report the latest revision if another owner page changed the grant while
	// directory preparation was in progress. Never revive it from a stale copy.
	h.status(w, r)
}
