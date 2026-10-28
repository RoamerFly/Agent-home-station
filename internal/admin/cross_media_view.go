package admin

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A private console projection of durable batch facts. Never queries or sends
// native messages, exposes upload handles, or returns raw journal input text.
func (s *Server) crossMediaRoutes(mux *http.ServeMux) {
	s.crossMediaConsoleRoutes(mux)
	mux.HandleFunc("GET /api/agent/media-batches", s.protected(func(w http.ResponseWriter, r *http.Request) {
		offset, limit := 0, 30
		for key, target := range map[string]*int{"offset": &offset, "limit": &limit} {
			if r.URL.Query().Has(key) {
				v, e := strconv.Atoi(r.URL.Query().Get(key))
				if e != nil || v < 0 || key == "limit" && v == 0 {
					fail(w, 400, "分页参数无效")
					return
				}
				*target = v
			}
		}
		entries, e := os.ReadDir(filepath.Join(s.manager.dir, "cross-media"))
		if e != nil && !os.IsNotExist(e) {
			fail(w, 500, "附件投递记录暂不可读")
			return
		}
		rows := []map[string]any{}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		names := map[string]string{}
		name := func(id string) string {
			if n, ok := names[id]; ok {
				return n
			}
			n := s.manager.memoryLabel(ctx, id)
			names[id] = n
			return n
		}
		for _, entry := range entries {
			if r.Context().Err() != nil {
				return
			}
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			id := strings.TrimSuffix(entry.Name(), ".json")
			batch, e := s.manager.readCrossBatch(id)
			if e != nil {
				rows = append(rows, map[string]any{"operation_id": id, "state": "needs_review", "reason": "投递记录不完整或身份校验未通过，已保留原记录"})
				continue
			}
			if thread := r.URL.Query().Get("thread_id"); thread != "" && thread != batch.Origin && thread != batch.Source && thread != batch.Target {
				continue
			}
			parts := []map[string]any{}
			accepted := 0
			for i, p := range batch.Parts {
				part := map[string]any{"index": i + 1, "kind": p.Media.Kind, "title": p.Media.Title, "state": "pending", "duration_ms": p.Media.DurationMS}
				if receipt, ok := batch.Receipts[p.Key]; ok && validCrossReceipt(batch, p, receipt) {
					part["state"] = "accepted"
					part["server_id"] = receipt.ServerID
					accepted++
				}
				parts = append(parts, part)
			}
			state := "queued"
			if batch.SupersededBy != "" {
				state = "superseded"
			} else if crossBatchAccepted(batch) {
				state = "accepted"
			} else if !s.manager.crossSeedAllowed(batch.Parts[0]) {
				state = "paused"
			}
			rows = append(rows, map[string]any{"operation_id": batch.ID, "account": batch.Account, "created": batch.Created, "origin_thread_id": batch.Origin, "origin_name": name(batch.Origin), "source_thread_id": batch.Source, "source_name": name(batch.Source), "target_thread_id": batch.Target, "target_name": name(batch.Target), "asset_id": batch.Asset, "state": state, "accepted_parts": accepted, "total_parts": len(parts), "parts": parts})
		}
		sort.SliceStable(rows, func(i, j int) bool {
			a, _ := rows[i]["created"].(time.Time)
			b, _ := rows[j]["created"].(time.Time)
			if !a.Equal(b) {
				return a.After(b)
			}
			return rows[i]["operation_id"].(string) < rows[j]["operation_id"].(string)
		})
		if offset > len(rows) {
			fail(w, 400, "分页位置超出范围")
			return
		}
		end := offset + min(limit, len(rows)-offset)
		respond(w, 200, map[string]any{"items": rows[offset:end], "total": len(rows), "next_offset": end, "has_more": end < len(rows), "account": s.manager.native.Account(), "read_only": true, "note": "状态来自持久批次回执；待核对不等于未送达。本页面不重发、不重新生成，也不宣称手机可播放。"})
	}))
}
