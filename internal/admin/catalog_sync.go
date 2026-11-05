package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/linuxnative"
)

type nativeCatalogSync interface {
	StartCatalogSync(context.Context, string) (linuxnative.CatalogSyncOperation, error)
	CatalogSyncStatus(context.Context, string) (linuxnative.CatalogSyncOperation, error)
	RefreshGroupMetadata(context.Context, string) (linuxnative.CatalogSyncOperation, error)
}
type nativeMemberSync interface {
	RefreshGroupMembers(context.Context, string) (linuxnative.CatalogSyncOperation, error)
}
type catalogRefresh struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Thread   string `json:"thread,omitempty"`
	State    string `json:"state"`
	Phase    string `json:"phase"`
	Message  string `json:"message"`
	Records  int    `json:"records"`
	Hydrated int    `json:"hydrated_groups"`
	Partial  bool   `json:"partial"`
}

func (s *Server) catalogSyncRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/catalog/sync", s.protected(func(w http.ResponseWriter, r *http.Request) {
		s.catalogMu.Lock()
		defer s.catalogMu.Unlock()
		if s.catalogJob == nil {
			respond(w, 200, catalogRefresh{State: "idle", Message: "尚未主动同步目录"})
			return
		}
		respond(w, 200, *s.catalogJob)
	}))
	mux.HandleFunc("POST /api/catalog/sync", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Kind   string `json:"kind"`
			Thread string `json:"thread"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Kind != "groups" && in.Kind != "contacts" && in.Kind != "members" {
			fail(w, 400, "请选择群聊、好友或群成员目录")
			return
		}
		if in.Kind == "members" {
			if !strings.HasSuffix(in.Thread, "@chatroom") || !s.consoleConversationAllowed(in.Thread) {
				fail(w, 403, "请先选择已授权的群聊")
				return
			}
			if _, ok := s.manager.native.(nativeMemberSync); !ok {
				fail(w, 501, "当前微信后台不支持群成员刷新")
				return
			}
		} else {
			in.Thread = ""
		}
		native, ok := s.manager.native.(nativeCatalogSync)
		if !ok {
			fail(w, 501, "当前微信后台不支持目录同步")
			return
		}
		s.catalogMu.Lock()
		defer s.catalogMu.Unlock()
		if s.catalogJob != nil && s.catalogJob.State == "running" {
			if in.Kind == "members" && (s.catalogJob.Kind != in.Kind || s.catalogJob.Thread != in.Thread) {
				fail(w, 409, "另一个目录同步正在进行，请完成后再刷新当前群成员")
				return
			}
			respond(w, 202, *s.catalogJob)
			return
		}
		job := &catalogRefresh{ID: randomToken(), Kind: in.Kind, Thread: in.Thread, State: "running", Phase: "inventory", Message: "正在同步微信目录…"}
		if in.Kind == "members" {
			job.Message = "正在从微信同步当前群的成员资料…"
		}
		s.catalogJob = job
		go s.runCatalogRefresh(native, *job)
		respond(w, 202, *job)
	}))
}

func (s *Server) publishCatalog(job catalogRefresh) {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	if s.catalogJob != nil && s.catalogJob.ID == job.ID {
		copy := job
		s.catalogJob = &copy
	}
}
func (s *Server) waitCatalog(ctx context.Context, n nativeCatalogSync, op linuxnative.CatalogSyncOperation) (linuxnative.CatalogSyncOperation, error) {
	for {
		switch op.State {
		case "succeeded":
			return op, nil
		case "queued", "running", "starting":
		default:
			return op, errors.New("微信目录读取未完成，请确认登录后再刷新")
		}
		select {
		case <-ctx.Done():
			return op, errors.New("目录同步超时，当前读取可能仍在后台完成，请先查询目录")
		case <-time.After(500 * time.Millisecond):
		}
		var err error
		op, err = n.CatalogSyncStatus(ctx, op.ID)
		if err != nil {
			return op, err
		}
	}
}
func (s *Server) runCatalogRefresh(n nativeCatalogSync, job catalogRefresh) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var problem error
	defer func() {
		job.State = "succeeded"
		job.Phase = "done"
		job.Message = "目录已刷新，可重新搜索。"
		if job.Kind == "members" {
			job.Message = "当前群成员资料已刷新。"
		}
		if job.Partial {
			job.Message = "目录已部分更新，部分资料尚未完成，可再次刷新。"
		}
		if problem != nil {
			job.State = "failed"
			job.Message = problem.Error()
		}
		s.manager.record(bot.Record{Event: "catalog_sync_completed", Category: "system", Status: job.State, Reason: job.Kind, Content: job.Message})
		// Terminal status must be the last operation: callers may release
		// storage immediately after observing completion (including shutdown).
		s.publishCatalog(job)
	}()
	var op linuxnative.CatalogSyncOperation
	var err error
	if job.Kind == "members" {
		op, err = n.(nativeMemberSync).RefreshGroupMembers(ctx, job.Thread)
	} else {
		op, err = n.StartCatalogSync(ctx, job.Kind)
	}
	if err != nil {
		problem = err
		return
	}
	op, err = s.waitCatalog(ctx, n, op)
	if err != nil {
		problem = err
		return
	}
	job.Records = op.Result.RecordCount
	job.Partial = op.Result.FailedItems > 0 || op.Result.ResumeAvailable || op.Result.Complete != nil && !*op.Result.Complete
	if job.Kind != "groups" {
		return
	}
	catalog, ok := s.manager.native.(nativeCatalog)
	if !ok {
		job.Partial = true
		return
	}
	groups, err := s.manager.native.Groups(ctx, "")
	if err != nil {
		problem = err
		return
	}
	known := map[string]bool{}
	for _, g := range groups {
		known[g.ID] = true
	}
	job.Phase = "hydrate"
	job.Message = "正在补充新会话的群名和资料…"
	s.publishCatalog(job)
	cursor := ""
	seen := map[string]bool{}
	attempted := 0
	for page := 0; page < 10; page++ {
		threads, err := catalog.Catalog(ctx, "threads", "", "", cursor)
		if err != nil {
			problem = errors.New("目录已更新，但新群会话资料读取失败，请稍后刷新")
			return
		}
		for _, thread := range threads.Items {
			id, _ := thread["peerId"].(string)
			if id == "" {
				id, _ = thread["id"].(string)
			}
			if !strings.HasSuffix(id, "@chatroom") || !validID(id) || known[id] {
				continue
			}
			known[id] = true
			if attempted >= 5 {
				job.Partial = true
				continue
			}
			attempted++
			op, err := n.RefreshGroupMetadata(ctx, id)
			if err != nil {
				job.Partial = true
				continue
			}
			op, err = s.waitCatalog(ctx, n, op)
			if err != nil {
				job.Partial = true
				continue
			}
			job.Records += op.Result.RecordCount
			job.Hydrated++
			if op.Result.FailedItems > 0 {
				job.Partial = true
			}
			s.publishCatalog(job)
		}
		if threads.NextCursor == nil || *threads.NextCursor == "" {
			return
		}
		cursor = *threads.NextCursor
		if seen[cursor] {
			problem = errors.New("会话目录游标重复，请稍后刷新")
			return
		}
		seen[cursor] = true
	}
	job.Partial = true
}
