package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"wechat-bot/internal/linuxnative"
)

type nativeActions interface {
	ExecuteAction(context.Context, linuxnative.AdminAction, string) (linuxnative.ActionStatus, error)
	InspectAction(context.Context, string) (linuxnative.ActionStatus, error)
}
type actionProposal struct {
	ID      string                   `json:"id"`
	Account string                   `json:"account"`
	Request linuxnative.AdminAction  `json:"request"`
	Summary string                   `json:"summary"`
	State   string                   `json:"state"`
	Created time.Time                `json:"created"`
	Result  linuxnative.ActionStatus `json:"result"`
}
type actionLedger struct {
	mu    sync.Mutex
	path  string
	items map[string]actionProposal
}

func newActionLedger(dir string) (*actionLedger, error) {
	ledger := &actionLedger{path: filepath.Join(dir, "action-proposals.json"), items: map[string]actionProposal{}}
	b, err := os.ReadFile(ledger.path)
	if errors.Is(err, os.ErrNotExist) {
		return ledger, nil
	}
	if err != nil || len(b) > 2<<20 || json.Unmarshal(b, &ledger.items) != nil || ledger.items == nil || len(ledger.items) > 1000 {
		return nil, errors.New("人工操作记录不可用")
	}
	return ledger, nil
}
func (l *actionLedger) save(p actionProposal) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.items) >= 1000 {
		if _, ok := l.items[p.ID]; !ok {
			return errors.New("人工操作记录已达上限")
		}
	}
	old, exists := l.items[p.ID]
	l.items[p.ID] = p
	b, err := json.Marshal(l.items)
	if err != nil || len(b) > 2<<20 {
		if exists {
			l.items[p.ID] = old
		} else {
			delete(l.items, p.ID)
		}
		return errors.New("人工操作记录过大")
	}
	if err = writePrivate(l.path, b); err != nil {
		if exists {
			l.items[p.ID] = old
		} else {
			delete(l.items, p.ID)
		}
		return err
	}
	return nil
}
func (l *actionLedger) read(id, account string) (actionProposal, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.items[id]
	return p, ok && p.Account == account
}
func (l *actionLedger) list(account string) []actionProposal {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []actionProposal{}
	for _, p := range l.items {
		if p.Account == account {
			out = append(out, p)
		}
	}
	return out
}

func (s *Server) actionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/actions", s.protected(func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]any{"items": s.manager.actions.list(s.manager.native.Account())})
	}))
	mux.HandleFunc("POST /api/actions/prepare", s.protected(s.prepareAction))
	mux.HandleFunc("POST /api/actions/{id}/approve", s.protected(s.approveAction))
	mux.HandleFunc("POST /api/actions/{id}/cancel", s.protected(s.cancelAction))
	mux.HandleFunc("GET /api/actions/{id}", s.protected(s.inspectAction))
}
func (s *Server) prepareAction(w http.ResponseWriter, r *http.Request) {
	var input linuxnative.AdminAction
	if !decode(w, r, &input) {
		return
	}
	if _, ok := s.manager.native.(nativeActions); !ok {
		fail(w, 501, "当前后台未接入账户操作")
		return
	}
	m := s.manager
	m.op.Lock()
	defer m.op.Unlock()
	c := m.Config().Config
	labels := map[string]string{"rename": "修改群名", "my_name": "修改我的群昵称", "mute": "设置群免打扰", "add_members": "添加群成员", "remove_members": "移出群成员", "leave": "退出群聊", "clear_announcement": "清空群公告", "create": "创建群聊", "recall": "撤回自己的消息"}
	label, ok := labels[input.Kind]
	if !ok || !utf8.ValidString(input.Text) || len(input.Text) > 256 || strings.ContainsRune(input.Text, '\x00') {
		fail(w, 400, "操作类型或文字无效")
		return
	}
	if input.Kind != "create" && (!permittedRecipient(c, input.ThreadID) || !validID(input.ThreadID)) {
		fail(w, 403, "操作目标必须是已授权会话")
		return
	}
	if input.Kind != "create" && input.Kind != "recall" && !strings.HasSuffix(input.ThreadID, "@chatroom") {
		fail(w, 400, "这个操作需要群聊目标")
		return
	}
	if input.Kind == "rename" || input.Kind == "my_name" {
		if strings.TrimSpace(input.Text) == "" {
			fail(w, 400, "请填写新名称")
			return
		}
	}
	if input.Kind == "create" || input.Kind == "add_members" || input.Kind == "remove_members" {
		min := 1
		if input.Kind == "create" {
			min = 2
		}
		if len(input.Members) < min || len(input.Members) > 40 {
			fail(w, 400, "成员数量不符合限制")
			return
		}
		seen := map[string]bool{}
		for _, id := range input.Members {
			if !validID(id) || strings.HasSuffix(id, "@chatroom") || seen[id] {
				fail(w, 400, "成员 ID 无效或重复")
				return
			}
			seen[id] = true
		}
	}
	if input.Kind == "recall" {
		msg, err := m.native.Message(r.Context(), input.MessageID)
		if err != nil || msg.ThreadID != input.ThreadID || msg.Direction != "outgoing" || msg.ServerID == "" || msg.ServerID != input.ServerID {
			fail(w, 400, "只能撤回所选会话中具有准确服务端编号的本人消息")
			return
		}
	}
	id := randomToken()
	summary := label + " · " + input.ThreadID
	if input.Kind == "create" {
		summary = label + " · " + input.Text
	}
	proposal := actionProposal{ID: id, Account: m.native.Account(), Request: input, Summary: summary, State: "awaiting_approval", Created: time.Now().UTC()}
	if err := m.actions.save(proposal); err != nil {
		fail(w, 500, err.Error())
		return
	}
	m.log("admin_action_prepared")
	respond(w, 201, proposal)
}
func (s *Server) approveAction(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Confirm string `json:"confirm"`
	}
	if !decode(w, r, &input) {
		return
	}
	m := s.manager
	m.op.Lock()
	defer m.op.Unlock()
	p, ok := m.actions.read(r.PathValue("id"), m.native.Account())
	if !ok {
		fail(w, 404, "未找到待审批操作")
		return
	}
	if p.State != "awaiting_approval" {
		respond(w, 200, p)
		return
	}
	if input.Confirm != p.ID {
		fail(w, 400, "需要确认当前操作编号")
		return
	}
	if time.Since(p.Created) > 15*time.Minute {
		p.State = "expired"
		_ = m.actions.save(p)
		fail(w, 409, "审批已过期，请重新准备")
		return
	}
	if p.Request.Kind != "create" && !permittedRecipient(m.Config().Config, p.Request.ThreadID) {
		fail(w, 403, "会话授权已取消")
		return
	}
	native, ok := m.native.(nativeActions)
	if !ok {
		fail(w, 501, "后台操作不可用")
		return
	}
	p.State = "unknown"
	if err := m.actions.save(p); err != nil {
		fail(w, 500, "无法保存操作领取记录，未执行")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	status, err := native.ExecuteAction(ctx, p.Request, p.ID)
	if err == nil {
		p.Result = status
		p.State = status.State
	}
	if saveErr := m.actions.save(p); saveErr != nil {
		fail(w, 500, "操作可能已提交，记录保存失败；不要重复审批")
		return
	}
	m.log("admin_action_submitted_no_retry")
	respond(w, 202, p)
}
func (s *Server) cancelAction(w http.ResponseWriter, r *http.Request) {
	var input struct{}
	if !decode(w, r, &input) {
		return
	}
	m := s.manager
	m.op.Lock()
	defer m.op.Unlock()
	p, ok := m.actions.read(r.PathValue("id"), m.native.Account())
	if !ok {
		fail(w, 404, "未找到操作")
		return
	}
	if p.State != "awaiting_approval" {
		fail(w, 409, "只允许取消尚未审批的草稿")
		return
	}
	p.State = "cancelled"
	if err := m.actions.save(p); err != nil {
		fail(w, 500, err.Error())
		return
	}
	respond(w, 200, p)
}
func (s *Server) inspectAction(w http.ResponseWriter, r *http.Request) {
	m := s.manager
	m.op.Lock()
	defer m.op.Unlock()
	p, ok := m.actions.read(r.PathValue("id"), m.native.Account())
	if !ok {
		fail(w, 404, "未找到操作")
		return
	}
	if p.Result.ID != "" {
		native, ok := m.native.(nativeActions)
		if !ok {
			fail(w, 501, "后台操作不可用")
			return
		}
		status, err := native.InspectAction(r.Context(), p.Result.ID)
		if err != nil {
			fail(w, 502, "操作查询失败，不要重复执行")
			return
		}
		p.Result = status
		p.State = status.State
		if err = m.actions.save(p); err != nil {
			fail(w, 500, err.Error())
			return
		}
	}
	respond(w, 200, p)
}
