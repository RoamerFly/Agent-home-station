package companion

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"modernc.org/sqlite"
)

// Use the same evidence classification for source validation and SQL selection.
// Filtering inside SQL keeps lexical/vector pagination from admitting old media
// observations as facts, without rewriting or deleting historical originals.
func init() {
	sqlite.MustRegisterDeterministicScalarFunction("memory_user_text_reliable", 3, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		value := func(v driver.Value) string {
			switch x := v.(type) {
			case string:
				return x
			case []byte:
				return string(x)
			}
			return ""
		}
		literal, state := memoryLiteralSource(value(args[0]), value(args[1]), []byte(value(args[2])))
		// A nonempty edited original may legitimately leave its old memory in
		// conflicted state for clarification. Revision handling owns that case;
		// this filter classifies evidence authorship, never promotes its facts.
		if strings.TrimSpace(literal) != "" && (state == "verified_original" || state == "legacy_original" || state == "admin_correction") {
			return int64(1), nil
		}
		return int64(0), nil
	})
}

func memoryLiteralSource(role, text string, payload []byte) (string, string) {
	var turn Turn
	if json.Unmarshal(payload, &turn) != nil {
		return "", "unknown"
	}
	if role == "admin_correction" {
		return text, "admin_correction"
	}
	if role != "user" {
		return "", "unknown"
	}
	if turn.UserTextState != "" && turn.UserTextState != "verified_original" {
		return "", "unknown"
	}
	// FullText is deliberately not serialized in the payload. The event body is
	// authoritative even when explicitly empty; display Text can contain media.
	turn.Role, turn.FullText, turn.HasOriginal = role, text, true
	if turn.UserTextState == "" && (turn.Media != nil || observedUserTextState(Input{Text: turn.Text, Source: turn.Source, IncomingMedia: turn.IncomingMedia}) == "unknown") {
		return "", "unknown"
	}
	return literalTurn(turn)
}

func reliableMemorySourcesSQL(alias string) string {
	return " AND NOT EXISTS(SELECT 1 FROM memory_sources reliable_ms JOIN events reliable_e ON reliable_e.id=reliable_ms.event_id WHERE reliable_ms.memory_id=" + alias + ".id AND memory_user_text_reliable(reliable_e.role,reliable_e.text,reliable_e.payload)=0)"
}

type AssertionSource struct {
	EventID       string `json:"event_id"`
	EventRevision int    `json:"event_revision"`
	Quote         string `json:"quote"`
}

// A binding names its evidence basis, not a numerical model confidence.
// Unknown participants stay unknown; library voice IDs are never identities.
type MemoryAssertion struct {
	Subject       string            `json:"subject"`
	Predicate     string            `json:"predicate"`
	Object        string            `json:"object"`
	AssertedBy    string            `json:"asserted_by"`
	State         string            `json:"state"`
	SubjectBasis  string            `json:"subject_basis"`
	SubjectText   string            `json:"subject_text"`
	ObjectBasis   string            `json:"object_basis"`
	ObjectText    string            `json:"object_text"`
	PredicateText string            `json:"predicate_text"`
	Negated       bool              `json:"negated,omitempty"`
	Sources       []AssertionSource `json:"sources"`
}

func sameAssertions(a, b []MemoryAssertion) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		x.Sources = nil
		y.Sources = nil
		if !reflect.DeepEqual(x, y) {
			return false
		}
	}
	return true
}
func enrichMemoryEvent(ev *MemoryEvent, raw []byte) {
	var turn Turn
	if json.Unmarshal(raw, &turn) != nil {
		return
	}
	ev.SenderName, ev.Media = turn.SenderName, turn.Media
	ev.Source, ev.QuotedSource = turn.Source, turn.QuotedSource
	ev.Addressees, ev.IncomingMedia = turn.Addressees, turn.IncomingMedia
	ev.UserTextState = turn.UserTextState
	if ev.Role == "user" && ev.UserTextState == "" {
		if _, state := memoryLiteralSource(ev.Role, ev.Text, raw); state == "unknown" {
			ev.UserTextState = state
		}
	}
}

func validateMemoryAssertions(ctx context.Context, tx *sql.Tx, m *MemoryEntry) error {
	if m.Status == "deleted" || len(m.Assertions) == 0 {
		return nil
	}
	if m.Continuity || m.Manual {
		return errors.New("typed assertions require user event evidence")
	}
	seen := map[string]bool{}
	for _, s := range m.Sources {
		seen[s.EventID+"\x00"+s.Quote] = true
	}
	for _, a := range m.Assertions {
		if a.Subject == "" || a.Predicate == "" || a.Object == "" || a.AssertedBy == "" || len(a.Sources) == 0 {
			return errors.New("assertion identity and sources required")
		}
		switch a.State {
		case "explicit", "reported", "corrected":
		case "uncertain":
			m.Inference = true
		default:
			return errors.New("invalid assertion evidence state")
		}
		if a.Subject != m.Subject {
			return errors.New("assertion subject differs from memory subject")
		}
		switch a.Predicate {
		case "father", "mother", "spouse", "child", "attribute", "reports":
		default:
			return errors.New("unsupported sourced assertion predicate")
		}
		for _, src := range a.Sources {
			var scope, text, role, sender string
			var revision, suppressed int
			var payload []byte
			if err := tx.QueryRowContext(ctx, "SELECT scope,text,role,sender,revision,suppressed,payload FROM events WHERE id=?", src.EventID).Scan(&scope, &text, &role, &sender, &revision, &suppressed, &payload); err != nil {
				return err
			}
			if scope != m.Scope || (role != "user" && !(role == "admin_correction" && a.State == "corrected")) || suppressed != 0 || revision != src.EventRevision || src.EventRevision < 1 || src.Quote == "" || !strings.Contains(text, src.Quote) || sender != a.AssertedBy {
				return errors.New("assertion source identity, revision or quote mismatch")
			}
			var blocked int
			if err := tx.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM memory_blocks WHERE scope=? AND event_id=?)+(SELECT COUNT(*) FROM source_conflicts WHERE event_id=?)", scope, src.EventID, src.EventID).Scan(&blocked); err != nil {
				return err
			}
			if blocked > 0 && m.Status != "deleted" {
				return errors.New("assertion source was forgotten")
			}
			var turn Turn
			if err := json.Unmarshal(payload, &turn); err != nil {
				return err
			}
			literal, reliability := memoryLiteralSource(role, text, payload)
			if reliability == "unknown" || src.Quote == "" || !strings.Contains(literal, src.Quote) {
				return errors.New("assertion requires verified user words, not media or runtime observations")
			}
			if role == "admin_correction" {
				if turn.QuotedSource == nil || turn.QuotedSource.EventID == "" {
					return errors.New("admin correction original source missing")
				}
				var originalRevision int
				if err := tx.QueryRowContext(ctx, "SELECT revision FROM events WHERE id=? AND scope=? AND suppressed=0 AND NOT EXISTS(SELECT 1 FROM source_conflicts sc WHERE sc.event_id=events.id) AND NOT EXISTS(SELECT 1 FROM memory_blocks mb WHERE mb.event_id=events.id AND mb.scope=events.scope)", turn.QuotedSource.EventID, m.Scope).Scan(&originalRevision); err != nil {
					return err
				}
				if originalRevision != turn.QuotedSource.Revision {
					return ErrMemoryConflict
				}
			}

			if !assertionBinding(a.Subject, a.SubjectBasis, a.SubjectText, a, turn, src.Quote, false) || !assertionBinding(a.Object, a.ObjectBasis, a.ObjectText, a, turn, src.Quote, true) {
				return errors.New("assertion participant binding is not supported by source")
			}
			if a.SubjectBasis == "relative" && !relativeSubjectBinding(a.Subject, a.SubjectText, turn, text) {
				return errors.New("relative subject is only present in quoted text")
			}
			if a.PredicateText == "" || !strings.Contains(src.Quote, a.PredicateText) {
				return errors.New("assertion predicate evidence missing")
			}
			if a.Subject != sender && a.State == "explicit" {
				return errors.New("third party assertion must retain reported attribution")
			}
			// Parenthood is a directional owner -> relative relation, not a guess
			// based on a voice label or a bare kinship word elsewhere in the quote.
			kin := map[string][]string{"father": {"爸", "爸爸", "父亲"}, "mother": {"妈", "妈妈", "母亲"}, "spouse": {"丈夫", "妻子", "爱人", "老公", "老婆"}, "child": {"孩子", "儿子", "女儿"}}
			if words, ok := kin[a.Predicate]; ok {
				if a.Negated {
					return errors.New("relative entity binding cannot negate the kinship predicate")
				}
				exact := false
				for _, word := range words {
					exact = exact || a.PredicateText == word
				}
				if !exact || a.ObjectBasis != "relative" || (!unquotedPossessive(src.Quote, a.SubjectText, a.PredicateText) || !unquotedPossessive(text, a.SubjectText, a.PredicateText)) {
					return errors.New("kinship requires an exact unquoted term attached to its bound possessor")
				}
			} else if !assertionPolaritySupported(a, src.Quote) {
				return errors.New("assertion negation is ambiguous or not attached to its predicate")
			}

			key := src.EventID + "\x00" + src.Quote
			if !seen[key] {
				m.Sources = append(m.Sources, MemorySource{src.EventID, src.Quote})
				seen[key] = true
			}
		}
	}
	return nil
}

func assertionBinding(id, basis, surface string, a MemoryAssertion, t Turn, quote string, object bool) bool {
	if surface == "" || !strings.Contains(quote, surface) {
		return false
	}
	switch basis {
	case "speaker":
		return id == t.Sender && (surface == "我" || surface == t.SenderName && t.SenderName != "")
	case "addressee":
		if surface != "你" && surface != "您" {
			return false
		}
		for _, target := range t.Addressees {
			if target == id {
				return true
			}
		}
		return false
	case "named":
		// A name is accepted only when this exact source already binds it to a
		// stable participant; matching a nickname elsewhere is insufficient.
		if id == t.Sender && t.SenderName != "" && surface == t.SenderName {
			return true
		}
		return t.QuotedSource != nil && id == t.QuotedSource.Sender && t.QuotedSource.SenderName != "" && surface == t.QuotedSource.SenderName
	case "literal":
		return object && id == surface && a.Predicate != "father" && a.Predicate != "mother" && a.Predicate != "child" && a.Predicate != "spouse"
	case "relative":
		if object {
			return id == "relative:"+a.Subject+":"+a.Predicate && surface == a.PredicateText
		}
		return relativeSubjectBinding(id, surface, t, quote)
	case "unknown":
		return a.State == "uncertain" && strings.HasPrefix(id, "unknown:")
	}
	return false
}

// SaveSourcedAdminCorrection records an authenticated management statement as
// its own source, never as a fabricated incoming WeChat message. The caller
// must verify current administrator authorization and target conversation.
func (s *Store) SaveSourcedAdminCorrection(ctx context.Context, scope, requester, originalID string, originalRevision int, detail string, assertions []MemoryAssertion) (MemoryEntry, error) {
	if requester == "" || strings.TrimSpace(detail) == "" || len(assertions) == 0 {
		return MemoryEntry{}, errors.New("admin correction requester, statement and assertions required")
	}
	original, err := s.ReadEvent(ctx, []string{scope}, originalID)
	if err != nil {
		return MemoryEntry{}, err
	}
	if original.Revision != originalRevision || original.Role != "user" {
		return MemoryEntry{}, ErrMemoryConflict
	}
	hash := sha256.Sum256([]byte(scope + "\x00" + requester + "\x00" + originalID + "\x00" + detail))
	eid := hex.EncodeToString(hash[:])
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.data.Scopes[scope]
	if !ok {
		return MemoryEntry{}, errors.New("unknown correction scope")
	}
	next := copyScope(old)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MemoryEntry{}, err
	}
	defer tx.Rollback()
	committed := false
	defer func() {
		if !committed {
			s.data.Scopes[scope] = old
		}
	}()
	found := false
	for _, t := range next.Messages {
		if t.EventID == eid {
			found = true
			break
		}
	}
	if !found {
		ref := original.Source
		if ref != nil {
			copyRef := *ref
			ref = &copyRef
		} else {
			ref = &SourceRef{MessageID: original.Trace, Sender: original.Sender, OccurredAt: original.At}
		}
		ref.EventID = original.ID
		ref.Revision = original.Revision
		next.Messages = append(next.Messages, Turn{EventID: eid, TraceID: "admin-correction-" + eid, Role: "admin_correction", Sender: requester, Text: detail, FullText: detail, HasOriginal: true, Time: now, QuotedSource: ref})
	}
	next.Summary = ""
	next.CompressedCount = 0
	next.SummaryBoundary = 0
	if err = s.writeScope(tx, scope, old, next); err != nil {
		return MemoryEntry{}, err
	}
	var revision int
	if err = tx.QueryRowContext(ctx, "SELECT revision FROM events WHERE id=? AND scope=?", eid, scope).Scan(&revision); err != nil {
		return MemoryEntry{}, err
	}
	candidate := append([]MemoryAssertion(nil), assertions...)
	for i := range candidate {
		if candidate[i].Subject != requester {
			return MemoryEntry{}, errors.New("admin correction subject must be the verified requester")
		}
		candidate[i].AssertedBy = requester
		candidate[i].State = "corrected"
		candidate[i].Sources = []AssertionSource{{EventID: eid, EventRevision: revision, Quote: detail}}
	}
	saved, err := s.saveMemory(ctx, MemoryEntry{Scope: scope, Subject: requester, Kind: "fact", Detail: detail, Assertions: candidate, Sources: []MemorySource{{eid, detail}, {originalID, original.Text}}}, 0, tx)
	if err != nil {
		return MemoryEntry{}, err
	}
	if err = tx.Commit(); err != nil {
		return MemoryEntry{}, err
	}
	s.data.Scopes[scope] = next
	committed = true
	return saved, nil

}

func (s *Store) validateAssertionCandidate(ctx context.Context, m MemoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return validateMemoryAssertions(ctx, tx, &m)
}

// unquotedPossessive accepts only an immediately attached possessor + exact
// kinship token. Pronouns inside reported quotation keep the quoted speaker's
// viewpoint and cannot establish a current participant relationship.
func unquotedPossessive(text, possessor, kin string) bool {
	r := []rune(text)
	depth := 0
	asciiQuote := rune(0)
	for i, c := range r {
		switch c {
		case '“', '‘', '「', '『', '《':
			depth++
			continue
		case '”', '’', '」', '』', '》':
			if depth > 0 {
				depth--
			}
			continue
		case 34, 39:
			if asciiQuote == c {
				asciiQuote = 0
			} else if asciiQuote == 0 {
				asciiQuote = c
			}
			continue
		}
		if depth != 0 || asciiQuote != 0 {
			continue
		}
		suffix := string(r[i:])
		for _, phrase := range []string{possessor + kin, possessor + "的" + kin} {
			if strings.HasPrefix(suffix, phrase) {
				return true
			}
		}
	}
	return false
}

// relativeSubjectBinding retains the possessor's stable identity when another
// statement concerns that relative (e.g. a parent's occupation).
func relativeSubjectBinding(id, surface string, t Turn, quote string) bool {
	if !strings.HasPrefix(id, "relative:") {
		return false
	}
	split := strings.LastIndex(id, ":")
	if split <= len("relative:") {
		return false
	}
	owner, relation := id[len("relative:"):split], id[split+1:]
	words := map[string][]string{"father": {"爸", "爸爸", "父亲"}, "mother": {"妈", "妈妈", "母亲"}, "spouse": {"丈夫", "妻子", "爱人", "老公", "老婆"}, "child": {"孩子", "儿子", "女儿"}}[relation]
	possessors := []string{}
	if owner == t.Sender {
		possessors = append(possessors, "我")
		if t.SenderName != "" {
			possessors = append(possessors, t.SenderName)
		}
	}
	for _, target := range t.Addressees {
		if target == owner {
			possessors = append(possessors, "你", "您")
		}
	}
	for _, possessor := range possessors {
		for _, word := range words {
			if surface == possessor+word || surface == possessor+"的"+word {
				return unquotedPossessive(quote, possessor, word)
			}
		}
	}
	return false
}

// Negation belongs to a specific subject/predicate clause. A negative word
// elsewhere in the source cannot negate an unrelated relation.
func assertionPolaritySupported(a MemoryAssertion, quote string) bool {
	hasNegative := false
	for _, word := range []string{"不", "没", "并非", "未"} {
		hasNegative = hasNegative || strings.Contains(quote, word)
	}
	if !hasNegative && !a.Negated {
		return true
	}
	predicates := []string{a.PredicateText}
	if !strings.HasSuffix(a.PredicateText, a.ObjectText) {
		predicates = []string{a.PredicateText + a.ObjectText}
	}
	positive, negative := false, false
	for _, pred := range predicates {
		positive = positive || strings.Contains(quote, a.SubjectText+pred)
		for _, neg := range []string{"不", "没有", "没", "未"} {
			negative = negative || strings.Contains(quote, a.SubjectText+neg+pred)
		}
	}
	// “并非/不是 + object” is a negated copula, not a negated subject.
	if a.PredicateText == "是" || a.PredicateText == "是"+a.ObjectText {
		for _, neg := range []string{"不是", "并非"} {
			negative = negative || strings.Contains(quote, a.SubjectText+neg+a.ObjectText)
		}
	}
	return positive != negative && a.Negated == negative
}
