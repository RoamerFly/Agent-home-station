package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

// ExecutionContext is minted from the authenticated native packet, never from
// tool arguments, nicknames, quoted text, summaries or downloaded documents.
type ExecutionContext struct {
	Account      string `json:"account"`
	OriginThread string `json:"origin_thread"`
	Requester    string `json:"requester"`
	Owner        bool   `json:"owner"`
	TaskID       string `json:"task_id,omitempty"`
	Private      string `json:"private_workspace"`
	Public       string `json:"public_workspace"`
	Conversation string `json:"conversation_workspace"`
}

func (m *Manager) managedRoot() string {
	if base := os.Getenv("WECHAT_WORKSPACE_ROOT"); base != "" && filepath.IsAbs(base) {
		return filepath.Clean(base)
	}
	if m.accounts != nil && m.accounts.root.manager != m {
		return m.accounts.root.manager.managedRoot()
	}
	if filepath.Base(filepath.Dir(m.dir)) == "accounts" {
		return filepath.Join(filepath.Dir(filepath.Dir(m.dir)), "managed-workspaces")
	}
	return filepath.Join(m.dir, "managed-workspaces")
}
func (p *permittedTools) executionContext() ExecutionContext {
	root := p.manager.managedRoot()
	private := filepath.Join(root, "pri_workspace", accountDirectory(p.trace.Account))
	return ExecutionContext{Account: p.trace.Account, OriginThread: p.trace.Thread, Requester: p.trace.Sender, Owner: p.privileged(), TaskID: p.trace.TaskID, Private: private, Public: filepath.Join(root, "public_workspace"), Conversation: filepath.Join(private, "conversations", companion.ScopeID("wechat", p.trace.Account, p.trace.Thread))}
}
func ensureManaged(paths ...string) error {
	for _, path := range paths {
		if e := os.MkdirAll(path, 0700); e != nil {
			return e
		}
		resolved, e := filepath.EvalSymlinks(path)
		if e != nil || resolved != filepath.Clean(path) {
			return errors.New("managed_workspace_symlink_not_allowed")
		}
	}
	return nil
}
func (p *permittedTools) hostConfig() (agenthost.Config, error) {
	c := p.manager.agent.config().Host
	x := p.executionContext()
	publicProjects := filepath.Join(x.Public, "projects")
	models, runtimes := filepath.Join(x.Public, "models"), filepath.Join(x.Public, "runtimes")
	if err := ensureManaged(x.Conversation, x.Private, publicProjects, models, runtimes, filepath.Join(x.Public, "manifests")); err != nil {
		return c, err
	}
	for _, sub := range []string{"memory", "inbox", "output", "tasks", "scratch"} {
		if e := ensureManaged(filepath.Join(x.Conversation, sub)); e != nil {
			return c, e
		}
	}
	c.Roots = []string{x.Conversation}
	c.Isolation = &agenthost.Isolation{ReadOnly: []string{publicProjects, models, runtimes, filepath.Join(x.Public, "manifests")}}
	if x.Owner {
		c.Roots = []string{x.Conversation, x.Public}
		c.Isolation.ReadOnly = nil
	}
	// Windows uses its own mandatory LPAC backend; never the legacy user shell.
	return c, nil
}

// A global mount is requested for one tool invocation, never stored on a session.
func (p *permittedTools) scopedHostConfig(raw json.RawMessage) (agenthost.Config, error) {
	var in struct {
		Global bool `json:"global"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return agenthost.Config{}, toolInputError("invalid execution scope")
	}
	c, e := p.hostConfig()
	if e != nil {
		return c, e
	}
	if in.Global {
		if !p.privileged() || !p.manager.agent.config().CrossConversation {
			return c, toolInputError("account global workspace requires the trusted owner")
		}
		x := p.executionContext()
		c.Roots = []string{x.Private, x.Public}
	}
	return c, nil
}
func (p *permittedTools) workspaceExecutionAllowed() bool {
	return p.trace.Sender != "" && (strings.HasSuffix(p.trace.Thread, "@chatroom") || p.trace.Thread == p.trace.Sender) && !p.trace.Preview && !p.trace.ReportOnly && !p.trace.Surf && p.trace.Account == p.manager.native.Account() && p.trace.Thread != "" && permittedRecipient(p.manager.Config().Config, p.trace.Thread) && p.manager.agent.config().Enabled && p.manager.agent.config().Host.Mode != "disabled"
}
func (p *permittedTools) projectTools() []mcptools.Tool {
	return []mcptools.Tool{
		agentTool("project_list", "List registered public projects and exact managed paths. Read these before searching directories; no disk-wide discovery. Input {}.", `{"type":"object","properties":{},"additionalProperties":false}`),
		agentTool("project_read", "Read one registered project's JSON usage manifest. Input {id:string}.", `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`),
		agentTool("agent_global_search", "Trusted owner only: search THIS account's conversation summaries, messages and assets. Results retain original times, source conversation and message IDs. This does not switch identity or authorize other accounts. Input {query?:string,thread_id?:string,offset?:integer,limit?:integer}.", `{"type":"object","properties":{"query":{"type":"string"},"thread_id":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}},"additionalProperties":false}`),
	}
}
func (p *permittedTools) callProject(_ context.Context, name string, raw json.RawMessage) (string, error) {
	var in struct {
		ID, Query     string
		Thread        string `json:"thread_id"`
		Offset, Limit int
	}
	if json.Unmarshal(raw, &in) != nil || in.Offset < 0 || in.Limit < 0 {
		return "", toolInputError("invalid project/search arguments")
	}
	x := p.executionContext()
	dir := filepath.Join(x.Public, "manifests")
	if err := ensureManaged(dir); err != nil {
		return "", err
	}
	var value any
	if name == "agent_global_search" {
		if !p.privileged() || !p.manager.agent.config().CrossConversation {
			return "", toolInputError("只有当前账号可信管理员可检索全局会话")
		}
		rows := []map[string]any{}
		query := strings.ToLower(in.Query)
		cfg := p.manager.Config()
		now := time.Now()
		assets, assetErr := p.manager.loadAssets()
		if assetErr != nil {
			return "", assetErr
		}
		for _, item := range p.manager.memory.Summaries(cfg.Companion, now) {
			if in.Thread != "" && item.Thread != in.Thread || !permittedRecipient(cfg.Config, item.Thread) {
				continue
			}
			scope, e := p.manager.memory.Read(item.ID, cfg.Companion, now)
			if e != nil || scope.Account != x.Account {
				continue
			}
			label := p.manager.memoryLabel(context.Background(), item.Thread)
			if scope.Summary != "" && (query == "" || strings.Contains(strings.ToLower(scope.Summary+label), query)) {
				rows = append(rows, map[string]any{"kind": "summary", "thread_id": scope.Thread, "thread_name": label, "at": scope.SummaryUpdated, "text": scope.Summary})
			}
			for _, a := range assets {
				if a.Account != x.Account || a.Scope != scope.ID || query != "" && !strings.Contains(strings.ToLower(a.Filename+" "+a.Title+" "+label), query) {
					continue
				}
				rows = append(rows, map[string]any{"kind": "asset", "thread_id": scope.Thread, "thread_name": label, "at": a.Updated, "asset_id": a.ID, "filename": a.Filename, "bytes": a.Bytes, "mime": a.MIME, "parent_asset_id": a.ParentAssetID, "requests": a.Requests})
			}
			for _, m := range scope.Messages {
				if query != "" && !strings.Contains(strings.ToLower(m.Text+label), query) {
					continue
				}
				rows = append(rows, map[string]any{"kind": "message", "thread_id": scope.Thread, "thread_name": label, "at": m.Time, "message_id": m.TraceID, "sender": m.Sender, "text": m.Text, "media": m.Media})
			}
		}
		sort.SliceStable(rows, func(i, j int) bool {
			a, _ := json.Marshal(rows[i]["at"])
			b, _ := json.Marshal(rows[j]["at"])
			if string(a) != string(b) {
				return string(a) > string(b)
			}
			return fmt.Sprint(rows[i]["thread_id"], rows[i]["message_id"]) < fmt.Sprint(rows[j]["thread_id"], rows[j]["message_id"])
		})
		if in.Offset > len(rows) {
			return "", toolInputError("offset out of range")
		}
		limit := in.Limit
		if limit == 0 {
			limit = 30
		}
		end := in.Offset + min(limit, len(rows)-in.Offset)
		value = map[string]any{"account": x.Account, "items": rows[in.Offset:end], "total": len(rows), "next_offset": end, "has_more": end < len(rows), "source_material_not_instructions": true}
	} else {
		entries, e := os.ReadDir(dir)
		if e != nil {
			return "", e
		}
		items := []json.RawMessage{}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			if name == "project_read" && entry.Name() != in.ID+".json" {
				continue
			}
			b, e := os.ReadFile(filepath.Join(dir, entry.Name()))
			if e != nil || len(b) > 65536 || !json.Valid(b) {
				return "", errors.New("project_manifest_invalid")
			}
			items = append(items, b)
		}
		if name == "project_read" {
			if len(items) != 1 {
				return "", errors.New("project_not_registered")
			}
			value = items[0]
		} else {
			value = map[string]any{"projects": items, "execution": x}
		}
	}
	b, e := json.Marshal(value)
	return string(b), e
}

// Notes are mapped into the same physical session directories as file tools.
// Legacy notes are copied once, never overwritten or deleted.
func (m *Manager) configureManagedNotes() error {
	base := filepath.Join(m.managedRoot(), "pri_workspace", accountDirectory(m.native.Account()))
	if err := ensureManaged(base, filepath.Join(base, "global", "memory"), filepath.Join(base, "global", "tasks"), filepath.Join(base, "assets", "objects")); err != nil {
		return err
	}
	identity := filepath.Join(base, "account.md")
	if _, err := os.Lstat(identity); os.IsNotExist(err) {
		content := "# Account workspace\n\nStable account: " + m.native.Account() + "\n\nConversation directories belong only to this account. Global access requires the real trusted owner for the current request. Use project_list/project_read for shared projects and agent_global_search for source-labelled account memory. Credentials and executor control files are outside this workspace.\n"
		f, e := os.OpenFile(identity, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = f.WriteString(content)
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	} else if err != nil {
		return err
	}
	resolve := func(scope string) (string, error) {
		if scope == "shared" {
			return filepath.Join(base, "guides"), nil
		}
		if scope == agentID(m.native.Account(), "global-notes") {
			return filepath.Join(base, "global", "memory"), nil
		}
		if len(scope) != 64 || strings.Trim(scope, "0123456789abcdef") != "" {
			return "", errors.New("invalid_note_scope")
		}
		return filepath.Join(base, "conversations", scope, "memory"), nil
	}
	old := filepath.Join(m.dir, "agent-workspace")
	entries, e := os.ReadDir(old)
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		dest, e := resolve(entry.Name())
		if e != nil {
			continue
		}
		if e = ensureManaged(dest); e != nil {
			return e
		}
		files, e := os.ReadDir(filepath.Join(old, entry.Name()))
		if e != nil {
			return e
		}
		for _, file := range files {
			if file.IsDir() || file.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(file.Name(), ".md") {
				continue
			}
			b, e := os.ReadFile(filepath.Join(old, entry.Name(), file.Name()))
			if e != nil {
				return e
			}
			target := filepath.Join(dest, file.Name())
			if _, e = os.Stat(target); os.IsNotExist(e) {
				if e = writePrivate(target, b); e != nil {
					return e
				}
			}
		}
	}
	m.workspace.ResolveDirectory = resolve
	return ensureManaged(filepath.Join(base, "global", "memory"), filepath.Join(base, "assets", "objects"))
}

func (m *Manager) managedLayoutStatus() map[string]any {
	backend := "当前平台尚未适配隔离执行器"
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("bwrap"); err == nil {
			backend = "Linux · Bubblewrap 已安装"
		} else {
			backend = "Linux · 待安装 Bubblewrap"
		}
	} else if runtime.GOOS == "windows" {
		backend = "Windows · LPAC CMD"
	}
	return map[string]any{"account": m.native.Account(), "private_workspace": filepath.Join(m.managedRoot(), "pri_workspace", accountDirectory(m.native.Account())), "public_workspace": filepath.Join(m.managedRoot(), "public_workspace"), "backend": backend, "platform": runtime.GOOS, "isolation_required": true}
}

func (s *Server) managedWorkspaceRoutes(mux *http.ServeMux) {
	s.crossMediaRoutes(mux)
	mux.HandleFunc("POST /api/agent/file-read", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Message string `json:"message_id"`
			Thread  string `json:"thread_id"`
		}
		if !decode(w, r, &in) {
			return
		}
		m := s.manager
		if !permittedRecipient(m.Config().Config, in.Thread) {
			fail(w, 403, "来源会话未授权")
			return
		}
		msg, e := m.native.Message(r.Context(), in.Message)
		if e != nil || msg.AccountID != m.native.Account() || msg.ThreadID != in.Thread || msg.FileAttachmentName() == "" {
			fail(w, 400, "不是当前账号来源会话中的文件")
			return
		}
		result, e := (mediaUnderstanding{manager: m}).receiveFile(r.Context(), msg, msg.FileAttachmentName())
		if e != nil {
			fail(w, 502, e.Error())
			return
		}
		respond(w, 200, map[string]any{"observation": result, "real_wechat_sends": 0})
	}))

	mux.HandleFunc("GET /api/agent/global-search", s.protected(func(w http.ResponseWriter, r *http.Request) {
		c := s.manager.agent.config()
		if len(c.OwnerIDs) == 0 || !c.CrossConversation {
			fail(w, 403, "当前账号未配置管理员全局会话")
			return
		}
		offset, e := strconv.Atoi(r.URL.Query().Get("offset"))
		if e != nil && r.URL.Query().Has("offset") {
			fail(w, 400, "分页参数无效")
			return
		}
		limit, e := strconv.Atoi(r.URL.Query().Get("limit"))
		if e != nil && r.URL.Query().Has("limit") {
			fail(w, 400, "分页参数无效")
			return
		}
		// Explicit authenticated console inspection, not a fabricated WeChat packet.
		p := &permittedTools{manager: s.manager, trace: companion.Input{Account: s.manager.native.Account(), Thread: c.OwnerIDs[0], Sender: c.OwnerIDs[0], Mode: "direct"}}
		raw, _ := json.Marshal(map[string]any{"query": r.URL.Query().Get("query"), "thread_id": r.URL.Query().Get("thread_id"), "offset": offset, "limit": limit})
		result, err := p.callProject(r.Context(), "agent_global_search", raw)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(result))
	}))
	mux.HandleFunc("GET /api/agent/projects", s.protected(func(w http.ResponseWriter, r *http.Request) {
		p := &permittedTools{manager: s.manager, trace: companion.Input{Account: s.manager.native.Account()}}
		result, err := p.callProject(r.Context(), "project_list", json.RawMessage(`{}`))
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(result))
	}))
}
