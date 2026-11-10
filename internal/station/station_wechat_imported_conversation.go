package station

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

func (s *stationWechat) readImportedConversation(owner *websession.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		id, thread := r.PathValue("connection"), r.PathValue("thread")
		query := r.URL.Query()
		kind := query.Get("kind")
		if kind == "" {
			kind = "memories"
		}
		limit, maxBytes := stationLegacyMemoryDefaultLimit, 8192
		if value := query.Get("limit"); value != "" {
			limit, _ = strconv.Atoi(value)
		}
		if value := query.Get("max_bytes"); value != "" {
			maxBytes, _ = strconv.Atoi(value)
		}
		if !identity.ValidPathID(id) || !identity.ValidPathID(thread) || (kind != "memories" && kind != "events") ||
			limit < 1 || limit > stationLegacyMemoryMaxLimit || maxBytes < stationLegacyMemoryMinBytes || maxBytes > stationLegacyMemoryMaxBytes {
			websession.Fail(w, 400, "会话、档案类别或分页参数无效")
			return
		}
		session, _, ok := owner.Lookup(r)
		if !ok {
			websession.Fail(w, 401, "请重新登录管理页面")
			return
		}
		account, err := s.signedInAccount(ctx, id)
		if err != nil {
			websession.Fail(w, 409, "请先核对原微信账号登录状态")
			return
		}
		native, err := s.WorkerSender(ctx, id)
		if err != nil || native == nil || native.Account() != account {
			websession.Fail(w, 409, "当前微信连接无法核对")
			return
		}
		worker, err := s.worker(id, false)
		if err != nil || worker == nil {
			websession.Fail(w, 404, "当前连接没有可读取的旧会话档案")
			return
		}
		worker.mu.Lock()
		defer worker.mu.Unlock()
		grant := worker.policy.HistoryGrants[thread]
		if grant.IsZero() {
			websession.Fail(w, 403, "此会话的历史读取授权未生效")
			return
		}
		if _, err := readStationLegacyMemory(worker.dir, account, thread, grant); err != nil {
			websession.Fail(w, 409, "该会话的旧档案尚未接续，或与当前授权不匹配")
			return
		}
		dir := filepath.Join(worker.dir, "memory")
		root, err := workspacefs.OpenPath(dir)
		if err != nil {
			websession.Fail(w, 409, "会话的记忆核对资料尚不可用")
			return
		}
		defer root.Close()
		db, err := workspacefs.OpenRegular(root, legacyMemoryDB)
		if err != nil {
			websession.Fail(w, 409, "会话的记忆核对资料尚不可用")
			return
		}
		db.Close()
		store, err := companion.NewStore(dir)
		if err != nil {
			websession.Fail(w, 503, "旧记忆暂时不可读")
			return
		}
		defer store.Close()
		access := &stationAttachmentTools{
			store: store, native: native, dir: worker.dir,
			reads: map[string]time.Time{thread: grant},
			trace: companion.Input{Channel: "wechat", Account: account, Thread: thread, MessageTime: time.Now().UTC().Format(time.RFC3339Nano)},
			blocked: func() bool {
				key, _, valid := owner.Lookup(r)
				return !valid || key != session || s.ctx.Err() != nil || ctx.Err() != nil
			},
			legacyReadCursors: map[string]stationLegacyReadCursor{},
		}
		associated, _ := json.Marshal([]string{"station-imported-conversation-v1", session, id, account, thread, kind})
		input := map[string]any{"kind": kind, "limit": limit, "max_bytes": maxBytes}
		if cursor := query.Get("cursor"); cursor != "" {
			if len(cursor) > 2048 {
				websession.Fail(w, 400, "档案续读位置无效")
				return
			}
			var state stationLegacyReadCursor
			raw, decodeErr := base64.RawURLEncoding.DecodeString(cursor)
			clear, openErr := s.contactCursor.Open(nil, nil, raw, associated)
			if decodeErr != nil || openErr != nil || json.Unmarshal(clear, &state) != nil {
				websession.Fail(w, 409, "档案续读位置已失效，请从第一页读取")
				return
			}
			input["cursor"] = cursor
			access.legacyReadCursors[cursor] = state
		}
		if current, err := s.signedInAccount(ctx, id); err != nil || current != account || access.blocked() {
			websession.Fail(w, 409, "微信账号或管理员会话已变化")
			return
		}
		data, _ := json.Marshal(input)
		page, err := access.readLegacyMemory(ctx, data)
		if err != nil {
			websession.Fail(w, 409, "旧档案授权、来源或续读位置已变化，请从第一页核对")
			return
		}
		var result map[string]json.RawMessage
		if err := json.Unmarshal([]byte(page), &result); err != nil {
			websession.Fail(w, 503, "旧档案页面格式无效")
			return
		}
		var next string
		_ = json.Unmarshal(result["next_cursor"], &next)
		if state, exists := access.legacyReadCursors[next]; next != "" && exists {
			clear, _ := json.Marshal(state)
			sealed := s.contactCursor.Seal(nil, nil, clear, associated)
			result["next_cursor"], _ = json.Marshal(base64.RawURLEncoding.EncodeToString(sealed))
		}
		websession.Respond(w, 200, result)
	}
}
