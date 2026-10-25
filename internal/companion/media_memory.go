package companion

import (
	"reflect"
	"time"
)

type MediaMemory struct {
	ParentAssetID  string `json:"parent_asset_id,omitempty"`
	RequestTraceID string `json:"request_trace_id,omitempty"`
	RequesterID    string `json:"requester_id,omitempty"`
	RequesterName  string `json:"requester_name,omitempty"`
	TaskID         string `json:"task_id,omitempty"`
	CachedMIME     string `json:"cached_mime,omitempty"`
	URL            string `json:"url,omitempty"`
	Key            string `json:"key"`
	Kind           string `json:"kind"`
	Title          string `json:"title"`
	SourceID       string `json:"source_id,omitempty"`
	AssetID        string `json:"asset_id,omitempty"`
	JobID          string `json:"job_id,omitempty"`
	State          string `json:"state"`
	Phase          string `json:"phase,omitempty"`
	Reason         string `json:"reason,omitempty"`
	DurationMS     int64  `json:"duration_ms,omitempty"`
	AcceptedParts  int    `json:"accepted_parts,omitempty"`
	TotalParts     int    `json:"total_parts,omitempty"`
	Retries        int    `json:"retries,omitempty"`
}

func (s *Store) ObserveMedia(input Input, c Config, media MediaMemory, now time.Time) error {
	if input.Preview || !c.Permissions.Remember {
		return nil
	}
	if media.Key == "" || len(media.Key) > 256 {
		return nil
	}
	media.Title = boundedText(media.Title, 256)
	media.RequestTraceID = boundedText(input.OriginTraceID, 256)
	if media.RequestTraceID == "" {
		media.RequestTraceID = boundedText(input.TraceID, 256)
	}
	media.RequesterID = boundedText(input.Sender, 256)
	media.RequesterName = boundedText(input.SenderName, 128)
	media.TaskID = boundedText(input.TaskID, 256)
	return s.transaction(func(data *persisted) error {
		id := ScopeID(input.Channel, input.Account, input.Thread)
		scope, exists := data.Scopes[id]
		if !exists {
			return nil
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
	return s.transaction(func(data *persisted) error {
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
				}
			}
			if changed {
				data.Scopes[id] = scope
			}
		}
		return nil
	})
}
