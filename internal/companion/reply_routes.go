package companion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// A reply route is minted by the host from real authorized message evidence.
// Claim evidence remains separate: an execution result cannot become a route.
type replyRoute struct {
	ID              string       `json:"route_id"`
	Kind            string       `json:"kind"`
	TargetIDs       []string     `json:"target_ids"`
	AnchorMessageID string       `json:"anchor_message_id"`
	QuoteRequired   bool         `json:"quote_required"`
	Proof           ReplyContext `json:"-"`
}

type editedReplyBubble struct {
	Text    string `json:"text"`
	RouteID string `json:"route_id"`
}

type routeEditedReply struct {
	Action  string              `json:"action"`
	Bubbles []editedReplyBubble `json:"bubbles"`
	Private string              `json:"private_report"`
}

func (e *Engine) replyRoutes(ctx context.Context, in Input, d Decision) ([]replyRoute, error) {
	routes := []replyRoute{}
	seen := map[string]bool{}
	add := func(proof ReplyContext) error {
		if err := e.ValidateReplyContext(ctx, in, proof); err != nil {
			return err
		}
		if seen[proof.ContextVersion] {
			return nil
		}
		seen[proof.ContextVersion] = true
		kind := "member_reply"
		targets := append([]string{}, proof.TargetIDs...)
		if in.SourceOnlyContext || in.ConversationDelivery {
			kind, targets = "conversation_delivery", []string{}
		}
		routes = append(routes, replyRoute{ID: fmt.Sprintf("R%d", len(routes)+1), Kind: kind, TargetIDs: targets, AnchorMessageID: proof.AnchorMessageID, QuoteRequired: proof.QuoteRequired, Proof: *cloneReplyContext(&proof)})
		return nil
	}
	for _, proof := range d.ReplyContexts {
		if err := add(proof); err != nil {
			return nil, err
		}
	}
	// Cross-scope delivery keeps its real origin authorization separate from
	// the destination audience. Never offer the origin requester as a new
	// member of an unrelated destination conversation.
	if in.SourceOnlyContext || in.ConversationDelivery {
		return routes, nil
	}
	ids := []string{in.Sender}
	ids = append(ids, in.Addressees...)
	if in.QuotedSource != nil {
		ids = append(ids, in.QuotedSource.Sender)
	}
	for _, target := range uniqueSortedEvidence(ids) {
		base := ReplyContext{TargetIDs: []string{target}}
		if e.Store != nil {
			if source, ok := e.Store.SourceForInput(in); ok && source.EventID != "" {
				base.SourceEventIDs = append(base.SourceEventIDs, source.EventID)
			}
		}
		if in.QuotedSource != nil && target == in.QuotedSource.Sender {
			if quoted, ok := e.replyReferenceEvent(ctx, in, in.QuotedSource); ok {
				base.SourceEventIDs = append(base.SourceEventIDs, quoted.ID)
			}
		}
		candidate := d
		candidate.Messages = []string{""}
		candidate.ReplyContexts = []ReplyContext{base}
		resolved, err := e.normalizeReplyContexts(ctx, in, candidate)
		if err != nil {
			continue
		} // No verified connected anchor: no route.
		resolved, err = e.bindReplyEvidence(ctx, in, resolved)
		if err != nil {
			return nil, err
		}
		if err = add(resolved.ReplyContexts[0]); err != nil {
			return nil, err
		}
	}
	return routes, nil
}

func decodeRouteEditedReply(raw string) (routeEditedReply, error) {
	var out routeEditedReply
	if len(raw) > 65536 {
		return out, errors.New("reply_grounding_repair_invalid")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil {
		return out, errors.New("reply_grounding_repair_invalid")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || out.Bubbles == nil || out.Action != "reply" && out.Action != "silent" {
		return routeEditedReply{}, errors.New("reply_grounding_repair_invalid")
	}
	return out, nil
}

func (e *Engine) applyReplyRoutes(ctx context.Context, in Input, d Decision, bubbles []editedReplyBubble, routes []replyRoute) (Decision, error) {
	byID := map[string]ReplyContext{}
	for _, route := range routes {
		if _, exists := byID[route.ID]; exists {
			return Decision{}, errors.New("reply_route_duplicate")
		}
		byID[route.ID] = route.Proof
	}
	out := d
	out.Messages, out.ReplyContexts = []string{}, []ReplyContext{}
	for _, bubble := range bubbles {
		if strings.TrimSpace(bubble.Text) == "" {
			return Decision{}, errors.New("reply_grounding_repair_empty_message")
		}
		proof, ok := byID[bubble.RouteID]
		if !ok {
			return Decision{}, errors.New("reply_route_unknown")
		}
		if err := e.ValidateReplyText(ctx, in, proof, bubble.Text); err != nil {
			return Decision{}, err
		}
		out.Messages = append(out.Messages, bubble.Text)
		out.ReplyContexts = append(out.ReplyContexts, *cloneReplyContext(&proof))
	}
	return out, nil
}

func (e *Engine) validateReplyTexts(ctx context.Context, in Input, d Decision) error {
	if len(d.Messages) != len(d.ReplyContexts) {
		return errors.New("reply_context_count_mismatch")
	}
	for i, text := range d.Messages {
		if err := e.ValidateReplyText(ctx, in, d.ReplyContexts[i], text); err != nil {
			return err
		}
	}
	return nil
}

func routeProofs(routes []replyRoute) map[string]ReplyContext {
	proofs := map[string]ReplyContext{}
	for _, route := range routes {
		proofs[route.ID] = *cloneReplyContext(&route.Proof)
	}
	return proofs
}

func addReplyRouteState(state map[string]any, routes []replyRoute) {
	state["reply_routes"] = routes
	ids := []string{}
	switch current := state["evidence_event_ids"].(type) {
	case []string:
		ids = append(ids, current...)
	case []any:
		for _, item := range current {
			if id, ok := item.(string); ok {
				ids = append(ids, id)
			}
		}
	}
	for _, route := range routes {
		ids = append(ids, route.Proof.SourceEventIDs...)
	}
	state["evidence_event_ids"] = uniqueSortedEvidence(ids)
}

func replyAudiencesForInput(in Input, d Decision) []map[string]any {
	if !in.ConversationDelivery && !in.SourceOnlyContext {
		rows := replyAudiences(d)
		for _, row := range rows {
			row["kind"] = "member_reply"
		}
		return rows
	}
	rows := []map[string]any{}
	for i := range d.ReplyContexts {
		rows = append(rows, map[string]any{"message_index": i, "kind": "conversation_delivery", "request_actor_id": in.Sender, "target_ids": []string{}, "outgoing_you_means": nil, "audience": "authorized_destination_conversation", "interpretation": "Origin request actor and its source authorization do not identify a member being addressed in the destination. Use the authorized task's actual audience; do not infer one from origin source IDs."})
	}
	return rows
}
