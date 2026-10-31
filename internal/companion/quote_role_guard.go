package companion

import (
	"context"
	"sort"
	"strings"
)

type quoteRoleEdge struct {
	QuotedByID     string `json:"quoted_by_id"`
	QuotingEventID string `json:"quoting_event_id"`
	QuotedAuthorID string `json:"quoted_author_id"`
	QuotedEventID  string `json:"quoted_event_id"`
}
type quoteRoleActor struct {
	ID                       string   `json:"id"`
	Labels                   []string `json:"labels"`
	HasQuoteEdge             bool     `json:"has_quote_edge"`
	HasLiteralQuoteStatement bool     `json:"has_literal_quote_statement"`
}
type quoteRoleEvidence struct {
	Edges  []quoteRoleEdge  `json:"edges"`
	Actors []quoteRoleActor `json:"actors"`
}

// Quote actions have a direction: the author of an original does not become
// the person who quoted it. Only connected source references enter this graph;
// a participant's newest unrelated text cannot supply a missing quote edge.
func (e *Engine) quoteRoleEvidence(ctx context.Context, in Input, d Decision) quoteRoleEvidence {
	graph := quoteRoleEvidence{Edges: []quoteRoleEdge{}, Actors: []quoteRoleActor{}}
	if in.QuotedSource == nil || e.Store == nil || e.Store.db == nil {
		return graph
	}
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	queue := []MemoryEvent{}
	for _, r := range d.ReplyContexts {
		for _, id := range r.SourceEventIDs {
			if ev, err := e.Store.ReadEvent(ctx, []string{scope}, id); err == nil {
				queue = append(queue, ev)
			}
		}
	}
	if ref, ok := e.Store.SourceForInput(in); ok {
		if ev, err := e.Store.ReadEvent(ctx, []string{scope}, ref.EventID); err == nil {
			queue = append(queue, ev)
		}
	}
	actors := map[string]*quoteRoleActor{}
	seen := map[string]bool{}
	words := map[string]string{}
	addActor := func(id, name string) {
		if id == "" {
			return
		}
		if actors[id] == nil {
			actors[id] = &quoteRoleActor{ID: id, Labels: []string{id}}
		}
		if name != "" && name != id {
			for _, label := range actors[id].Labels {
				if label == name {
					return
				}
			}
			actors[id].Labels = append(actors[id].Labels, name)
		}
	}
	for len(queue) > 0 {
		ev := queue[0]
		queue = queue[1:]
		if seen[ev.ID] {
			continue
		}
		seen[ev.ID] = true
		addActor(ev.Sender, ev.SenderName)
		literal, state := literalTurn(eventEvidenceTurn(ev))
		if state != "unknown" {
			words[ev.ID] = literal
			if ev.Sender != "" && unquotedPossessive(literal, "我", "引用") {
				actors[ev.Sender].HasLiteralQuoteStatement = true
			}
		}
		ref := ev.QuotedSource
		if ref == nil && in.Source != nil && (ev.ID == in.Source.EventID || ev.Trace == in.Source.MessageID) {
			ref = in.QuotedSource
		}
		if quoted, ok := e.replyReferenceEvent(ctx, in, ref); ok {
			addActor(quoted.Sender, quoted.SenderName)
			if ev.Sender != "" {
				actors[ev.Sender].HasQuoteEdge = true
			}
			graph.Edges = append(graph.Edges, quoteRoleEdge{ev.Sender, ev.ID, quoted.Sender, quoted.ID})
			queue = append(queue, quoted)
		}
	}
	addActor(in.Sender, in.SenderName)
	if in.QuotedSource != nil {
		addActor(in.QuotedSource.Sender, in.QuotedSource.SenderName)
	}
	owners := map[string]int{}
	for _, actor := range actors {
		for _, label := range actor.Labels {
			owners[label]++
		}
	}
	for _, actor := range actors {
		labels := []string{}
		for _, label := range actor.Labels {
			if owners[label] == 1 {
				labels = append(labels, label)
			}
		}
		sort.Strings(labels)
		actor.Labels = labels
		for _, literal := range words {
			for _, label := range labels {
				if unquotedPossessive(literal, label, "引用") {
					actor.HasLiteralQuoteStatement = true
				}
			}
		}
		graph.Actors = append(graph.Actors, *actor)
	}
	sort.Slice(graph.Actors, func(i, j int) bool { return graph.Actors[i].ID < graph.Actors[j].ID })
	sort.Slice(graph.Edges, func(i, j int) bool { return graph.Edges[i].QuotingEventID < graph.Edges[j].QuotingEventID })
	return graph
}

func contradictsQuoteRole(graph quoteRoleEvidence, messages []string) bool {
	if len(graph.Edges) == 0 {
		return false
	}
	for _, actor := range graph.Actors {
		if actor.HasQuoteEdge || actor.HasLiteralQuoteStatement {
			continue
		}
		for _, text := range messages {
			for _, label := range actor.Labels {
				if !unquotedPossessive(text, label, "引用") {
					continue
				}
				for _, phrase := range []string{label + "引用", label + "的引用"} {
					for offset := 0; offset < len(text); {
						at := strings.Index(text[offset:], phrase)
						if at < 0 {
							break
						}
						at += offset
						prefix := strings.TrimSpace(text[:at])
						negated := false
						for _, negative := range []string{"不是", "并非", "并不是", "非"} {
							negated = negated || strings.HasSuffix(prefix, negative)
						}
						if !negated {
							return true
						}
						offset = at + len(phrase)
					}
				}
			}
		}
	}
	return false
}
