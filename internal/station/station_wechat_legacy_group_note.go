package station

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"path/filepath"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

type legacyGroupNoteView struct {
	Status string `json:"status"`
	Digest string `json:"digest,omitempty"`
	Note   string `json:"note,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func (s *stationWechat) legacyGroupNoteRoutes(server *websession.Store, mux *http.ServeMux) {
	const path = "/api/apps/wechat/connections/{connection}/legacy-import/memory/{thread}/preference"
	handle := server.Protect(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		thread := r.PathValue("thread")
		if !validStationGroup(thread) {
			websession.Fail(w, http.StatusBadRequest, "请选择明确的群聊")
			return
		}
		var digest string
		if r.Method == http.MethodPost {
			var in struct {
				Digest       string `json:"digest"`
				SameIdentity bool   `json:"same_identity"`
			}
			if !websession.Decode(w, r, &in) {
				return
			}
			if !legacyMemoryDigestValid(in.Digest) || !in.SameIdentity {
				websession.Fail(w, http.StatusBadRequest, "请预演并确认新旧连接使用同一个实际微信账号")
				return
			}
			digest = in.Digest
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		view, err := s.legacyGroupNote(ctx, r.PathValue("connection"), thread, digest)
		switch {
		case err != nil:
			legacyImportFailure(w, err)
		case digest != "" && view.Status != "applied":
			websession.Fail(w, http.StatusConflict, view.Reason)
		default:
			websession.Respond(w, http.StatusOK, view)
		}
	})
	mux.HandleFunc("GET "+path, handle)
	mux.HandleFunc("POST "+path, handle)
}

func (s *stationWechat) legacyGroupNote(ctx context.Context, id, thread, digest string) (legacyGroupNoteView, error) {
	unavailable := func(status, reason string) (legacyGroupNoteView, error) {
		return legacyGroupNoteView{Status: status, Reason: reason}, nil
	}
	native, err := s.WorkerSender(ctx, id)
	if err != nil {
		return legacyGroupNoteView{}, err
	}
	worker, err := s.worker(id, false)
	if err != nil {
		return legacyGroupNoteView{}, err
	}
	if worker == nil {
		return unavailable("not_ready", "请先保存旧版接入摘要并授权该群的历史读取")
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	history := worker.policy.HistoryGrants[thread]
	if history.IsZero() || worker.policy.Grants[thread].IsZero() {
		return unavailable("not_ready", "请先授权该群的回复范围和历史读取")
	}
	if prior := worker.policy.LegacyGroupNoteImports[thread]; prior != "" {
		if digest != "" && prior != digest {
			return unavailable("conflict", "该群的旧备注已有接续记录")
		}
		return legacyGroupNoteView{Status: "applied"}, nil
	}
	if worker.run != nil || (worker.policy.Enabled && !worker.policy.Paused) {
		return unavailable("not_ready", "请先暂停连接并等候工作器退出")
	}
	current := worker.policy.GroupRules[thread]
	if current.Note != "" {
		return unavailable("conflict", "新站已有该群备注，保留现有内容")
	}
	target, err := workspacefs.OpenPath(worker.dir)
	if err != nil {
		return legacyGroupNoteView{}, apphost.ErrUnavailable
	}
	defer target.Close()
	account := native.Account()
	archive, err := readLegacyImportArchive(target)
	if err != nil || archive.Plan.Account != account {
		return unavailable("not_ready", "请先保存此连接的旧版接入摘要")
	}
	snapshot, source, err := s.openLegacyImport()
	if err != nil {
		return legacyGroupNoteView{}, err
	}
	defer snapshot.Close()
	config, _, err := legacyImportAccountPaths(snapshot, source.Primary, account)
	if err != nil {
		return legacyGroupNoteView{}, err
	}
	cutover, evidence := s.checkLegacyCutover(ctx, id, native, snapshot, source, archive.LegacyServer)
	if !legacyMemoryCutoverReady(cutover) {
		return unavailable("not_ready", "请先核对旧宿主停机记录、原生档案导入回执与目标微信登录")
	}
	daemon, err := native.DaemonStatus(ctx)
	if err != nil {
		return unavailable("not_ready", "目标微信原生服务暂时不可核对")
	}
	dir, err := workspacefs.OpenExistingDirectory(snapshot, filepath.Join(filepath.Dir(config), "companion"))
	if err != nil {
		return unavailable("not_ready", "未找到该账号的旧 SQLite 记忆副本")
	}
	defer dir.Close()
	note, file, err := readStationLegacyScope(ctx, dir, account, thread, companion.ReadLegacyConversationPreference)
	if err != nil {
		return unavailable("not_ready", "旧库不完整或无法核对该群身份")
	}
	if note == "" {
		return unavailable("empty", "该群没有保存过可接续的文本备注")
	}
	binding, err := json.Marshal([]any{
		"legacy-group-note-v1", s.installation, id, account, thread, archive.Plan.Digest,
		source.Root, daemon.ServerID, daemon.OwnershipGeneration, evidence,
		legacyMemoryVersion{file.Size(), file.ModTime().UTC()}, worker.policy.Grants[thread], history, current, note,
	})
	if err != nil {
		return legacyGroupNoteView{}, err
	}
	want := legacySettingsHash(binding)
	if digest == "" {
		return legacyGroupNoteView{Status: "ready", Digest: want, Note: note}, nil
	}
	if digest != want {
		return unavailable("conflict", "旧备注或目标权限已变化，请重新预演")
	}
	latest, latestEvidence := s.checkLegacyCutover(ctx, id, native, snapshot, source, archive.LegacyServer)
	status, err := native.DaemonStatus(ctx)
	if err != nil || status != daemon || !legacyMemoryCutoverReady(latest) || latestEvidence != evidence {
		return unavailable("conflict", "切换前置条件已变化，请重新预演")
	}
	if fresh, err := dir.Lstat(legacyMemoryDB); err != nil || !sameLegacyMemoryFile(file, fresh) || !standaloneLegacyMemory(dir) {
		return unavailable("conflict", "旧数据库在确认期间变化，请重新预演")
	}
	if ctx.Err() != nil || s.ctx.Err() != nil {
		return legacyGroupNoteView{}, apphost.ErrLifecycleConflict
	}
	// Fill just the text field. The explicit share toggle and all grants
	// remain on their current policy and authorization epochs.
	next := worker.policy
	next.GroupRules = maps.Clone(next.GroupRules)
	next.LegacyGroupNoteImports = maps.Clone(next.LegacyGroupNoteImports)
	if next.GroupRules == nil {
		next.GroupRules = make(map[string]stationWechatGroupRule)
	}
	if next.LegacyGroupNoteImports == nil {
		next.LegacyGroupNoteImports = make(map[string]string)
	}
	current.Note = note
	next.GroupRules[thread] = current
	next.LegacyGroupNoteImports[thread] = want
	if err := worker.save(next); err != nil {
		return legacyGroupNoteView{}, err
	}
	return legacyGroupNoteView{Status: "applied"}, nil
}
