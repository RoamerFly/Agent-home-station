package companion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"wechat-bot/internal/mcptools"
)

type ReplyAuditSettings struct {
	Enabled       bool `json:"enabled"`
	Snapshots     bool `json:"snapshots"`
	RetentionDays int  `json:"retention_days"`
}

func ReplyAuditDefaults() ReplyAuditSettings { return ReplyAuditSettings{true, true, 7} }

type AuditSource struct {
	Scope    string `json:"scope"`
	EventID  string `json:"event_id"`
	Revision int    `json:"revision"`
}
type ReplyAudit struct {
	ReviewSettings   GroundingSettings `json:"review_settings"`
	ReviewTransport  string            `json:"review_transport"`
	Sources          []AuditSource     `json:"sources,omitempty"`
	ExpiresAt        time.Time         `json:"expires_at"`
	SnapshotKind     string            `json:"snapshot_kind,omitempty"`
	ID               string            `json:"id"`
	Scope            string            `json:"scope"`
	At               time.Time         `json:"at"`
	TriggerMessageID string            `json:"trigger_message_id"`
	Sender           string            `json:"sender"`
	Contexts         []ReplyContext    `json:"contexts"`
	Messages         []string          `json:"messages,omitempty"`
	State            string            `json:"state"`
	RiskFlags        []string          `json:"risk_flags,omitempty"`
	ContextHash      string            `json:"context_hash,omitempty"`
	ReviewFailure    *ReviewFailure    `json:"review_failure,omitempty"`
	Snapshot         json.RawMessage   `json:"snapshot,omitempty"`
}

func (e *Engine) writeReplyAudit(in Input, d Decision, risks []string, state string, snapshot []byte) {
	if in.Preview || e.Store == nil {
		return
	}
	cfg := ReplyAuditDefaults()
	if e.ReplyAuditOptions != nil {
		cfg = e.ReplyAuditOptions()
	}
	if !cfg.Enabled {
		return
	}
	if cfg.RetentionDays < 1 {
		cfg.RetentionDays = 7
	}
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	at := time.Now().UTC()
	trigger := in.TraceID
	if trigger == "" && in.Source != nil {
		trigger = in.Source.MessageID
	}
	h := sha256.Sum256([]byte(scope + trigger + at.Format(time.RFC3339Nano)))
	id := hex.EncodeToString(h[:])
	a := ReplyAudit{ID: id, Scope: scope, At: at, TriggerMessageID: trigger, Sender: in.Sender, Contexts: d.ReplyContexts, State: state, RiskFlags: risks, ReviewFailure: d.ReviewFailure, ExpiresAt: at.AddDate(0, 0, cfg.RetentionDays), ReviewSettings: e.Config.GroundingConfig(), ReviewTransport: e.groundingTransport()}
	local, hasLocal := e.Tools.(interface{ RequestScopedMemory() bool })
	private, hasRoute := e.Tools.(PrivateReplyRouter)
	if in.PrivateQuote != "" || in.RequestScopedReply || hasLocal && local.RequestScopedMemory() || hasRoute && private.PrivateReplyRequired() {
		// Private app evidence stays in its source and in the current request.
		// Keep operational metadata without snapshotting the model payload.
		snapshot, a.Contexts, a.ReviewFailure = nil, nil, nil
	}
	if len(snapshot) > 0 {
		if prepared := e.preparedContext(); len(prepared) > 0 {
			b, _ := json.Marshal(map[string]any{"model_request": json.RawMessage(prepared), "review_input": json.RawMessage(snapshot), "decision": d})
			snapshot = b
		}
	}
	if len(snapshot) > 0 {
		a.Sources = e.auditSources(snapshot, scope)
		sum := sha256.Sum256(snapshot)
		a.ContextHash = hex.EncodeToString(sum[:])
		if cfg.Snapshots {
			a.Snapshot = redactAudit(snapshot)
			a.SnapshotKind = "grounding_review_input"
			if len(e.preparedContext()) > 0 {
				a.SnapshotKind = "model_request_and_grounding_review"
			}
		}
	}
	dir := filepath.Join(filepath.Dir(e.Store.path), "reply-audits", scope)
	if os.MkdirAll(dir, 0700) != nil {
		return
	}
	raw, _ := json.Marshal(a)
	tmp := filepath.Join(dir, id+".pending")
	if os.WriteFile(tmp, raw, 0600) == nil {
		_ = os.Rename(tmp, filepath.Join(dir, id+".json"))
	}
	metadata := a
	metadata.Snapshot = nil
	metadata.Messages = nil
	meta, _ := json.Marshal(metadata)
	_ = os.WriteFile(filepath.Join(dir, id+".meta.json"), meta, 0600)
	rows, _ := os.ReadDir(dir)
	cutoff := at.AddDate(0, 0, -cfg.RetentionDays)
	for _, r := range rows {
		if r.IsDir() || !strings.HasSuffix(r.Name(), ".json") {
			continue
		}
		info, err := r.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, r.Name()))
		}
	}
}
func redactAudit(raw []byte) json.RawMessage {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	var clean func(any) any
	clean = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, item := range x {
				key := strings.ToLower(k)
				if strings.Contains(key, "token") || strings.Contains(key, "password") || strings.Contains(key, "authorization") || strings.Contains(key, "secret") || strings.Contains(key, "api_key") || strings.Contains(key, "reasoning") {
					x[k] = "[redacted]"
				} else {
					x[k] = clean(item)
				}
			}
			return x
		case []any:
			for i := range x {
				x[i] = clean(x[i])
			}
			return x
		case string:
			var nested any
			if json.Unmarshal([]byte(x), &nested) == nil {
				b, _ := json.Marshal(clean(nested))
				return string(b)
			}
			x = regexp.MustCompile(`(?:sk-[A-Za-z0-9_-]{8,}|ghp_[A-Za-z0-9]{8,}|github_pat_[A-Za-z0-9_]{8,}|hf_[A-Za-z0-9]{8,})`).ReplaceAllString(x, "[redacted credential]")
			x = regexp.MustCompile(`(?i)(api[_-]?key|token|password|secret|authorization)([\s=:\"\']+)[^\s,\"\'&]+`).ReplaceAllString(x, "$1=[redacted]")
			if strings.Contains(strings.ToLower(x), "bearer ") {
				return "[redacted authentication text]"
			}
			return x
		default:
			return x
		}
	}
	out, _ := json.Marshal(clean(v))
	return out
}
func (s *Store) ReadReplyAudit(ctx context.Context, scope, id string, options ...ReplyAuditSettings) (ReplyAudit, error) {
	var a ReplyAudit
	if ctx.Err() != nil {
		return a, ctx.Err()
	}
	if !validScopeID(scope) || !validScopeID(id) {
		return a, errors.New("invalid audit identity")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(s.path), "reply-audits", scope, id+".json"))
	if err != nil {
		return a, err
	}
	if json.Unmarshal(raw, &a) != nil || a.Scope != scope || a.ID != id {
		return ReplyAudit{}, errors.New("audit identity mismatch")
	}
	cfg := ReplyAuditDefaults()
	if len(options) > 0 {
		cfg = options[0]
	}
	if !cfg.Enabled {
		return ReplyAudit{}, errors.New("reply audit disabled")
	}
	if cfg.RetentionDays > 0 && time.Since(a.At) > time.Duration(cfg.RetentionDays)*24*time.Hour {
		return ReplyAudit{}, errors.New("audit expired")
	}
	if !cfg.Snapshots {
		a.Snapshot = nil
	}
	if !a.ExpiresAt.IsZero() && time.Now().After(a.ExpiresAt) {
		return ReplyAudit{}, errors.New("audit expired")
	}
	for _, ref := range a.Sources {
		ev, err := s.ReadEvent(ctx, []string{ref.Scope}, ref.EventID)
		if err != nil || ev.Revision != ref.Revision {
			a.Snapshot = nil
			a.Messages = nil
			a.State = "source_unavailable"
		}
	}
	for _, r := range a.Contexts {
		for _, id := range r.SourceEventIDs {
			if _, err := s.ReadEvent(ctx, []string{scope}, id); err != nil {
				a.Snapshot = nil
				a.Messages = nil
				a.State = "source_unavailable"
			}
		}
	}
	return a, nil
}
func (s *Store) ReplyAudits(ctx context.Context, scope string, limit int, options ...ReplyAuditSettings) ([]ReplyAudit, error) {
	rows, _, err := s.FindReplyAudits(ctx, scope, "", 0, limit, options...)
	return rows, err
}

// FindReplyAudits filters small metadata sidecars before selecting a page;
// snapshot bodies are never loaded by list operations.
func (s *Store) FindReplyAudits(ctx context.Context, scope, messageID string, offset, limit int, options ...ReplyAuditSettings) ([]ReplyAudit, bool, error) {
	if !validScopeID(scope) || offset < 0 || limit < 0 {
		return nil, false, errors.New("invalid audit query")
	}
	if limit == 0 {
		limit = 50
	}
	cfg := ReplyAuditDefaults()
	if len(options) > 0 {
		cfg = options[0]
	}
	if !cfg.Enabled {
		return []ReplyAudit{}, false, nil
	}
	dir := filepath.Join(filepath.Dir(s.path), "reply-audits", scope)
	files, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []ReplyAudit{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	rows := []ReplyAudit{}
	for _, f := range files {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".meta.json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			continue
		}
		var a ReplyAudit
		if json.Unmarshal(raw, &a) != nil || a.Scope != scope || a.ID+".meta.json" != f.Name() {
			continue
		}
		if messageID != "" && a.TriggerMessageID != messageID {
			continue
		}
		if !a.ExpiresAt.IsZero() && time.Now().After(a.ExpiresAt) || cfg.RetentionDays > 0 && time.Since(a.At) > time.Duration(cfg.RetentionDays)*24*time.Hour {
			continue
		}
		a.Snapshot = nil
		a.Messages = nil
		rows = append(rows, a)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].At.After(rows[j].At) })
	if offset > len(rows) {
		return []ReplyAudit{}, false, nil
	}
	end := offset + min(limit, len(rows)-offset)
	return rows[offset:end], end < len(rows), nil
}

type preparedAuditContext struct {
	mu  sync.Mutex
	raw []byte
}

func (e *Engine) clearPreparedContext() {
	if e.prepared == nil {
		return
	}
	e.prepared.mu.Lock()
	e.prepared.raw = nil
	e.prepared.mu.Unlock()
}
func (e *Engine) capturePreparedContext(system string, turns []map[string]string, tools []mcptools.Tool) {
	if e.prepared == nil {
		return
	}
	raw, _ := json.Marshal(map[string]any{"system": system, "messages": turns, "tools": tools})
	e.prepared.mu.Lock()
	e.prepared.raw = raw
	e.prepared.mu.Unlock()
}
func (e *Engine) preparedContext() []byte {
	if e.prepared == nil {
		return nil
	}
	e.prepared.mu.Lock()
	defer e.prepared.mu.Unlock()
	return append([]byte(nil), e.prepared.raw...)
}

func (e *Engine) auditSources(raw []byte, scope string) []AuditSource {
	seen := map[string]bool{}
	out := []AuditSource{}
	var walk func(any, string)
	add := func(id, scope string) {
		key := scope + "/" + id
		if seen[key] || !validScopeID(scope) || !validScopeID(id) {
			return
		}
		seen[key] = true
		ev, err := e.Store.ReadEvent(context.Background(), []string{scope}, id)
		revision := 0
		if err == nil {
			revision = ev.Revision
		}
		out = append(out, AuditSource{scope, id, revision})
	}
	walk = func(v any, scope string) {
		switch x := v.(type) {
		case string:
			var nested any
			if json.Unmarshal([]byte(x), &nested) == nil {
				walk(nested, scope)
			}
		case []any:
			for _, item := range x {
				walk(item, scope)
			}
		case map[string]any:
			if value, ok := x["scope"].(string); ok && validScopeID(value) {
				scope = value
			}
			if _, ok := x["role"]; ok {
				if id, ok := x["id"].(string); ok {
					add(id, scope)
				}
			}
			for _, key := range []string{"event_id", "source_event_id"} {
				if id, ok := x[key].(string); ok {
					add(id, scope)
				}
			}
			if ids, ok := x["source_event_ids"].([]any); ok {
				for _, id := range ids {
					if id, ok := id.(string); ok {
						add(id, scope)
					}
				}
			}
			for _, item := range x {
				walk(item, scope)
			}
		}
	}
	var value any
	if json.Unmarshal(raw, &value) == nil {
		walk(value, scope)
	}
	return out
}
