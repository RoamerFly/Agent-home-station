package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
	"wechat-bot/internal/companion"
)

// Called only behind the authenticated management middleware. This is console
// inspection under configured owner permissions, never an incoming packet.
func (s *Server) consoleGlobalAccess() (*permittedTools, error) {
	c := s.manager.agent.config()
	if !c.CrossConversation {
		return nil, errors.New("当前账号未开启管理员全局会话")
	}
	for _, owner := range c.OwnerIDs {
		p := &permittedTools{manager: s.manager, trace: companion.Input{Channel: "wechat", Account: s.manager.native.Account(), Thread: owner, Sender: owner, Mode: "direct"}}
		if p.privileged() {
			return p, nil
		}
	}
	return nil, errors.New("当前账号未配置可用的管理员全局会话")
}

func (s *Server) consoleConversationAllowed(thread string) bool {
	if permittedRecipient(s.manager.Config().Config, thread) {
		return true
	}
	_, err := s.consoleGlobalAccess()
	return err == nil && s.manager.accountThreadKnown(thread)
}

func (s *Server) accountConversationConsoleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agent/conversations", s.protected(func(w http.ResponseWriter, r *http.Request) {
		p, err := s.consoleGlobalAccess()
		if err != nil {
			fail(w, 403, err.Error())
			return
		}
		query := r.URL.Query()
		input := map[string]any{"query": query.Get("query")}
		for _, key := range []string{"offset", "limit"} {
			if query.Has(key) {
				value, e := strconv.Atoi(query.Get(key))
				if e != nil || value < 0 {
					fail(w, 400, "分页参数无效")
					return
				}
				input[key] = value
			}
		}
		if query.Has("refresh") {
			value, e := strconv.ParseBool(query.Get("refresh"))
			if e != nil {
				fail(w, 400, "刷新参数无效")
				return
			}
			input["refresh"] = value
		}
		seconds := 30
		if query.Has("timeout_seconds") {
			value, e := strconv.Atoi(query.Get("timeout_seconds"))
			if e != nil || value < 0 || int64(value) > int64((time.Duration(1<<63-1))/time.Second) {
				fail(w, 400, "超时参数无效")
				return
			}
			seconds = value
		}
		ctx, cancel := context.WithCancel(r.Context())
		if seconds > 0 {
			cancel()
			ctx, cancel = context.WithTimeout(r.Context(), time.Duration(seconds)*time.Second)
		}
		defer cancel()
		writeUntil := time.Time{}
		if deadline, ok := ctx.Deadline(); ok {
			writeUntil = deadline.Add(5 * time.Second)
		}
		if err := http.NewResponseController(w).SetWriteDeadline(writeUntil); err != nil && !errors.Is(err, http.ErrNotSupported) {
			fail(w, 500, "无法设置目录查询期限")
			return
		}
		raw, _ := json.Marshal(input)
		result, err := p.listAccountConversations(ctx, raw)
		if err != nil {
			var inputErr *agentLocalError
			if ctx.Err() != nil {
				fail(w, 504, "会话目录读取未完成，可先使用已保存的目录")
			} else if errors.As(err, &inputErr) {
				fail(w, 400, inputErr.Error())
			} else {
				fail(w, 502, "会话目录暂不可读，请检查账号及目录索引")
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(result))
	}))
}
