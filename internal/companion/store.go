package companion

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Turn struct {
	EventID           string             `json:"event_id,omitempty"`
	FullText          string             `json:"-"`
	HasOriginal       bool               `json:"-"`
	FullObservation   string             `json:"-"`
	DeviceObservation *DeviceObservation `json:"device_observation,omitempty"`
	Media             *MediaMemory       `json:"media,omitempty"`
	SenderName        string             `json:"sender_name,omitempty"`
	SenderNameSource  string             `json:"sender_name_source,omitempty"`
	TraceID           string             `json:"message_id,omitempty"`
	Role              string             `json:"role"`
	Text              string             `json:"text"`
	Sender            string             `json:"sender"`
	Time              time.Time          `json:"time"`
}
type Scope struct {
	RecallGeneration int    `json:"recall_generation,omitempty"`
	Summary          string `json:"summary,omitempty"`
	CompressedCount  int    `json:"compressed_count,omitempty"`
	// SummaryBoundary is the conversation snapshot length at compression time.
	// The UI folds that snapshot, while Context still retains the recent tail.
	SummaryBoundary int           `json:"summary_boundary,omitempty"`
	SummaryUpdated  time.Time     `json:"summary_updated,omitempty"`
	ID              string        `json:"id"`
	Channel         string        `json:"channel"`
	Account         string        `json:"account"`
	Thread          string        `json:"thread"`
	Messages        []Turn        `json:"messages"`
	Notes           []string      `json:"notes"`
	LastInteraction time.Time     `json:"last_interaction"`
	LastActivity    time.Time     `json:"last_activity,omitempty"`
	LastDecision    time.Time     `json:"last_decision"`
	LastProactive   time.Time     `json:"last_proactive"`
	ProactiveDay    string        `json:"proactive_day,omitempty"`
	ProactiveCount  int           `json:"proactive_count,omitempty"`
	NextProactiveAt time.Time     `json:"next_proactive_at,omitempty"`
	Autonomy        AutonomyStats `json:"autonomy,omitempty"`
}
type ScopeSummary struct {
	ID              string    `json:"id"`
	Channel         string    `json:"channel"`
	Thread          string    `json:"thread"`
	Label           string    `json:"label"`
	MessageCount    int       `json:"message_count"`
	NoteCount       int       `json:"note_count"`
	LastInteraction time.Time `json:"last_interaction"`
	CompressedCount int       `json:"compressed_count,omitempty"`
	VisibleCount    int       `json:"visible_count"`
	HasSummary      bool      `json:"has_summary"`
}
type persisted struct {
	Version        int              `json:"version"`
	Scopes         map[string]Scope `json:"scopes"`
	ReplyHour      string           `json:"reply_hour"`
	Replies        int              `json:"replies"`
	ProactiveDay   string           `json:"proactive_day"`
	ProactiveCount int              `json:"proactive_count"`
	DecisionHour   string           `json:"decision_hour"`
	Decisions      int              `json:"decisions"`
}
type Store struct {
	db            *sql.DB
	compressionMu sync.Mutex
	compressing   map[string]bool
	mu            sync.Mutex
	path          string
	data          persisted
}

func ScopeID(channel, account, thread string) string {
	sum := sha256.Sum256([]byte(channel + "\x00" + account + "\x00" + thread))
	return hex.EncodeToString(sum[:])
}
func NewStore(dir string) (*Store, error) {
	return openSQLiteStore(dir)
}

func readLegacyStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errors.New("character memory directory unavailable")
	}
	s := &Store{path: filepath.Join(dir, "memory.json"), data: persisted{Version: 1, Scopes: map[string]Scope{}}}
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, errors.New("character memory unavailable")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (64<<20)+1))
	if err != nil || len(b) > 64<<20 || json.Unmarshal(b, &s.data) != nil || s.data.Version != 1 || s.data.Scopes == nil || len(s.data.Scopes) > 512 {
		return nil, errors.New("character memory invalid; not reset automatically")
	}
	for id, scope := range s.data.Scopes {
		if !validScopeID(id) || scope.ID != id || ScopeID(scope.Channel, scope.Account, scope.Thread) != id || len(scope.Messages) > 2000 || len(scope.Notes) > 20 {
			return nil, errors.New("character memory scope invalid")
		}
	}
	return s, nil
}
func validScopeID(id string) bool { b, err := hex.DecodeString(id); return err == nil && len(b) == 32 }
func boundedText(text string, max int) string {
	if len(text) <= max {
		return text
	}
	for max > 0 && !utf8.ValidString(text[:max]) {
		max--
	}
	return text[:max]
}
func copyScope(scope Scope) Scope {
	scope.Messages = append([]Turn{}, scope.Messages...)
	for i := range scope.Messages {
		if scope.Messages[i].Media != nil {
			v := *scope.Messages[i].Media
			scope.Messages[i].Media = &v
		}
	}
	scope.Notes = append([]string{}, scope.Notes...)
	return scope
}

// Transactions become visible only after an atomic, private write. No caller
// can acquire a send/proactive budget from a failed persistence operation.
func (s *Store) transaction(change func(*persisted) error) error {
	return s.transact(nil, change)
}
func prune(scope Scope, c Config, now time.Time) Scope {
	cutoff := now.Add(-time.Duration(c.MemoryDays) * 24 * time.Hour)
	kept := make([]Turn, 0, len(scope.Messages))
	folded, compressed, boundary := scope.FoldedHistoryCount(), 0, 0
	for index, turn := range scope.Messages {
		if !turn.Time.Before(cutoff) {
			kept = append(kept, turn)
			if index < scope.CompressedCount {
				compressed++
			}
			if index < folded {
				boundary++
			}
		}
	}
	trimmed := max(0, len(kept)-c.MemoryMessages)
	if len(kept) > c.MemoryMessages {
		kept = kept[len(kept)-c.MemoryMessages:]
	}
	scope.CompressedCount = max(0, compressed-trimmed)
	scope.SummaryBoundary = max(0, boundary-trimmed)
	scope.Messages = kept
	return scope
}
func (s *Store) Observe(input Input, c Config, now time.Time) error {
	if input.Preview {
		return nil
	}
	return s.scopeTransaction(ScopeID(input.Channel, input.Account, input.Thread), func(data *persisted) error {
		id := ScopeID(input.Channel, input.Account, input.Thread)
		scope, exists := data.Scopes[id]
		if !exists {
			if len(data.Scopes) >= 512 {
				return errors.New("character scope limit reached")
			}
			scope = Scope{ID: id, Channel: input.Channel, Account: input.Account, Thread: input.Thread, Messages: []Turn{}, Notes: []string{}}
		}
		var err error
		scope, err = s.pruneStored(scope, c, now)
		if err != nil {
			return err
		}
		scope.LastInteraction = now
		if c.Permissions.Remember {
			for _, turn := range scope.Messages {
				if input.TraceID != "" && turn.TraceID == input.TraceID && turn.Role == "user" {
					data.Scopes[id] = scope
					return nil
				}
			}
			stamp := now
			if parsed, err := time.Parse(time.RFC3339, input.MessageTime); err == nil && !parsed.After(now) {
				stamp = parsed
			}
			scope.Messages = append(scope.Messages, Turn{TraceID: input.TraceID, Role: "user", Text: boundedInputMemory(input.Text), FullText: UserRequestText(input.Text), HasOriginal: true, FullObservation: strings.TrimPrefix(input.Text, UserRequestText(input.Text)), Sender: boundedText(input.Sender, 128), SenderName: boundedText(input.SenderName, 256), SenderNameSource: input.SenderNameSource, Time: stamp})
			scope, err = s.pruneStored(scope, c, now)
			if err != nil {
				return err
			}
		}
		data.Scopes[id] = scope
		return nil
	})
}
func (s *Store) Context(input Input, c Config, now time.Time) Scope {
	if input.Preview {
		return Scope{Messages: []Turn{}, Notes: []string{}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := prune(copyScope(s.data.Scopes[ScopeID(input.Channel, input.Account, input.Thread)]), c, now)
	scope.Messages = scope.Messages[min(scope.CompressedCount, len(scope.Messages)):]
	if !c.Permissions.Remember {
		scope.Summary = ""
		scope.Messages = []Turn{}
		scope.Notes = []string{}
	}
	return scope
}
func (s *Store) Accepted(input Input, text string, c Config, now time.Time) error {
	if input.Preview || !c.Permissions.Remember {
		return nil
	}
	return s.scopeTransaction(ScopeID(input.Channel, input.Account, input.Thread), func(data *persisted) error {
		id := ScopeID(input.Channel, input.Account, input.Thread)
		scope, ok := data.Scopes[id]
		if !ok {
			return errors.New("character scope missing")
		}
		scope.Messages = append(scope.Messages, Turn{Role: "assistant", Text: boundedText(text, 2048), FullText: text, SenderName: c.Name, SenderNameSource: "character", Time: now})
		recordAutonomyAccepted(&scope, input, c, now)
		clean, err := s.pruneStored(scope, c, now)
		if err != nil {
			return err
		}
		data.Scopes[id] = clean
		return nil
	})
}
func (s *Store) ClaimDecision(input Input, c Config, now time.Time) error {
	if input.Preview {
		return nil
	}
	return s.scopeTransaction(ScopeID(input.Channel, input.Account, input.Thread), func(data *persisted) error {
		hour := now.UTC().Format("2006-01-02T15")
		if data.DecisionHour != hour {
			data.DecisionHour = hour
			data.Decisions = 0
		}
		id := ScopeID(input.Channel, input.Account, input.Thread)
		scope, ok := data.Scopes[id]
		if !ok {
			return errors.New("character scope missing")
		}
		scope.LastDecision = now
		data.Scopes[id] = scope
		data.Decisions++
		return nil
	})
}
func (s *Store) ClaimReply(c Config, now time.Time) error {
	return s.scopeTransaction("", func(data *persisted) error {
		hour := now.UTC().Format("2006-01-02T15")
		if data.ReplyHour != hour {
			data.ReplyHour = hour
			data.Replies = 0
		}
		// Retain counters for observability, not as a sending quota.
		data.Replies++
		return nil
	})
}
func (s *Store) Counters(c Config, now time.Time) (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hour, day := 0, 0
	if s.data.ReplyHour == now.UTC().Format("2006-01-02T15") {
		hour = s.data.Replies
	}
	if s.data.ProactiveDay == localDay(c, now) {
		day = s.data.ProactiveCount
	}
	return hour, day
}
func localDay(c Config, now time.Time) string {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return now.UTC().Format("2006-01-02")
	}
	return now.In(loc).Format("2006-01-02")
}
func (s *Store) ProactiveCandidates(channel, account string, c Config, now time.Time) []Input {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Input{}
	for _, thread := range c.ProactiveThreads {
		if s.proactiveStatusLocked(channel, account, thread, c, now).Reason != "ready" {
			continue
		}
		out = append(out, Input{Channel: channel, Account: account, Thread: thread, Mode: "proactive", IsGroup: strings.HasSuffix(thread, "@chatroom"), Text: "这是一次获准的自主活动评估。结合最新群聊、关系记忆、自己的兴趣和未完成事情，决定自然接话、真实查找并分享有趣内容，或安静。可以使用当前可用工具，不必等待冷场，不重复已发内容；也可以用wake_after_seconds安排下次评估，不催促回复。"})
	}
	return out
}
func (s *Store) ClaimProactive(input Input, c Config, now time.Time) error {
	return s.scopeTransaction(ScopeID(input.Channel, input.Account, input.Thread), func(data *persisted) error {
		// Persist the evaluation and its next randomized due time before the
		// model runs. This is restart-safe scheduling, not a sending allowance.
		if state := proactiveStatus(*data, input.Channel, input.Account, input.Thread, c, now); state.Reason != "ready" {
			return errors.New("proactive unavailable: " + state.Reason)
		}
		day := localDay(c, now)
		if data.ProactiveDay != day {
			data.ProactiveDay = day
			data.ProactiveCount = 0
		}
		id := ScopeID(input.Channel, input.Account, input.Thread)
		scope, ok := data.Scopes[id]
		if !ok {
			return errors.New("proactive conversation missing")
		}
		// A crash or silence keeps the next due time, but never exhausts a
		// daily allowance. Real side effects use their own delivery ledger.
		scope.ProactiveCount = proactiveToday(scope, c, now) + 1
		scope.ProactiveDay = day
		scope.LastProactive = now
		scope.NextProactiveAt = nextProactiveEvaluation(c.ProactiveRule(input.Thread), now)
		data.Scopes[id] = scope
		data.ProactiveCount++
		return nil
	})
}
func (s *Store) Summaries(c Config, now time.Time) []ScopeSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ScopeSummary{}
	for _, stored := range s.data.Scopes {
		scope := prune(copyScope(stored), c, now)
		out = append(out, ScopeSummary{ID: scope.ID, Channel: scope.Channel, Thread: scope.Thread, Label: scope.Thread, MessageCount: len(scope.Messages), NoteCount: len(scope.Notes), LastInteraction: scope.LastInteraction, CompressedCount: scope.CompressedCount, VisibleCount: len(scope.Messages) - scope.FoldedHistoryCount(), HasSummary: scope.Summary != ""})
	}
	return out
}
func (s *Store) Read(id string, c Config, now time.Time) (Scope, error) {
	if !validScopeID(id) {
		return Scope{}, errors.New("invalid memory scope")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	scope, ok := s.data.Scopes[id]
	if !ok {
		return Scope{}, errors.New("memory scope not found")
	}
	return prune(copyScope(scope), c, now), nil
}
func (s *Store) Note(id, text string) error {
	text = strings.TrimSpace(text)
	if !validScopeID(id) || text == "" || len(text) > 1024 || !utf8.ValidString(text) {
		return errors.New("关系备注需要 1 至 1024 字节的文字")
	}
	return s.scopeTransaction(id, func(data *persisted) error {
		scope, ok := data.Scopes[id]
		if !ok {
			return errors.New("会话不存在")
		}
		if len(scope.Notes) >= 20 {
			return errors.New("每个会话最多保存 20 条关系备注")
		}
		scope.Notes = append(scope.Notes, text)
		data.Scopes[id] = scope
		return nil
	})
}
func (s *Store) Clear(id string) error {
	if !validScopeID(id) {
		return errors.New("invalid memory scope")
	}
	return s.scopeTransaction(id, func(data *persisted) error {
		scope, ok := data.Scopes[id]
		if !ok {
			return errors.New("memory scope not found")
		}
		// Clearing content must not reset send claims or proactive timing.
		scope.Messages = []Turn{}
		scope.Notes = []string{}
		scope.Summary = ""
		scope.CompressedCount = 0
		scope.SummaryBoundary = 0
		scope.SummaryUpdated = time.Time{}
		scope.RecallGeneration++
		data.Scopes[id] = scope
		return nil
	})
}

func (s *Store) Expire(c Config, now time.Time) error {
	s.mu.Lock()
	changed := false
	for _, scope := range s.data.Scopes {
		clean := prune(copyScope(scope), c, now)
		if len(clean.Messages) != len(scope.Messages) {
			changed = true
			break
		}
	}
	s.mu.Unlock()
	if !changed {
		return nil
	}
	return s.transaction(func(data *persisted) error {
		for id, scope := range data.Scopes {
			clean, err := s.pruneStored(scope, c, now)
			if err != nil {
				return err
			}
			data.Scopes[id] = clean
		}
		return nil
	})
}
