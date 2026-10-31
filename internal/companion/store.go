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
	UserTextState     string             `json:"user_text_state,omitempty"`
	Source            *SourceRef         `json:"source,omitempty"`
	QuotedSource      *SourceRef         `json:"quoted_source,omitempty"`
	Addressees        []string           `json:"addressees,omitempty"`
	IncomingMedia     []IncomingMediaRef `json:"incoming_media,omitempty"`
	ReplyContext      *ReplyContext      `json:"reply_context,omitempty"`
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
	Preferences         string `json:"preferences,omitempty"`
	PreferencesRevision int    `json:"preferences_revision,omitempty"`
	RecallGeneration    int    `json:"recall_generation,omitempty"`
	Summary             string `json:"summary,omitempty"`
	CompressedCount     int    `json:"compressed_count,omitempty"`
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
	accessLease    *os.File
	sourceInstance string
	maintenanceMu  sync.Mutex
	db             *sql.DB
	compressionMu  sync.Mutex
	compressing    map[string]bool
	mu             sync.Mutex
	path           string
	data           persisted
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
		cloneTurnProvenance(&scope.Messages[i])
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
	if derivedObservationInput(input) {
		// A task may need a scope container for durable execution/receipt state,
		// but its runtime wrapper is never a new statement or user interaction.
		id := ScopeID(input.Channel, input.Account, input.Thread)
		s.mu.Lock()
		_, exists := s.data.Scopes[id]
		s.mu.Unlock()
		if exists {
			return nil
		}
		return s.scopeTransaction(id, func(data *persisted) error {
			if _, exists := data.Scopes[id]; exists {
				return nil
			}
			if len(data.Scopes) >= 512 {
				return errors.New("character scope limit reached")
			}
			data.Scopes[id] = Scope{ID: id, Channel: input.Channel, Account: input.Account, Thread: input.Thread, Messages: []Turn{}, Notes: []string{}}
			return nil
		})
	}
	userTextState := observedUserTextState(input)
	original := UserRequestText(input.Text)
	if input.OriginalText != nil {
		original = *input.OriginalText
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
		if input.Source != nil && input.Source.Integrity == "verified_import" {
			// Reconstructing an archived, verified source is not a fresh user
			// interaction and must not restart social/proactive timers.
			if at, e := time.Parse(time.RFC3339Nano, input.MessageTime); e == nil && at.After(scope.LastInteraction) {
				scope.LastInteraction = at
			}
		} else {
			scope.LastInteraction = now
		}
		if c.Permissions.Remember {
			source, err := s.normalizeSource(input, original, now)
			if err != nil {
				return err
			}
			for i, turn := range scope.Messages {
				if (input.TraceID != "" && turn.TraceID == input.TraceID || sameSourceMessage(turn.Source, source)) && turn.Role == "user" {
					if !sameSourceInstance(turn.Source, source) {
						continue
					}
					if sourceReplay(turn.Source, source) {
						return nil
					}
					if sourceCollision(turn, source, original, input.MessageTime) {
						markSourceConflict(&scope.Messages[i])
						if source == nil {
							source = legacyConflictSource(input, original, now, s.SourceInstanceID())
						}
						source.Integrity = "conflicted"
						continue
					}
					if turn.Sender != input.Sender {
						return errors.New("message identity sender changed")
					}
					if source != nil {
						if turn.Source != nil {
							source.ReceivedAt = turn.Source.ReceivedAt
							source.Sequence = turn.Source.Sequence
							source.Revision = max(source.Revision, turn.Source.Revision)
						}
						turn.Source = source
					}
					turn.ReplyContext = cloneReplyContext(input.ReplyContext)
					turn.QuotedSource = cloneSource(input.QuotedSource)
					turn.Addressees = append([]string(nil), input.Addressees...)
					turn.IncomingMedia = cloneIncomingMedia(input.IncomingMedia)
					turn.Text = boundedInputMemory(input.Text)
					turn.UserTextState = userTextState
					turn.FullText = original
					turn.HasOriginal = true
					turn.FullObservation = strings.TrimPrefix(input.Text, UserRequestText(input.Text))
					scope.Messages[i] = turn
					data.Scopes[id] = scope
					return nil
				}
			}
			stamp := now
			if parsed, err := time.Parse(time.RFC3339, input.MessageTime); err == nil && !parsed.After(now) {
				stamp = parsed
			}
			if source != nil && source.OccurredAt != "" {
				if at, e := time.Parse(time.RFC3339Nano, source.OccurredAt); e == nil {
					stamp = at
				}
			}
			observation := strings.TrimPrefix(input.Text, UserRequestText(input.Text))
			if input.OriginalText != nil && original == "" {
				observation = input.Text
			}
			scope.Messages = append(scope.Messages, Turn{UserTextState: userTextState, ReplyContext: cloneReplyContext(input.ReplyContext), Source: source, QuotedSource: cloneSource(input.QuotedSource), Addressees: append([]string(nil), input.Addressees...), IncomingMedia: cloneIncomingMedia(input.IncomingMedia), TraceID: input.TraceID, Role: "user", Text: boundedInputMemory(input.Text), FullText: original, HasOriginal: true, FullObservation: observation, Sender: boundedText(input.Sender, 128), SenderName: boundedText(input.SenderName, 256), SenderNameSource: input.SenderNameSource, Time: stamp})
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
	keptSources := scope.Messages[:0]
	for _, turn := range scope.Messages {
		if turn.Source == nil || turn.Source.Integrity != "conflicted" {
			keptSources = append(keptSources, turn)
		}
	}
	scope.Messages = keptSources
	if s.db != nil {
		rows, err := s.db.Query("SELECT event_id FROM memory_blocks WHERE scope=? UNION SELECT id FROM events WHERE scope=? AND suppressed!=0 UNION SELECT event_id FROM source_conflicts WHERE scope=?", scope.ID, scope.ID, scope.ID)
		if err != nil {
			return Scope{ID: scope.ID, Messages: []Turn{}}
		}
		blocked := map[string]bool{}
		for rows.Next() {
			var id string
			if rows.Scan(&id) != nil {
				rows.Close()
				return Scope{ID: scope.ID, Messages: []Turn{}}
			}
			blocked[id] = true
		}
		rowErr := rows.Err()
		rows.Close()
		if rowErr != nil {
			return Scope{ID: scope.ID, Messages: []Turn{}}
		}
		kept := []Turn{}
		for i, t := range scope.Messages {
			if !blocked[eventID(scope.ID, t, i)] {
				kept = append(kept, t)
			}
		}
		if len(kept) != len(scope.Messages) {
			scope.Messages = kept
			scope.CompressedCount = 0
			scope.Summary = ""
			scope.Notes = nil
		}
	}
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
		scope.Messages = append(scope.Messages, Turn{ReplyContext: cloneReplyContext(input.ReplyContext), Role: "assistant", Text: boundedText(text, 2048), FullText: text, SenderName: c.Name, SenderNameSource: "character", Time: now})
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

// Preferences are administrator-authored behavior, separate from remembered facts.
func (s *Store) Preferences(id string) (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := s.data.Scopes[id]
	return scope.Preferences, scope.PreferencesRevision
}
func (s *Store) SetPreferences(id, text string, revision int) error {
	text = strings.TrimSpace(text)
	if !validScopeID(id) || len(text) > 4000 || !utf8.ValidString(text) {
		return errors.New("偏好最多 4000 字节")
	}
	return s.scopeTransaction(id, func(data *persisted) error {
		scope, ok := data.Scopes[id]
		if !ok {
			return errors.New("会话不存在")
		}
		if scope.PreferencesRevision != revision {
			return errors.New("偏好已被更新，请重新打开后修改")
		}
		if scope.Preferences != text {
			scope.Preferences = text
			scope.PreferencesRevision++
		}
		data.Scopes[id] = scope
		return nil
	})
}
