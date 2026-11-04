package companion

import (
	"errors"
	"reflect"
	"time"
)

type MediaMemory struct {
	VoiceID      string `json:"voice_id,omitempty"`
	VoiceEngine  string `json:"voice_engine,omitempty"`
	VoiceProfile string `json:"voice_profile_id,omitempty"`
	// SynthesisText records the verified input to a successful speech job. It
	// is empty/unknown for media not synthesized from text and is never inferred
	// from a caption, title, or assistant response.
	SynthesisText      string `json:"synthesis_text,omitempty"`
	SynthesisTextKnown bool   `json:"synthesis_text_known,omitempty"`
	ParentAssetID      string `json:"parent_asset_id,omitempty"`
	RequestTraceID     string `json:"request_trace_id,omitempty"`
	RequesterID        string `json:"requester_id,omitempty"`
	RequesterName      string `json:"requester_name,omitempty"`
	TaskID             string `json:"task_id,omitempty"`
	CachedMIME         string `json:"cached_mime,omitempty"`
	URL                string `json:"url,omitempty"`
	Key                string `json:"key"`
	Kind               string `json:"kind"`
	Title              string `json:"title"`
	SourceID           string `json:"source_id,omitempty"`
	AssetID            string `json:"asset_id,omitempty"`
	JobID              string `json:"job_id,omitempty"`
	State              string `json:"state"`
	Phase              string `json:"phase,omitempty"`
	Reason             string `json:"reason,omitempty"`
	DurationMS         int64  `json:"duration_ms,omitempty"`
	AcceptedParts      int    `json:"accepted_parts,omitempty"`
	TotalParts         int    `json:"total_parts,omitempty"`
	Retries            int    `json:"retries,omitempty"`
}

func (s *Store) ObserveMedia(input Input, c Config, media MediaMemory, now time.Time) error {
	if input.Preview || !c.Permissions.Remember {
		return nil
	}
	if media.Key == "" || len(media.Key) > 256 {
		return nil
	}
	media.Title = boundedText(media.Title, 256)
	media.SynthesisText = boundedText(media.SynthesisText, 4096)
	media.RequestTraceID = boundedText(input.OriginTraceID, 256)
	if media.RequestTraceID == "" {
		media.RequestTraceID = boundedText(input.TraceID, 256)
	}
	media.RequesterID = boundedText(input.Sender, 256)
	media.RequesterName = boundedText(input.SenderName, 128)
	media.TaskID = boundedText(input.TaskID, 256)
	return s.scopeTransaction(ScopeID(input.Channel, input.Account, input.Thread), func(data *persisted) error {
		id := ScopeID(input.Channel, input.Account, input.Thread)
		scope, exists := data.Scopes[id]
		if !exists {
			return nil
		}
		if media.State == "accepted" {
			recordAutonomyAccepted(&scope, input, c, now)
		}
		for i := range scope.Messages {
			if old := scope.Messages[i].Media; old != nil && old.Key == media.Key {
				// A status update may lack old request metadata. Never erase a
				// known association merely because this is a legacy recovery.
				if media.RequesterID == "" {
					media.RequesterID = old.RequesterID
				}
				if media.RequesterName == "" {
					media.RequesterName = old.RequesterName
				}
				if media.RequestTraceID == "" {
					media.RequestTraceID = old.RequestTraceID
				}
				if media.TaskID == "" {
					media.TaskID = old.TaskID
				}
				if media.CachedMIME == "" {
					media.CachedMIME = old.CachedMIME
				}
				if media.ParentAssetID == "" {
					media.ParentAssetID = old.ParentAssetID
				}
				if media.VoiceID == "" {
					media.VoiceID = old.VoiceID
					media.VoiceEngine = old.VoiceEngine
				}
				if media.VoiceProfile == "" {
					media.VoiceProfile = old.VoiceProfile
				}
				if !media.SynthesisTextKnown && old.SynthesisTextKnown {
					media.SynthesisText = old.SynthesisText
					media.SynthesisTextKnown = true
				}
				if reflect.DeepEqual(*old, media) {
					return nil
				}
				copy := media
				scope.Messages[i].Media = &copy
				data.Scopes[id] = scope
				return nil
			}
		}
		copy := media
		scope.Messages = append(scope.Messages, Turn{Role: "assistant", Text: "[媒体附件] " + media.Title, TraceID: input.TraceID, SenderName: c.Name, SenderNameSource: "character", Time: now, Media: &copy})
		clean, err := s.pruneStored(scope, c, now)
		if err != nil {
			return err
		}
		data.Scopes[id] = clean
		return nil
	})
}

// BackfillMediaSources only joins exact scoped asset IDs. It never rewrites
// text, timestamps, receipt states, or assigns a source from another thread.
func (s *Store) BackfillMediaSources(sources map[string]map[string]string) error {
	metadata := map[string]map[string]MediaMemory{}
	for scope, assets := range sources {
		metadata[scope] = map[string]MediaMemory{}
		for id, raw := range assets {
			metadata[scope][id] = MediaMemory{URL: raw}
		}
	}
	return s.BackfillMediaMetadata(metadata)
}

// BackfillMediaMetadata joins exact scoped IDs only. Unknown historical
// requesters and missing original videos remain unknown rather than guessed.
func (s *Store) BackfillMediaMetadata(sources map[string]map[string]MediaMemory) error {
	// Starting a service with unchanged assets must preserve pagination cursors.
	// A callback that returns before commit leaves the database epoch intact.
	noUpdates := errors.New("media metadata already current")
	err := s.transaction(func(data *persisted) error {
		updated := false
		for id, scope := range data.Scopes {
			changed := false
			for i := range scope.Messages {
				m := scope.Messages[i].Media
				if m != nil {
					metadata := sources[id][m.AssetID]
					if m.URL == "" && metadata.URL != "" {
						m.URL = metadata.URL
						changed = true
					}
					if m.CachedMIME == "" && metadata.CachedMIME != "" {
						m.CachedMIME = metadata.CachedMIME
						changed = true
					}
					if m.ParentAssetID == "" && metadata.ParentAssetID != "" {
						m.ParentAssetID = metadata.ParentAssetID
						changed = true
					}
					// A script changes what a recipient can reasonably infer from an
					// old voice note, so only join it when the existing delivery has
					// the same retained, visible source request. In particular, never
					// revive a forgotten request merely because an asset ID survives.
					if !m.SynthesisTextKnown && metadata.SynthesisTextKnown && metadata.SynthesisText != "" && m.RequestTraceID != "" && m.RequesterID != "" && m.RequestTraceID == metadata.RequestTraceID && m.RequesterID == metadata.RequesterID && s.mediaBackfillSourceVisible(id, scope, m.RequestTraceID, m.RequesterID) {
						m.SynthesisText = boundedText(metadata.SynthesisText, 4096)
						m.SynthesisTextKnown = true
						changed = true
					}
				}
			}
			if changed {
				updated = true
				data.Scopes[id] = scope
			}
		}
		if !updated {
			return noUpdates
		}
		return nil
	})
	if err == noUpdates {
		return nil
	}
	return err
}

// mediaBackfillSourceVisible protects a provenance upgrade from bypassing a
// forgotten, suppressed, or conflicted user source. Backfill runs during
// startup, but source visibility is still checked against the durable index.
func (s *Store) mediaBackfillSourceVisible(scopeID string, scope Scope, trace, requester string) bool {
	if s.db == nil || trace == "" || requester == "" {
		return false
	}
	for _, turn := range scope.Messages {
		if turn.Role != "user" || turn.TraceID != trace || turn.Sender != requester || turn.Source == nil || turn.EventID == "" || turn.Source.Integrity == "conflicted" {
			continue
		}
		var visible bool
		err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM events e WHERE e.id=? AND e.scope=? AND e.suppressed=0 AND NOT EXISTS(SELECT 1 FROM memory_blocks b WHERE b.scope=e.scope AND b.event_id=e.id) AND NOT EXISTS(SELECT 1 FROM source_conflicts c WHERE c.event_id=e.id))`, turn.EventID, scopeID).Scan(&visible)
		if err == nil && visible {
			return true
		}
	}
	return false
}
