package companion

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// Only numeric component sizes are retained. No prompt, query, source text,
// secrets or provider response is written to the diagnostic metric table.
func (s *Store) recordContextMetrics(ctx context.Context, system string, turns []map[string]string, catalog []byte) error {
	if s.db == nil {
		return nil
	}
	parts := map[string]int{"policy": EstimateTokens(system), "tools": EstimateTokens(string(catalog))}
	details := toolContextParts(catalog)
	if details == nil {
		details = map[string]contextPartSize{}
	}
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
		for name, size := range packetContextParts(t["content"]) {
			part := details[name]
			part.Bytes += size.Bytes
			part.Tokens += size.Tokens
			details[name] = part
		}
	}
	total := 0
	for _, n := range parts {
		total += n
	}
	// The detail fields are overlapping subparts of policy/tools/turns. Keep
	// them out of total and expose them separately when reading the metric.
	for name, size := range details {
		parts["detail_bytes."+name] = size.Bytes
		parts["detail_tokens."+name] = size.Tokens
	}
	raw, _ := json.Marshal(parts)
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
		components, detailBytes, detailTokens := map[string]int{}, map[string]int{}, map[string]int{}
		for name, value := range parts {
			switch {
			case strings.HasPrefix(name, "detail_bytes."):
				detailBytes[strings.TrimPrefix(name, "detail_bytes.")] = value
			case strings.HasPrefix(name, "detail_tokens."):
				detailTokens[strings.TrimPrefix(name, "detail_tokens.")] = value
			default:
				components[name] = value
			}
		}
		out = append(out, map[string]any{"at": at, "estimated_input_tokens": total, "input_bytes": bytes, "components": components, "detail_bytes": detailBytes, "detail_estimated_tokens": detailTokens, "method": method})
	}
	return out, rows.Err()
}
