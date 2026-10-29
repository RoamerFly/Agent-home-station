package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"wechat-bot/internal/linuxnative"
)

//go:embed static/*
var assets embed.FS

type session struct {
	CSRF    string
	Expires time.Time
}
type Server struct {
	authRoot      *Server
	accounts      *accountRegistry
	catalogMu     sync.Mutex
	catalogJob    *catalogRefresh
	manager       *Manager
	passwordHash  [32]byte
	origin        string
	mu            sync.Mutex
	sessions      map[string]session
	loginWindow   time.Time
	loginAttempts int
	loginMu       sync.Mutex
	operation     string
	loginUnknown  bool
}

func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("system random source unavailable")
	}
	return hex.EncodeToString(b[:])
}

func NewHandler(manager *Manager, password, origin string) (http.Handler, error) {
	return newHandler(manager, password, origin, nil)
}
func newHandler(manager *Manager, password, origin string, auth *Server) (http.Handler, error) {
	if manager == nil || auth == nil && !validPassword(password) {
		return nil, errors.New("invalid admin authentication configuration")
	}
	if origin != "" {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return nil, errors.New("invalid public admin origin")
		}
	}
	s := &Server{manager: manager, passwordHash: sha256.Sum256([]byte(password)), origin: origin, sessions: map[string]session{}}
	s.authRoot = auth
	if auth == nil {
		var err error
		s.accounts, err = newAccountRegistry(s)
		if err != nil {
			return nil, err
		}
		manager.accounts = s.accounts
	}
	mux := http.NewServeMux()
	if s.accounts != nil {
		s.accounts.routes(mux)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]bool{"ok": true}) })
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.protected(s.logout))
	mux.HandleFunc("POST /api/password", s.protected(s.changePassword))
	mux.HandleFunc("GET /api/session", s.protected(func(w http.ResponseWriter, r *http.Request) {
		_, session, _ := s.session(r)
		respond(w, 200, map[string]any{"authenticated": true, "csrf": session.CSRF})
	}))
	mux.HandleFunc("GET /api/config", s.protected(func(w http.ResponseWriter, r *http.Request) { respond(w, 200, s.manager.Config()) }))
	mux.HandleFunc("POST /api/config", s.protected(s.saveConfig))
	mux.HandleFunc("GET /api/status", s.protected(s.status))
	mux.HandleFunc("GET /api/logs", s.protected(func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]any{"items": s.manager.displayLogs(r.Context())})
	}))
	mux.HandleFunc("POST /api/bot/start", s.protected(func(w http.ResponseWriter, r *http.Request) { s.toggle(w, r, true) }))
	mux.HandleFunc("POST /api/bot/stop", s.protected(func(w http.ResponseWriter, r *http.Request) { s.toggle(w, r, false) }))
	mux.HandleFunc("POST /api/model/test", s.protected(s.testModel))
	mux.HandleFunc("GET /api/groups", s.protected(s.groups))
	s.companionRoutes(mux)
	s.billingRoutes(mux)
	s.agentRoutes(mux)
	s.repairTestRoutes(mux)
	s.ownerRequestRoutes(mux)
	s.mobileRoutes(mux)
	s.storageRoutes(mux)
	s.speechRoutes(mux)
	s.channelsRoutes(mux)
	s.contextRoutes(mux)
	s.canvasRoutes(mux)
	s.toolRoutes(mux)
	s.libraryRoutes(mux)
	s.voiceLibraryRoutes(mux)
	s.catalogRoutes(mux)
	s.catalogSyncRoutes(mux)
	s.actionRoutes(mux)
	s.mcpRoutes(mux)
	s.pluginRoutes(mux)
	s.surfRoutes(mux)
	s.mediaRuntimeRoutes(mux)
	mux.HandleFunc("POST /api/wechat/login", s.protected(s.beginLogin))
	mux.HandleFunc("GET /api/wechat/login/{id}", s.protected(s.loginStatus))
	mux.HandleFunc("GET /api/wechat/login/{id}/qr", s.protected(s.qr))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.NotFound(w, r)
			return
		}
		name := r.URL.Path
		switch name {
		case "/":
			name = "index.html"
		case "/app.js", "/assets/app.js":
			name = "app.js"
		case "/assets/voices.js":
			name = "voices.js"
		case "/assets/voices.css":
			name = "voices.css"
		case "/assets/theme.js":
			name = "theme.js"
		case "/assets/tools.js":
			name = "tools.js"
		case "/assets/catalog.js":
			name = "catalog.js"
		case "/assets/accounts.js":
			name = "accounts.js"
		case "/assets/catalog-sync.js":
			name = "catalog-sync.js"
		case "/assets/actions.js":
			name = "actions.js"
		case "/assets/mcp.js":
			name = "mcp.js"
		case "/assets/surf.js":
			name = "surf.js"
		case "/assets/proactive.js":
			name = "proactive.js"
		case "/assets/billing.js":
			name = "billing.js"
		case "/assets/storage.js":
			name = "storage.js"
		case "/assets/workbench.js":
			name = "workbench.js"
		case "/assets/flow-model.js":
			name = "flow-model.js"
		case "/assets/flow-default.json":
			name = "flow-default.json"
		case "/assets/workbench.css":
			name = "workbench.css"
		case "/assets/dialogs.js":
			name = "dialogs.js"
		case "/assets/conversations.js":
			name = "conversations.js"
		case "/assets/rounds.js":
			name = "rounds.js"
		case "/assets/console.css":
			name = "console.css"
		case "/assets/logs.js":
			name = "logs.js"
		case "/assets/ui.js":
			name = "ui.js"
		case "/assets/channels.js":
			name = "channels.js"
		case "/assets/speech.js":
			name = "speech.js"
		case "/assets/agent.js":
			name = "agent.js"
		case "/assets/agent-workspace.js":
			name = "agent-workspace.js"
		case "/assets/mobile.js":
			name = "mobile.js"
		case "/assets/plugins.js":
			name = "plugins.js"
		case "/assets/whale-girl.png":
			name = "whale-girl.png"
		case "/assets/whale-girl-cutout.png":
			name = "whale-girl-cutout.png"
		case "/app.css", "/assets/app.css":
			name = "app.css"
		default:
			http.NotFound(w, r)
			return
		}
		data, err := assets.ReadFile("static/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		switch name {
		case "index.html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case "voices.js", "accounts.js", "app.js", "theme.js", "tools.js", "catalog.js", "catalog-sync.js", "actions.js", "mcp.js", "plugins.js", "ui.js", "agent.js", "agent-workspace.js", "mobile.js", "speech.js", "dialogs.js", "conversations.js", "workbench.js", "flow-model.js", "rounds.js", "logs.js", "surf.js", "proactive.js", "billing.js", "storage.js", "channels.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		case "flow-default.json":
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
		case "whale-girl.png", "whale-girl-cutout.png":
			w.Header().Set("Content-Type", "image/png")
		case "voices.css", "app.css", "console.css", "workbench.css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		}
		_, _ = w.Write(data)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' blob:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if s.accounts != nil {
			s.accounts.selectAccount(w, r, mux)
		} else {
			mux.ServeHTTP(w, r)
		}
	}), nil
}

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}
func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		fail(w, 415, "请求需要使用 JSON")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(value) != nil {
		fail(w, 400, "请求格式无效或过大")
		return false
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		fail(w, 400, "请求需要一个完整 JSON 对象")
		return false
	}
	return true
}
func (s *Server) sameOrigin(r *http.Request) bool {
	want := s.origin
	if want == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		want = scheme + "://" + r.Host
	}
	return r.Header.Get("Origin") == want
}
func (s *Server) session(r *http.Request) (string, session, bool) {
	if s.authRoot != nil {
		return s.authRoot.session(r)
	}
	cookie, err := r.Cookie("wechat_admin_session")
	if err != nil {
		return "", session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.sessions[cookie.Value]
	if !ok || time.Now().After(value.Expires) {
		delete(s.sessions, cookie.Value)
		return "", session{}, false
	}
	return cookie.Value, value, true
}
func (s *Server) protected(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, value, ok := s.session(r)
		if !ok {
			fail(w, 401, "请先登录管理页面")
			return
		}
		if r.Method != "GET" {
			header := r.Header.Get("X-CSRF-Token")
			if !s.sameOrigin(r) || len(header) != len(value.CSRF) || subtle.ConstantTimeCompare([]byte(header), []byte(value.CSRF)) != 1 {
				fail(w, 403, "页面验证失效，请刷新后重试")
				return
			}
		}
		next(w, r)
	}
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		fail(w, 403, "不接受跨站登录请求")
		return
	}
	s.mu.Lock()
	if time.Since(s.loginWindow) > time.Minute {
		s.loginWindow = time.Now()
		s.loginAttempts = 0
	}
	s.loginAttempts++
	limited := s.loginAttempts > 10
	s.mu.Unlock()
	if limited {
		fail(w, 429, "登录尝试过多，请一分钟后再试")
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &input) {
		return
	}
	hash := sha256.Sum256([]byte(input.Password))
	s.mu.Lock()
	if subtle.ConstantTimeCompare(hash[:], s.passwordHash[:]) != 1 {
		s.mu.Unlock()
		fail(w, 401, "管理密码不正确")
		return
	}
	id, csrf := randomToken(), randomToken()
	for key, value := range s.sessions {
		if time.Now().After(value.Expires) {
			delete(s.sessions, key)
		}
	}
	if len(s.sessions) >= 32 {
		for key := range s.sessions {
			delete(s.sessions, key)
			break
		}
	}
	s.sessions[id] = session{csrf, time.Now().Add(8 * time.Hour)}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "wechat_admin_session", Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || strings.HasPrefix(s.origin, "https://"), MaxAge: 8 * 3600})
	respond(w, 200, map[string]any{"authenticated": true, "csrf": csrf})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	id, _, _ := s.session(r)
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "wechat_admin_session", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	respond(w, 200, map[string]bool{"ok": true})
}
func (s *Server) saveConfig(w http.ResponseWriter, r *http.Request) {
	var input ConfigInput
	if !decode(w, r, &input) {
		return
	}
	view, err := s.manager.Save(input)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	respond(w, 200, view)
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	native, err := s.manager.native.Status(r.Context())
	if err != nil {
		native = linuxnative.Status{Connection: "unavailable"}
	}
	name := ""
	if resolver, ok := s.manager.native.(interface {
		DisplayName(context.Context) (string, error)
	}); ok {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		name, _ = resolver.DisplayName(ctx)
		cancel()
	}
	respond(w, 200, map[string]any{"native": native, "bot": s.manager.BotStatus(), "key_configured": s.manager.Config().KeyConfigured, "account": s.manager.native.Account(), "account_name": name, "version": "0.35.0-managed-workspaces-local"})
}
func (s *Server) toggle(w http.ResponseWriter, r *http.Request, enabled bool) {
	if err := s.manager.Toggle(enabled); err != nil {
		fail(w, 400, err.Error())
		return
	}
	s.status(w, r)
}
func (s *Server) testModel(w http.ResponseWriter, r *http.Request) {
	if err := s.manager.TestModel(r.Context()); err != nil {
		fail(w, 400, err.Error())
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}
func (s *Server) groups(w http.ResponseWriter, r *http.Request) {
	items, err := s.manager.native.Groups(r.Context(), r.URL.Query().Get("search"))
	if err != nil {
		fail(w, 502, "群目录暂不可用，请检查登录与同步状态，或缩小搜索范围")
		return
	}
	respond(w, 200, map[string]any{"items": items})
}
func terminal(state string) bool {
	switch state {
	case "succeeded", "failed", "cancelled", "expired", "rejected", "needs_login", "challenge_required", "approval_required", "needs_review":
		return true
	}
	return false
}
func (s *Server) beginLogin(w http.ResponseWriter, r *http.Request) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	if s.loginUnknown {
		fail(w, 409, "上一次登录提交结果未知，请核对手机和后台状态后再处理，未自动重试")
		return
	}
	if s.operation != "" {
		op, err := s.manager.native.LoginStatus(r.Context(), s.operation)
		if err != nil {
			fail(w, 502, "暂时无法核对原登录操作，未重复提交，请稍后查询")
			return
		}
		if !terminal(op.State) {
			respond(w, 200, op)
			return
		}
	}
	op, err := s.manager.native.BeginLogin(r.Context(), randomToken())
	if err != nil {
		s.loginUnknown = errors.Is(err, linuxnative.ErrLoginOutcomeUnknown)
		fail(w, 502, "登录请求未取得确定结果，未自动重试；请检查后台状态")
		return
	}
	s.operation = op.ID
	s.manager.log("wechat_qr_login_requested")
	respond(w, 200, op)
}
func (s *Server) loginStatus(w http.ResponseWriter, r *http.Request) {
	op, err := s.manager.native.LoginStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 502, "暂时无法读取登录状态")
		return
	}
	respond(w, 200, op)
}
func (s *Server) qr(w http.ResponseWriter, r *http.Request) {
	generation, err := strconv.Atoi(r.URL.Query().Get("generation"))
	if err != nil {
		fail(w, 400, "二维码代次无效")
		return
	}
	data, err := s.manager.native.LoginQR(r.Context(), r.PathValue("id"), generation)
	if err != nil {
		fail(w, 502, "二维码尚未生成或已经过期")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(data)
}
