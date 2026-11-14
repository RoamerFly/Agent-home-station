package station

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"

	"wechat-bot/internal/identity"
	"wechat-bot/internal/websession"
)

// unverifiedMemoryManifest labels an old snapshot whose source metadata predates
// the current integrity fields. It remains a separate dataset, but the owner
// has explicitly allowed it to participate in memory search and replies.
type unverifiedMemoryManifest struct {
	Version       int    `json:"version"`
	SourceSHA256  string `json:"source_sha256"`
	SourceAccount string `json:"source_account"`
	Connection    string `json:"connection"`
	FishID        string `json:"fish_id"`
	OwnerID       string `json:"owner_id"`
	Status        string `json:"status"`
	AgentUsable   bool   `json:"agent_usable"`
}

type unverifiedMemoryScope struct {
	Thread      string `json:"thread_id"`
	EventCount  int    `json:"event_count"`
	MemoryCount int    `json:"memory_count"`
}

func unverifiedMemoryDir(workerDir string) string {
	return filepath.Join(workerDir, "unverified-memory")
}

func readUnverifiedMemoryManifest(workerDir string) (unverifiedMemoryManifest, error) {
	var item unverifiedMemoryManifest
	raw, err := os.ReadFile(filepath.Join(unverifiedMemoryDir(workerDir), "manifest.json"))
	if err != nil {
		return item, err
	}
	if json.Unmarshal(raw, &item) != nil || item.Version != 1 || item.Status != "unverified_read_only" || item.AgentUsable != true ||
		!identity.ValidPathID(item.SourceAccount) || !identity.ValidPathID(item.Connection) || item.FishID == "" || item.OwnerID == "" {
		return unverifiedMemoryManifest{}, errors.New("invalid unverified memory manifest")
	}
	return item, nil
}

func (s *stationWechat) unverifiedMemoryDatabase(id string) (string, unverifiedMemoryManifest, error) {
	worker, err := s.worker(id, false)
	if err != nil || worker == nil {
		return "", unverifiedMemoryManifest{}, os.ErrNotExist
	}
	manifest, err := readUnverifiedMemoryManifest(worker.dir)
	if err != nil || manifest.Connection != id {
		return "", unverifiedMemoryManifest{}, os.ErrNotExist
	}
	database := filepath.Join(unverifiedMemoryDir(worker.dir), "memory.sqlite")
	if info, statErr := os.Stat(database); statErr != nil || !info.Mode().IsRegular() || info.Size() < 1 {
		return "", unverifiedMemoryManifest{}, os.ErrNotExist
	}
	return database, manifest, nil
}

func (s *stationWechat) unverifiedMemoryAllowed(id, fish, owner string) (string, unverifiedMemoryManifest, error) {
	database, manifest, err := s.unverifiedMemoryDatabase(id)
	if err != nil || manifest.FishID != fish || manifest.OwnerID != owner {
		return "", unverifiedMemoryManifest{}, os.ErrNotExist
	}
	return database, manifest, nil
}

func openUnverifiedMemory(database string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(database)+"?mode=ro")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec("PRAGMA query_only=ON"); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (s *stationWechat) listUnverifiedMemoryScopes(ctx context.Context, id, fish, owner string) ([]unverifiedMemoryScope, error) {
	database, manifest, err := s.unverifiedMemoryAllowed(id, fish, owner)
	if err != nil {
		return nil, err
	}
	db, err := openUnverifiedMemory(database)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT json_extract(payload,'$.thread'),
		(SELECT COUNT(*) FROM events e WHERE e.scope=s.id),
		(SELECT COUNT(*) FROM memories m WHERE m.scope=s.id)
		FROM scopes s WHERE json_extract(s.payload,'$.channel')='wechat' AND json_extract(s.payload,'$.account')=? ORDER BY json_extract(s.payload,'$.thread')`, manifest.SourceAccount)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []unverifiedMemoryScope{}
	for rows.Next() {
		var thread string
		var events, memories int
		if err := rows.Scan(&thread, &events, &memories); err != nil {
			return nil, err
		}
		if identity.ValidPathID(thread) {
			result = append(result, unverifiedMemoryScope{Thread: thread, EventCount: events, MemoryCount: memories})
		}
	}
	return result, rows.Err()
}

func (s *stationWechat) searchUnverifiedMemory(ctx context.Context, id, fish, owner, thread, query, kind string, limit int) ([]map[string]any, error) {
	database, manifest, err := s.unverifiedMemoryAllowed(id, fish, owner)
	if err != nil || thread == "" || !identity.ValidPathID(thread) {
		return nil, errors.New("unverified memory source unavailable")
	}
	if limit < 1 {
		limit = 5
	}
	if limit > 50 {
		limit = 50
	}
	db, err := openUnverifiedMemory(database)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	scope := ""
	if err := db.QueryRowContext(ctx, `SELECT id FROM scopes WHERE json_extract(payload,'$.channel')='wechat' AND json_extract(payload,'$.account')=? AND json_extract(payload,'$.thread')=?`, manifest.SourceAccount, thread).Scan(&scope); err != nil {
		return nil, err
	}
	needle := "%" + strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(query, "\\", "\\\\"), "%", "\\%"), "_", "\\_") + "%"
	items := []map[string]any{}
	if kind == "memories" {
		rows, err := db.QueryContext(ctx, `SELECT id,kind,version,payload,updated FROM memories WHERE scope=? AND status='active' AND (?='' OR lower(COALESCE(json_extract(payload,'$.summary'),'')||' '||COALESCE(json_extract(payload,'$.detail'),'')) LIKE lower(?) ESCAPE '\\') ORDER BY updated,id LIMIT ?`, scope, query, needle, limit)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var memoryID, memoryKind, updated string
			var version int
			var payload []byte
			if err := rows.Scan(&memoryID, &memoryKind, &version, &payload, &updated); err != nil {
				return nil, err
			}
			var value map[string]any
			_ = json.Unmarshal(payload, &value)
			text := strings.TrimSpace(stringValue(value["detail"]))
			if text == "" {
				text = strings.TrimSpace(stringValue(value["summary"]))
			}
			items = append(items, map[string]any{"memory_id": memoryID, "kind": memoryKind, "version": version, "text": text, "updated_at": updated, "evidence_state": "unverified_old_archive", "record_origin": "unverified_old_archive"})
		}
		return items, rows.Err()
	}
	rows, err := db.QueryContext(ctx, `SELECT id,trace,role,sender,at,text,hash FROM events WHERE scope=? AND active=1 AND suppressed=0 AND (?='' OR lower(text) LIKE lower(?) ESCAPE '\\') ORDER BY seq,id LIMIT ?`, scope, query, needle, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, trace, role, sender, at, text, hash string
		if err := rows.Scan(&id, &trace, &role, &sender, &at, &text, &hash); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"event_id": id, "message_id": trace, "role": role, "sender_id": sender, "occurred_at": at, "text": text, "sha256": hash, "evidence_state": "unverified_old_archive", "record_origin": "unverified_old_archive"})
	}
	return items, rows.Err()
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func (s *stationWechat) unverifiedMemorySources(ctx context.Context, id, fish, owner string) ([]localWechatMemorySource, error) {
	if _, _, err := s.unverifiedMemoryAllowed(id, fish, owner); err != nil {
		return nil, err
	}
	worker, err := s.worker(id, false)
	if err != nil || worker == nil {
		return nil, err
	}
	worker.mu.Lock()
	threads := make([]string, 0, len(worker.policy.HistoryGrants))
	account := ""
	for thread, grant := range worker.policy.HistoryGrants {
		if !grant.IsZero() && identity.ValidPathID(thread) {
			threads = append(threads, thread)
		}
	}
	worker.mu.Unlock()
	account = manifestAccount(s, id)
	result := make([]localWechatMemorySource, 0, len(threads))
	for _, thread := range threads {
		result = append(result, localWechatMemorySource{Connection: id, Account: account, Thread: thread, Dataset: "unverified_archive"})
	}
	return result, ctx.Err()
}

func manifestAccount(s *stationWechat, id string) string {
	_, manifest, err := s.unverifiedMemoryDatabase(id)
	if err != nil {
		return ""
	}
	return manifest.SourceAccount
}

func (s *stationWechat) unverifiedMemoryRoute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("connection")
	if !identity.ValidPathID(id) || s.ownerReader == nil {
		websession.Fail(w, http.StatusBadRequest, "连接编号无效")
		return
	}
	owner, err := s.ownerReader()
	if err != nil {
		websession.Fail(w, http.StatusConflict, "本机主人暂不可核对")
		return
	}
	if r.Method == http.MethodGet && r.PathValue("thread") == "" {
		items, err := s.listUnverifiedMemoryScopes(r.Context(), id, owner.FishID, owner.ID)
		if err != nil {
			websession.Fail(w, http.StatusNotFound, "未找到当前微信连接的未验证旧档案")
			return
		}
		websession.Respond(w, http.StatusOK, map[string]any{"status": "unverified_archive", "agent_usable": true, "items": items})
		return
	}
	thread := r.PathValue("thread")
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "memories"
	}
	if kind != "memories" && kind != "events" {
		websession.Fail(w, http.StatusBadRequest, "档案类别无效")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit == 0 {
		limit = 20
	}
	items, err := s.searchUnverifiedMemory(r.Context(), id, owner.FishID, owner.ID, thread, r.URL.Query().Get("q"), kind, limit)
	if err != nil {
		websession.Fail(w, http.StatusNotFound, "未找到当前会话的未验证旧档案")
		return
	}
	websession.Respond(w, http.StatusOK, map[string]any{"status": "unverified_archive", "agent_usable": true, "thread_id": thread, "kind": kind, "items": items})
}
