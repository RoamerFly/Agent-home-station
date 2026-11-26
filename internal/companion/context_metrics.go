package companion

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"wechat-bot/internal/deepseek"
)

type contextMetricRunKey struct{}

func withContextMetricRun(ctx context.Context) context.Context {
	if id, _ := ctx.Value(contextMetricRunKey{}).(string); id != "" {
		return ctx
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return ctx // Never fail the user's task solely because diagnostics are unavailable.
	}
	return context.WithValue(ctx, contextMetricRunKey{}, hex.EncodeToString(token[:]))
}

func contextMetricRunID(ctx context.Context) string {
	id, _ := ctx.Value(contextMetricRunKey{}).(string)
	return id
}

// Only numeric component sizes are retained. No prompt, query, source text,
// secrets or provider response is written to the diagnostic metric table.
func (s *Store) recordContextMetrics(ctx context.Context, system string, turns []map[string]string, catalog []byte) error {
	_, err := s.recordContextMetricsRound(ctx, system, turns, catalog, 0)
	return err
}

func (s *Store) recordContextMetricsRound(ctx context.Context, system string, turns []map[string]string, catalog []byte, round int) (int64, error) {
	if s.db == nil {
		return 0, nil
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
	result, e := s.db.ExecContext(ctx, `INSERT INTO context_metrics(at,total,bytes,components,method,run_id,model_round) VALUES(?,?,?,?,'conservative_text_estimate',?,?)`, memoryTime(time.Now()), total, bytes, string(raw), contextMetricRunID(ctx), round)
	if e != nil {
		return 0, e
	}
	id, e := result.LastInsertId()
	if e != nil {
		return 0, e
	}
	_, e = s.db.ExecContext(ctx, "DELETE FROM context_metrics WHERE id<(SELECT COALESCE(MAX(id),0)-999 FROM context_metrics)")
	return id, e
}

// Each provider HTTP attempt is attached to the exact prepared model input.
// The existing billing observer still owns pricing and remains chained by
// deepseek.WithUsageObserver. Missing usage or cache fields stay distinguishable.
func (s *Store) recordContextUsage(ctx context.Context, id int64, usage deepseek.Usage) error {
	if s.db == nil || id == 0 {
		return nil
	}
	reported, unknownCache := 0, 0
	input, output, cacheHit := int64(0), int64(0), int64(0)
	if usage.Reported {
		reported = 1
		input, output, cacheHit = usage.Input, usage.Output, usage.CacheHit
		if !usage.CacheHitKnown() {
			unknownCache = 1
		}
	}
	model := usage.Model
	if len(model) > 128 || strings.ContainsAny(model, "\r\n\x00") {
		model = ""
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(writeCtx, `UPDATE context_metrics SET provider_attempts=provider_attempts+1, provider_reported=provider_reported+?, provider_input=provider_input+?, provider_output=provider_output+?, provider_cache_hit=provider_cache_hit+?, provider_cache_unknown=provider_cache_unknown+?, provider_model=CASE WHEN ?='' THEN provider_model WHEN provider_model='' THEN ? WHEN provider_model=? THEN provider_model ELSE 'mixed' END WHERE id=?`, reported, input, output, cacheHit, unknownCache, model, model, model, id)
	return err
}

func (e *AgentRunner) withContextMetricUsage(ctx context.Context, id int64) context.Context {
	if id == 0 {
		return ctx
	}
	return deepseek.WithUsageObserver(ctx, func(usage deepseek.Usage) {
		if err := e.Store.recordContextUsage(ctx, id, usage); err != nil && e.ToolLog != nil {
			e.ToolLog("context_metrics_usage_write_failed")
		}
	})
}

func (s *Store) ContextMetrics(ctx context.Context) ([]map[string]any, error) {
	rows, e := s.db.QueryContext(ctx, "SELECT at,total,bytes,components,method,run_id,model_round,provider_attempts,provider_reported,provider_input,provider_output,provider_cache_hit,provider_cache_unknown,provider_model FROM context_metrics ORDER BY id DESC LIMIT 100")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var at, raw, method, runID, providerModel string
		var total, bytes, round, attempts, reported, cacheUnknown int
		var providerInput, providerOutput, cacheHit int64
		if e = rows.Scan(&at, &total, &bytes, &raw, &method, &runID, &round, &attempts, &reported, &providerInput, &providerOutput, &cacheHit, &cacheUnknown, &providerModel); e != nil {
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
		out = append(out, map[string]any{"at": at, "estimated_input_tokens": total, "input_bytes": bytes, "components": components, "detail_bytes": detailBytes, "detail_estimated_tokens": detailTokens, "method": method,
			"run_id": runID, "model_round": round, "provider_attempts": attempts, "provider_reported": reported, "provider_input_tokens": providerInput, "provider_output_tokens": providerOutput, "provider_cache_hit_tokens": cacheHit, "provider_cache_unknown": cacheUnknown, "provider_model": providerModel})
	}
	return out, rows.Err()
}
