package companion

import (
	"reflect"
	"time"
)

type MediaMemory struct {
	URL           string `json:"url,omitempty"`
	Key           string `json:"key"`
	Kind          string `json:"kind"`
	Title         string `json:"title"`
	SourceID      string `json:"source_id,omitempty"`
	AssetID       string `json:"asset_id,omitempty"`
	JobID         string `json:"job_id,omitempty"`
	State         string `json:"state"`
	Phase         string `json:"phase,omitempty"`
	Reason        string `json:"reason,omitempty"`
	DurationMS    int64  `json:"duration_ms,omitempty"`
	AcceptedParts int    `json:"accepted_parts,omitempty"`
	TotalParts    int    `json:"total_parts,omitempty"`
	Retries       int    `json:"retries,omitempty"`
}

func (s *Store) ObserveMedia(input Input, c Config, media MediaMemory, now time.Time) error {
	if input.Preview || !c.Permissions.Remember {
		return nil
	}
	if media.Key == "" || len(media.Key) > 256 {
		return nil
	}
	media.Title = boundedText(media.Title, 256)
	return s.transaction(func(data *persisted) error {
		id := ScopeID(input.Channel, input.Account, input.Thread)
		scope, exists := data.Scopes[id]
		if !exists {
			return nil
		}
		for i := range scope.Messages {
			if old := scope.Messages[i].Media; old != nil && old.Key == media.Key {
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
		data.Scopes[id] = prune(scope, c, now)
		return nil
	})
}

// BackfillMediaSources only joins exact scoped asset IDs. It never rewrites
// text, timestamps, receipt states, or assigns a source from another thread.
func (s *Store) BackfillMediaSources(sources map[string]map[string]string) error {
	return s.transaction(func(data *persisted) error {
		for id, scope := range data.Scopes {
			changed := false
			for i := range scope.Messages {
				m := scope.Messages[i].Media
				if m != nil && m.URL == "" {
					if u := sources[id][m.AssetID]; u != "" {
						m.URL = u
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
