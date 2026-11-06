package admin

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/apphttp"
	"wechat-bot/internal/stationruntime"
	"wechat-bot/internal/stationui"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

const localOwnerCookie = "ahs_local_session"

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
	projectTools  *localProjectTools
	windows       *agenthost.WindowsConfig
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
	if dir == "" || workspaceRoot == "" {
		return nil, errors.New("本地主人配置无效")
	}
	auth, err := websession.New(password, origin, localOwnerCookie)
	if errors.Is(err, websession.ErrOrigin) {
		return nil, errors.New("本地主人地址无效")
	}
	if err != nil {
		return nil, errors.New("本地主人配置无效")
	}
	dir, err = filepath.Abs(dir)
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
	var runner *agenthost.WindowsConfig
	if len(windows) > 0 {
		runner, err = stationruntime.BindWindowsRunner(windows[0], dir, workspaceRoot)
		if err != nil {
			return nil, err
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]any{"ok": true, "mode": "local", "wechat_required": false})
	})
	mux.HandleFunc("POST /api/login", auth.Login)
	mux.HandleFunc("GET /api/session", auth.Protect(auth.Status))
	home := &localHomeService{identity: newFishIdentityRegistry(dir), root: workspaceRoot, dataDir: dir, chatAvailable: true, windows: runner}
	home.configureAppCatalog()
	home.appInstallRoutes(auth, mux)
	mux.HandleFunc("GET /api/local/home", auth.Protect(home.status))
	mux.HandleFunc("POST /api/local/home", auth.Protect(home.update))
	chat := newLocalChatService(home)
	chat.windows = runner
	home.projectTools = chat.projectTools
	mux.HandleFunc("POST /api/logout", auth.Protect(func(w http.ResponseWriter, r *http.Request) {
		auth.Logout(w, r)
		chat.projectTools.obs.stop()
	}))
	chat.projectTools.routes(auth, mux)
	home.artifacts = &localArtifactStore{home: home, chat: chat}
	home.artifacts.routes(auth, mux)
	handler := &LocalHomeHandler{Handler: mux, chat: chat, home: home}
	registry, err = home.bindBundledObservationApps(registry)
	if err != nil {
		return nil, err
	}
	home.bilibiliRoutes(auth, mux, registry)
	home.douyinRoutes(auth, mux, registry, chat.ctx)
	registry, handler.wechat, err = bindStationWechat(registry, chat)
	if err != nil {
		return nil, err
	}
	chat.applications = registry
	// Recover a durable live ledger at desktop boot for an already prepared
	// local owner. Recovery reconciles and stops an interrupted old session.
	chat.liveOutput, chat.liveEncoder = stationLiveOutput(dir, workspaceRoot)
	chat.startLiveRecovery()
	chat.observations = apphost.NewObserver(registry, filepath.Join(dir, "app-observations"))
	apphttp.Register(mux, auth, registry)
	if handler.wechat != nil {
		chat.wechat = handler.wechat
		handler.wechat.contactRoutes(auth, mux)
		handler.wechat.groupActionRoutes(auth, mux, home.identity)
		handler.wechat.recallRoutes(auth, mux, home.identity)
		handler.wechat.legacyImportRoutes(auth, mux)
		handler.wechat.attachmentRoutes(auth, mux)
		handler.wechat.shareRoutes(auth, mux)
		handler.wechat.artifactRoutes(auth, mux)
		handler.wechat.deliveryControlRoutes(auth, mux)
		handler.wechat.restore()
	}
	chat.routes(auth, mux)
	// Desktop owns the inherited stop pipe. This authenticated endpoint only
	// proves which local-station process it started; it is intentionally not a
	// public stop surface and it never exposes application-account state.
	mux.HandleFunc("GET /api/local/runtime", auth.Protect(handler.runtime))
	stationui.Register(mux)
	return handler, nil
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
