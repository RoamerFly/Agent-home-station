package station

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/fishidentity"
	"wechat-bot/internal/stationauth"
	"wechat-bot/internal/stationhost"
	"wechat-bot/internal/stationruntime"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

const localOwnerCookie = "ahs_local_session"

// LocalHomeHandler wires the station's local conversation and identity into
// the shared HTTP and shutdown host.
type LocalHomeHandler struct {
	*stationhost.Handler
	chat *localChatService
	home *localHomeService
}

// SetApplicationPackagesDirectory selects the externally managed public
// application package root. The directory is read and verified on demand;
// application source is never copied into the station data directory.
func (h *LocalHomeHandler) SetApplicationPackagesDirectory(directory string) {
	if h == nil || h.home == nil || directory == "" {
		return
	}
	h.home.appCatalog = filepath.Clean(directory)
}

// localHomeService owns the companion's home, not an application's account.
// Authentication is provided by the local owner's management session. No
// Native, account Manager, device bridge or model is constructed by this path.
type localHomeService struct {
	identity      *fishidentity.Registry
	root          string
	dataDir       string
	chatAvailable bool
	toolPackages  string
	appCatalog    string
	appInstallMu  sync.Mutex
	artifacts     *localArtifactStore
	projectTools  *localProjectTools
	windows       *agenthost.WindowsConfig
	douyinWeb     *localDouyinWebLogin
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
	return NewLocalHomeHandlerWithPackageDirectories(dir, workspaceRoot, password, origin, registry, "", "", windows...)
}

// NewLocalHomeHandlerWithPackageDirectories wires externally managed public
// package roots before applications are attached and restored. The station
// reads these roots on demand; it never copies them into private data.
func NewLocalHomeHandlerWithPackageDirectories(dir, workspaceRoot, password, origin string, registry apphost.Registry, toolPackagesDirectory, applicationPackagesDirectory string, windows ...*agenthost.WindowsConfig) (*LocalHomeHandler, error) {
	if dir == "" || workspaceRoot == "" {
		return nil, errors.New("本地主人配置无效")
	}
	for name, directory := range map[string]string{"公共工具包目录": toolPackagesDirectory, "公共应用包目录": applicationPackagesDirectory} {
		if directory != "" && !filepath.IsAbs(directory) {
			return nil, errors.New(name + "必须是绝对路径")
		}
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
	var fenceErr error
	if entry, ok := registry.Lookup(wechatapp.ID); ok && entry.Registration == "station-native" {
		if _, err := root.Lstat(stationNativeLifecycleRelative(dir, stationNativeReleaseFile)); !errors.Is(err, os.ErrNotExist) {
			fenceErr = errors.New("微信原生启动已禁用或无法核对解除交接记录")
		}
	}
	if err = errors.Join(fenceErr, root.Close()); err != nil {
		return nil, err
	}
	if err = migrateLegacyAppPersona(dir); err != nil {
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
	stationauth.RegisterPassword(mux, auth, AuthorityRoot(dir))
	home := &localHomeService{identity: fishidentity.New(stationAuthorityDir(dir)), root: workspaceRoot, dataDir: dir, chatAvailable: true, windows: runner,
		toolPackages: toolPackagesDirectory, appCatalog: applicationPackagesDirectory}
	if err := home.migrateSingleHome(); err != nil {
		return nil, err
	}
	home.registerHomeRoutes(auth, mux)
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
	registry, apps, err := chat.attachApplications(registry)
	if err != nil {
		chat.BeginShutdown()
		return nil, err
	}
	chat.applications = registry
	// Recover a durable live ledger at desktop boot for an already prepared
	// local owner. Recovery reconciles and stops an interrupted old session.
	chat.liveOutput, chat.liveSource, chat.liveEncoder = stationLiveOutput(dir, workspaceRoot, chat.livePrograms)
	chat.obsDeploymentRoutes(auth, mux)
	chat.startLiveRecovery()
	chat.observations = apphost.NewObserver(registry, stationObservationsDir(dir))
	chat.routes(auth, mux)
	chat.startLocalTaskFollowups()
	chat.startSurfActionContinuations()
	return &LocalHomeHandler{Handler: stationhost.New(mux, auth, registry, dir, chat, apps...), chat: chat, home: home}, nil
}

// RegisterLegacyHomeRoutes retains the home settings in the existing management
// session for old installations. The standalone station has its own session.
func RegisterLegacyHomeRoutes(auth *websession.Store, mux *http.ServeMux, identity *fishidentity.Registry, root, dataDir string) {
	home := &localHomeService{identity: identity, root: root, dataDir: dataDir}
	home.registerHomeRoutes(auth, mux)
}

func (home *localHomeService) registerHomeRoutes(auth *websession.Store, mux *http.ServeMux) {
	home.configureAppCatalog()
	home.appInstallRoutes(auth, mux)
	mux.HandleFunc("GET /api/local/home", auth.Protect(home.status))
	mux.HandleFunc("POST /api/local/home", auth.Protect(home.update))
}

func (h *localHomeService) directory(record fishidentity.Record) (string, error) {
	if !fishidentity.ValidFishID(record.FishID) || record.LocalOwner == nil {
		return "", errors.New("请先为大肥鱼准备家目录")
	}
	// The home is a sibling of the legacy account-private subtree, never its
	// parent. No account or WeChat requester is part of this grant's identity.
	home := filepath.Join(h.root, "home")
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

// Keep the durable identity, but do not encode it in a single resident's path.
// Rename only a complete old home; conflicting destinations retain both trees.
func (h *localHomeService) migrateSingleHome() error {
	record, err := h.identity.Read()
	if err != nil || record.LocalOwner == nil {
		return err
	}
	oldCatalog := filepath.Join(stationPublicToolsDir(h.dataDir), record.FishID)
	newCatalog := filepath.Join(stationPublicToolsDir(h.dataDir), "catalog")
	if _, err := os.Lstat(oldCatalog); err == nil {
		if _, err := os.Lstat(newCatalog); !errors.Is(err, os.ErrNotExist) {
			return errors.New("新旧工具登记目录同时存在，请先合并")
		}
		check, err := workspacefs.EnsurePath(oldCatalog)
		if err != nil {
			return err
		}
		if err := check.Close(); err != nil {
			return err
		}
		if err := os.Rename(oldCatalog, newCatalog); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	destination, err := h.directory(record)
	if err != nil {
		return err
	}
	old := filepath.Join(h.root, "fish_home", record.FishID, "home")
	if _, err := os.Lstat(old); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := os.Lstat(destination); err == nil {
		return errors.New("新旧家目录同时存在，请先合并内容")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Validate all existing ancestors before moving a private workspace.
	check, err := workspacefs.EnsurePath(old)
	if err != nil {
		return err
	}
	if err := check.Close(); err != nil {
		return err
	}
	if err := os.Rename(old, destination); err != nil {
		return err
	}
	_ = os.Remove(filepath.Dir(old)) // Empty containers only; never recursive.
	_ = os.Remove(filepath.Dir(filepath.Dir(old)))
	return nil
}

func (h *localHomeService) ensureDirectory(record fishidentity.Record) error {
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

func (h *localHomeService) response(w http.ResponseWriter, record fishidentity.Record) {
	result := map[string]any{"initialized": record.LocalOwner != nil, "home_access": false, "mode": "local", "chat_available": h.chatAvailable}
	if record.LocalOwner != nil {
		result["fish_id"], result["owner_id"] = record.FishID, record.LocalOwner.ID
		result["revision"], result["home_access"] = record.LocalOwner.Revision, record.LocalOwner.HomeAccess
		path, err := h.directory(record)
		if err != nil {
			websession.Fail(w, http.StatusConflict, err.Error())
			return
		}
		result["directory"] = path
	}
	w.Header().Set("Cache-Control", "no-store")
	websession.Respond(w, http.StatusOK, result)
}

func (h *localHomeService) status(w http.ResponseWriter, r *http.Request) {
	record, err := h.identity.Read()
	if err != nil {
		websession.Fail(w, http.StatusConflict, "本机身份记录不可读，原文件保留")
		return
	}
	h.response(w, record)
}

func (h *localHomeService) update(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action   string `json:"action"`
		Revision uint64 `json:"revision,omitempty"`
	}
	if !websession.Decode(w, r, &in) {
		return
	}
	var record fishidentity.Record
	var err error
	switch in.Action {
	case "initialize":
		record, err = h.identity.InitializeLocal()
	case "authorize", "pause":
		record, err = h.identity.SetHomeAccess(in.Revision, in.Action == "authorize")
	default:
		websession.Fail(w, http.StatusBadRequest, "请选择准备、暂停或恢复家目录授权")
		return
	}
	if err != nil {
		websession.Fail(w, http.StatusConflict, "身份或授权已变更，请重新读取后操作")
		return
	}
	if record.LocalOwner.HomeAccess {
		if err = h.ensureDirectory(record); err != nil {
			websession.Fail(w, http.StatusServiceUnavailable, err.Error())
			return
		}
	}
	// Report the latest revision if another owner page changed the grant while
	// directory preparation was in progress. Never revive it from a stale copy.
	h.status(w, r)
}
