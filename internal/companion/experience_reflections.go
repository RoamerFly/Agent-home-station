package companion

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidExperienceReflection is a caller-correctable validation error.
// Stale source evidence and optimistic revision conflicts use ErrMemoryConflict
// instead, so callers never retry an old observation as if it were new data.
var ErrInvalidExperienceReflection = errors.New("invalid experience reflection")

// ObservedExperienceReflectionCandidate is compact, verified host observation
// evidence that a model may choose to revisit. It deliberately has no title or
// publisher-only fallback: those are not a lived/read experience.
type ObservedExperienceReflectionCandidate struct {
	SourceEventID            string `json:"source_event_id"`
	SourceEventRevision      int    `json:"source_event_revision"`
	SourceAssetID            string `json:"source_asset_id"`
	SourceObservationVersion int    `json:"source_observation_version"`
	SourceObservationHash    string `json:"source_observation_hash"`
	Summary                  string `json:"summary"`
	MediaKind                string `json:"media_kind"`
	ObservedRange            string `json:"observed_range"`
}

// ExperienceReflectionInput may be persisted only after the model explicitly
// selected a verified candidate as something it wants to revisit.
type ExperienceReflectionInput struct {
	Scope                    string `json:"scope"`
	SourceEventID            string `json:"source_event_id"`
	SourceEventRevision      int    `json:"source_event_revision"`
	SourceAssetID            string `json:"source_asset_id"`
	SourceObservationVersion int    `json:"source_observation_version"`
	SourceObservationHash    string `json:"source_observation_hash"`
	ParentExperienceID       string `json:"parent_experience_id,omitempty"`
	ParentExperienceVersion  int    `json:"parent_experience_version,omitempty"`
	Note                     string `json:"note"`
	WantsToRevisit           bool   `json:"wants_to_revisit"`
}

type ExperienceReflectionSource struct {
	EventID            string `json:"event_id"`
	AssetID            string `json:"asset_id"`
	Sender             string `json:"sender,omitempty"`
	SenderName         string `json:"sender_name,omitempty"`
	At                 string `json:"at,omitempty"`
	Revision           int    `json:"revision"`
	ObservationVersion int    `json:"observation_version"`
	ObservationHash    string `json:"observation_hash"`
	ObservationExcerpt string `json:"observation_excerpt,omitempty"`
	ObservedRange      string `json:"observed_range,omitempty"`
	MediaKind          string `json:"media_kind,omitempty"`
}

type ExperienceReflectionParent struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Detail  string `json:"detail,omitempty"`
}

// ExperienceReflection is a fish-owned, sourced interest note. Revision is
// independent of a source event revision, so management edits cannot silently
// overwrite each other.
type ExperienceReflection struct {
	ID                       string                      `json:"id"`
	Scope                    string                      `json:"scope"`
	SourceEventID            string                      `json:"source_event_id"`
	SourceEventRevision      int                         `json:"source_event_revision"`
	SourceAssetID            string                      `json:"source_asset_id"`
	SourceObservationVersion int                         `json:"source_observation_version"`
	SourceObservationHash    string                      `json:"source_observation_hash"`
	ParentExperienceID       string                      `json:"parent_experience_id,omitempty"`
	ParentExperienceVersion  int                         `json:"parent_experience_version,omitempty"`
	Note                     string                      `json:"note"`
	WantsToRevisit           bool                        `json:"wants_to_revisit"`
	Status                   string                      `json:"status"`
	Revision                 int                         `json:"revision"`
	Created                  time.Time                   `json:"created"`
	Updated                  time.Time                   `json:"updated"`
	Source                   *ExperienceReflectionSource `json:"source,omitempty"`
	ParentExperience         *ExperienceReflectionParent `json:"parent_experience,omitempty"`
}

type ExperienceReflectionQuery struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type ExperienceReflectionPage struct {
	Items          []ExperienceReflection `json:"items"`
	NextCursor     string                 `json:"next_cursor,omitempty"`
	HasMore        bool                   `json:"has_more"`
	EffectiveLimit int                    `json:"effective_limit"`
	Coverage       string                 `json:"coverage"`
}

const reflectionCandidatesSQL = `WITH observed AS (
    SELECT e.scope,e.id,e.seq,e.revision,media.value AS attachment,
        COALESCE(NULLIF(json_extract(media.value,'$.asset_id'),''),
            CASE WHEN json_array_length(CAST(e.payload AS TEXT),'$.incoming_media')=1 THEN 'event:'||e.id END) AS media_id
    FROM events e CROSS JOIN json_each(CAST(e.payload AS TEXT),'$.incoming_media') media
    WHERE e.scope=? AND e.role='user' AND e.suppressed=0
        AND instr(CAST(e.payload AS TEXT),'"observation_excerpt":')>0
        AND NOT EXISTS(SELECT 1 FROM memory_blocks b WHERE b.scope=e.scope AND b.event_id=e.id)
        AND NOT EXISTS(SELECT 1 FROM source_conflicts c WHERE c.event_id=e.id)
)
SELECT o.id,o.revision,o.attachment,o.media_id FROM observed o
WHERE o.media_id IS NOT NULL AND NOT EXISTS(
    SELECT 1 FROM experience_reflections r WHERE r.scope=o.scope AND r.source_event_id=o.id
        AND json_extract(r.payload,'$.source_asset_id')=o.media_id)
ORDER BY o.seq DESC,o.id DESC`

// ListObservedExperienceReflectionCandidates returns only a real, current
// observation with an explicit range and revision. A share's card title or its
// publisher text alone never enters this list.
func (s *Store) ListObservedExperienceReflectionCandidates(ctx context.Context, scope string, limit int) ([]ObservedExperienceReflectionCandidate, error) {
	if !validScopeID(scope) || limit < 0 {
		return nil, fmt.Errorf("%w: invalid observed experience query", ErrInvalidExperienceReflection)
	}
	if limit == 0 {
		limit = 12
	}
	rows, err := s.db.QueryContext(ctx, reflectionCandidatesSQL, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ObservedExperienceReflectionCandidate{}
	for rows.Next() {
		ev := MemoryEvent{Role: "user", Scope: scope}
		var raw []byte
		var mediaID string
		if err := rows.Scan(&ev.ID, &ev.Revision, &raw, &mediaID); err != nil {
			return nil, err
		}
		var media IncomingMediaRef
		if err := json.Unmarshal(raw, &media); err != nil {
			return nil, err
		}
		if candidate, ok := observedExperienceReflectionCandidateWithIdentity(ev, media, mediaID); ok {
			out = append(out, candidate)
			if len(out) == limit {
				break
			}
		}
	}
	return out, rows.Err()
}

func reflectionMediaIdentity(ev MemoryEvent, index int) string {
	if index < 0 || index >= len(ev.IncomingMedia) {
		return ""
	}
	if ev.IncomingMedia[index].AssetID != "" {
		return ev.IncomingMedia[index].AssetID
	}
	// Several unkeyed observations cannot be distinguished after a correction.
	// One is still a stable, event-scoped host observation and is safe to use.
	if len(ev.IncomingMedia) == 1 {
		return "event:" + ev.ID
	}
	return ""
}

func observedExperienceReflectionCandidateWithIdentity(ev MemoryEvent, media IncomingMediaRef, sourceAssetID string) (ObservedExperienceReflectionCandidate, bool) {
	if ev.Role != "user" || ev.ID == "" || ev.Revision < 1 || sourceAssetID == "" || media.Source.EventID != ev.ID || media.Source.Revision != ev.Revision ||
		media.ObservationVersion < 1 || strings.TrimSpace(media.ObservedRange) == "" || strings.TrimSpace(media.ObservationExcerpt) == "" || !observationAvailable(media) {
		return ObservedExperienceReflectionCandidate{}, false
	}
	hash := experienceObservationHash(ev.ID, ev.Revision, media)
	return ObservedExperienceReflectionCandidate{
		SourceEventID: ev.ID, SourceEventRevision: ev.Revision, SourceAssetID: sourceAssetID, SourceObservationVersion: media.ObservationVersion,
		SourceObservationHash: hash, Summary: boundedText(strings.TrimSpace(media.ObservationExcerpt), 600), MediaKind: media.Kind, ObservedRange: boundedText(strings.TrimSpace(media.ObservedRange), 256),
	}, true
}

func experienceObservationHash(eventID string, revision int, media IncomingMediaRef) string {
	raw := strings.Join([]string{eventID, fmt.Sprint(revision), media.AssetID, media.Kind, fmt.Sprint(media.ObservationVersion), media.ObservedRange, media.ObservationExcerpt}, "\x00")
	hash := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(hash[:])
}

func (s *Store) SaveExperienceReflection(ctx context.Context, in ExperienceReflectionInput) (ExperienceReflection, error) {
	var out ExperienceReflection
	in.Note = strings.TrimSpace(in.Note)
	if !validScopeID(in.Scope) || in.SourceEventID == "" || in.SourceAssetID == "" || in.SourceEventRevision < 1 || in.SourceObservationVersion < 1 || in.SourceObservationHash == "" ||
		!in.WantsToRevisit || in.Note == "" || len(in.Note) > 1200 || in.ParentExperienceVersion < 0 || (in.ParentExperienceID == "") != (in.ParentExperienceVersion == 0) {
		return out, fmt.Errorf("%w: sourced explicit revisit note required", ErrInvalidExperienceReflection)
	}
	identity, _ := json.Marshal([]string{in.Scope, in.SourceEventID, in.SourceAssetID})
	sum := sha256.Sum256(identity)
	out = ExperienceReflection{ID: hex.EncodeToString(sum[:]), Scope: in.Scope, SourceEventID: in.SourceEventID, SourceEventRevision: in.SourceEventRevision, SourceAssetID: in.SourceAssetID, SourceObservationVersion: in.SourceObservationVersion, SourceObservationHash: in.SourceObservationHash, ParentExperienceID: in.ParentExperienceID, ParentExperienceVersion: in.ParentExperienceVersion, Note: in.Note, WantsToRevisit: true, Status: "active"}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	candidate, err := observedExperienceReflectionCandidateFromRow(ctx, tx.QueryRowContext, in.Scope, in.SourceEventID, in.SourceAssetID)
	if err != nil || candidate.SourceEventRevision != in.SourceEventRevision || candidate.SourceObservationVersion != in.SourceObservationVersion || candidate.SourceObservationHash != in.SourceObservationHash {
		return out, ErrMemoryConflict
	}
	if in.ParentExperienceID != "" {
		parent, readErr := readMemory(ctx, tx.QueryRowContext, []string{in.Scope}, in.ParentExperienceID)
		if readErr != nil || parent.Kind != "episode" || parent.Status != "active" || parent.Version != in.ParentExperienceVersion || !memoryCitesEvent(parent, in.SourceEventID) {
			return out, ErrMemoryConflict
		}
	}
	var raw []byte
	var existingRevision int
	var existingStatus string
	err = tx.QueryRowContext(ctx, "SELECT payload,revision,status FROM experience_reflections WHERE id=? AND scope=?", out.ID, out.Scope).Scan(&raw, &existingRevision, &existingStatus)
	if err == nil {
		var existing ExperienceReflection
		if err = json.Unmarshal(raw, &existing); err != nil {
			return out, err
		}
		if existingStatus != "active" || existing.SourceObservationHash != in.SourceObservationHash {
			return out, ErrMemoryConflict
		}
		existing.Revision, existing.Status = existingRevision, existingStatus
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return out, rollbackErr
		}
		return s.hydrateExperienceReflection(ctx, existing)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	out.Revision, out.Created, out.Updated = 1, now, now
	payload, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO experience_reflections(id,scope,source_event_id,source_event_revision,source_observation_version,source_observation_hash,parent_experience_id,parent_experience_version,status,revision,created,updated,payload) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, out.ID, out.Scope, out.SourceEventID, out.SourceEventRevision, out.SourceObservationVersion, out.SourceObservationHash, out.ParentExperienceID, out.ParentExperienceVersion, out.Status, out.Revision, memoryTime(out.Created), memoryTime(out.Updated), payload); err != nil {
		return out, err
	}
	if err = bumpMemoryEpoch(tx); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return s.hydrateExperienceReflection(ctx, out)
}

func (s *Store) UpdateExperienceReflection(ctx context.Context, scope, id string, expectedRevision int, note string) (ExperienceReflection, error) {
	return s.mutateExperienceReflection(ctx, scope, id, expectedRevision, strings.TrimSpace(note), false)
}

func (s *Store) ForgetExperienceReflection(ctx context.Context, scope, id string, expectedRevision int) (ExperienceReflection, error) {
	return s.mutateExperienceReflection(ctx, scope, id, expectedRevision, "", true)
}

func (s *Store) mutateExperienceReflection(ctx context.Context, scope, id string, expectedRevision int, note string, forget bool) (ExperienceReflection, error) {
	var out ExperienceReflection
	if !validScopeID(scope) || strings.TrimSpace(id) == "" || expectedRevision < 1 || !forget && (note == "" || len(note) > 1200) {
		return out, fmt.Errorf("%w: invalid reflection update", ErrInvalidExperienceReflection)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var raw []byte
	var revision int
	var status string
	if err = tx.QueryRowContext(ctx, "SELECT payload,revision,status FROM experience_reflections WHERE id=? AND scope=?", id, scope).Scan(&raw, &revision, &status); err != nil {
		return out, err
	}
	if revision != expectedRevision || status != "active" {
		return out, ErrMemoryConflict
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	if !forget {
		candidate, evidenceErr := observedExperienceReflectionCandidateFromRow(ctx, tx.QueryRowContext, scope, out.SourceEventID, out.SourceAssetID)
		if evidenceErr != nil || candidate.SourceEventRevision != out.SourceEventRevision || candidate.SourceObservationVersion != out.SourceObservationVersion || candidate.SourceObservationHash != out.SourceObservationHash {
			return out, ErrMemoryConflict
		}
	}
	out.Revision, out.Updated = revision+1, time.Now().UTC()
	if forget {
		out.Status = "forgotten"
	} else {
		out.Note = note
	}
	payload, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO experience_reflection_revisions(reflection_id,revision,payload) VALUES(?,?,?)", id, revision, raw); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE experience_reflections SET status=?,revision=?,updated=?,payload=? WHERE id=? AND scope=? AND revision=?", out.Status, out.Revision, memoryTime(out.Updated), payload, id, scope, revision); err != nil {
		return out, err
	}
	if err = bumpMemoryEpoch(tx); err != nil {
		return out, err
	}
	if err = bumpRecallEpoch(tx); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	// Forgetting is a revocation, not an assertion that the old source can
	// still be read. In particular, an administrator must be able to hide a
	// reflection after its observation has been corrected or withdrawn.
	// Hydration deliberately rechecks that evidence and would otherwise turn a
	// committed forget into a misleading conflict response.
	if forget {
		return out, nil
	}
	return s.hydrateExperienceReflection(ctx, out)
}

func (s *Store) ListExperienceReflections(ctx context.Context, scope string, q ExperienceReflectionQuery) (ExperienceReflectionPage, error) {
	p := ExperienceReflectionPage{Items: []ExperienceReflection{}, Coverage: "verified_observations"}
	if !validScopeID(scope) || q.Limit < 0 {
		return p, fmt.Errorf("%w: invalid reflection query", ErrInvalidExperienceReflection)
	}
	if q.Limit == 0 {
		q.Limit = 24
	}
	if q.Limit == int(^uint(0)>>1) {
		return p, fmt.Errorf("%w: reflection page size exceeds integer range", ErrInvalidExperienceReflection)
	}
	p.EffectiveLimit = q.Limit
	epoch, err := s.DataEpoch(ctx)
	if err != nil {
		return p, err
	}
	key, _ := json.Marshal([]string{scope, epoch, "experience_reflections_v1"})
	hash := sha256.Sum256(key)
	offset := 0
	if q.Cursor != "" {
		var cursor struct {
			Hash   string
			Offset int
		}
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(q.Cursor)
		if decodeErr != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.Hash != hex.EncodeToString(hash[:]) || cursor.Offset < 0 {
			return p, ErrMemoryConflict
		}
		offset = cursor.Offset
	}
	nextOffset := offset
	for {
		// Do not allocate from client-controlled Limit. The connection is single
		// threaded, so close this one-row cursor before source verification.
		var raw []byte
		var revision int
		var status string
		err = s.db.QueryRowContext(ctx, "SELECT payload,revision,status FROM experience_reflections WHERE scope=? AND status='active' ORDER BY updated DESC,id DESC LIMIT 1 OFFSET ?", scope, nextOffset).Scan(&raw, &revision, &status)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return p, err
		}
		nextOffset++
		var item ExperienceReflection
		if err = json.Unmarshal(raw, &item); err != nil {
			return p, err
		}
		item.Revision, item.Status = revision, status
		hydrated, visibleErr := s.hydrateExperienceReflection(ctx, item)
		if errors.Is(visibleErr, ErrMemoryConflict) || errors.Is(visibleErr, sql.ErrNoRows) {
			continue
		}
		if visibleErr != nil {
			return p, visibleErr
		}
		if len(p.Items) == q.Limit {
			p.HasMore = true
			nextOffset-- // continue from this not-yet-returned verified row.
			break
		}
		p.Items = append(p.Items, hydrated)
	}
	if after, afterErr := s.DataEpoch(ctx); afterErr != nil {
		return p, afterErr
	} else if after != epoch {
		return p, ErrMemoryConflict
	}
	if p.HasMore {
		body, _ := json.Marshal(struct {
			Hash   string
			Offset int
		}{hex.EncodeToString(hash[:]), nextOffset})
		p.NextCursor = base64.RawURLEncoding.EncodeToString(body)
	}
	return p, nil
}

func (s *Store) observedExperienceReflectionCandidate(ctx context.Context, scope, eventID, assetID string) (ObservedExperienceReflectionCandidate, error) {
	return observedExperienceReflectionCandidateFromRow(ctx, s.db.QueryRowContext, scope, eventID, assetID)
}

func observedExperienceReflectionCandidateFromRow(ctx context.Context, queryRow func(context.Context, string, ...any) *sql.Row, scope, eventID, assetID string) (ObservedExperienceReflectionCandidate, error) {
	var ev MemoryEvent
	var payload []byte
	err := queryRow(ctx, `SELECT id,scope,trace,role,sender,at,text,hash,complete,revision,seq,payload FROM events
		WHERE id=? AND scope=? AND suppressed=0
		AND NOT EXISTS(SELECT 1 FROM source_conflicts sc WHERE sc.event_id=events.id)
		AND NOT EXISTS(SELECT 1 FROM memory_blocks b WHERE b.scope=events.scope AND b.event_id=events.id)`, eventID, scope).Scan(&ev.ID, &ev.Scope, &ev.Trace, &ev.Role, &ev.Sender, &ev.At, &ev.Text, &ev.Hash, &ev.Complete, &ev.Revision, &ev.Seq, &payload)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ObservedExperienceReflectionCandidate{}, ErrMemoryConflict
		}
		return ObservedExperienceReflectionCandidate{}, err
	}
	enrichMemoryEvent(&ev, payload)
	for index, media := range ev.IncomingMedia {
		if reflectionMediaIdentity(ev, index) != assetID {
			continue
		}
		if candidate, ok := observedExperienceReflectionCandidateWithIdentity(ev, media, assetID); ok {
			return candidate, nil
		}
	}
	return ObservedExperienceReflectionCandidate{}, ErrMemoryConflict
}

func memoryCitesEvent(parent MemoryEntry, eventID string) bool {
	for _, source := range parent.Sources {
		if source.EventID == eventID {
			return true
		}
	}
	return false
}

func (s *Store) readExperienceReflection(ctx context.Context, scope, id string) (ExperienceReflection, error) {
	var out ExperienceReflection
	var raw []byte
	var revision int
	var status string
	if err := s.db.QueryRowContext(ctx, "SELECT payload,revision,status FROM experience_reflections WHERE id=? AND scope=?", id, scope).Scan(&raw, &revision, &status); err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	// Table metadata is the authority when an interrupted old payload exists.
	out.Revision, out.Status = revision, status
	return out, nil
}

func (s *Store) hydrateExperienceReflection(ctx context.Context, in ExperienceReflection) (ExperienceReflection, error) {
	candidate, err := s.observedExperienceReflectionCandidate(ctx, in.Scope, in.SourceEventID, in.SourceAssetID)
	if err != nil {
		return in, err
	}
	if candidate.SourceEventRevision != in.SourceEventRevision || candidate.SourceObservationVersion != in.SourceObservationVersion || candidate.SourceObservationHash != in.SourceObservationHash {
		return in, ErrMemoryConflict
	}
	ev, err := s.ReadEvent(ctx, []string{in.Scope}, in.SourceEventID)
	if err != nil {
		return in, err
	}
	in.Source = &ExperienceReflectionSource{EventID: ev.ID, AssetID: candidate.SourceAssetID, Sender: ev.Sender, SenderName: ev.SenderName, At: ev.At, Revision: candidate.SourceEventRevision, ObservationVersion: candidate.SourceObservationVersion, ObservationHash: candidate.SourceObservationHash, ObservationExcerpt: candidate.Summary, ObservedRange: candidate.ObservedRange, MediaKind: candidate.MediaKind}
	if in.ParentExperienceID != "" {
		parent, readErr := s.ReadMemory(ctx, []string{in.Scope}, in.ParentExperienceID)
		if readErr != nil || parent.Kind != "episode" || parent.Status != "active" || parent.Version != in.ParentExperienceVersion || !memoryCitesEvent(parent, in.SourceEventID) {
			return in, ErrMemoryConflict
		}
		in.ParentExperience = &ExperienceReflectionParent{ID: parent.ID, Version: parent.Version, Detail: boundedText(parent.Detail, 600)}
	}
	return in, nil
}
