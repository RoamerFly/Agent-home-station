package companion

import (
	"context"
	"encoding/json"
	"time"
)

// Only numeric component sizes are retained. No prompt, query, source text,
// secrets or provider response is written to the diagnostic metric table.
func (s *Store) recordContextMetrics(ctx context.Context, system string, turns []map[string]string, catalog []byte) error {
	if s.db == nil {
		return nil
	}
	parts := map[string]int{"policy": EstimateTokens(system), "tools": EstimateTokens(string(catalog))}
	bytes := len(system) + len(catalog)
	for _, t := range turns {
		kind := t["role"]
		var header struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal([]byte(t["content"]), &header) == nil && header.Kind != "" {
			kind = header.Kind
		}
		parts[kind] += EstimateTokens(t["content"]) + 8
		bytes += len(t["content"])
	}
	raw, _ := json.Marshal(parts)
	total := 0
	for _, n := range parts {
		total += n
	}
	_, e := s.db.ExecContext(ctx, `INSERT INTO context_metrics(at,total,bytes,components,method) VALUES(?,?,?,?,'conservative_text_estimate')`, memoryTime(time.Now()), total, bytes, string(raw))
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, "DELETE FROM context_metrics WHERE id<(SELECT COALESCE(MAX(id),0)-999 FROM context_metrics)")
	return e
}

func (s *Store) ContextMetrics(ctx context.Context) ([]map[string]any, error) {
	rows, e := s.db.QueryContext(ctx, "SELECT at,total,bytes,components,method FROM context_metrics ORDER BY id DESC LIMIT 100")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var at, raw, method string
		var total, bytes int
		if e = rows.Scan(&at, &total, &bytes, &raw, &method); e != nil {
			return nil, e
		}
		parts := map[string]int{}
		if e = json.Unmarshal([]byte(raw), &parts); e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"at": at, "estimated_input_tokens": total, "input_bytes": bytes, "components": parts, "method": method})
	}
	return out, rows.Err()
}
