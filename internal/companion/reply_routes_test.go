package companion

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReplyRoutesCopyVerifiedProofAndNeverSelectLatestUnrelatedAnchor(t *testing.T) {
	e, in, d := namedReplyFixture(t)
	in.Addressees = []string{"beta"}
	routes, err := e.replyRoutes(t.Context(), in, d)
	if err != nil || len(routes) < 2 {
		t.Fatal(routes, err)
	}
	var betaRoute replyRoute
	for _, route := range routes {
		if len(route.Proof.TargetIDs) == 1 && route.Proof.TargetIDs[0] == "beta" {
			betaRoute = route
		}
	}
	if betaRoute.ID == "" || !betaRoute.Proof.QuoteRequired || betaRoute.Proof.AnchorMessageID != in.TraceID {
		t.Fatal("explicit addressee route lacks original linked anchor", betaRoute)
	}
	groundedInput(t, e.Store, e.Config, "beta", "later-unrelated", "另外一件事。")
	out, err := e.applyReplyRoutes(t.Context(), in, d, []editedReplyBubble{{Text: "小周，收到。", RouteID: betaRoute.ID}}, routes)
	if err != nil || out.ReplyContexts[0].AnchorMessageID != in.TraceID || out.ReplyContexts[0].ContextVersion != betaRoute.Proof.ContextVersion {
		t.Fatal("route proof was refreshed or redirected", out, err)
	}
	before, _ := json.Marshal(routes)
	if _, err = e.applyReplyRoutes(t.Context(), in, d, []editedReplyBubble{{Text: "收到。", RouteID: "R999"}}, routes); err == nil {
		t.Fatal("unknown route accepted")
	}
	if err = e.Store.Clear(ScopeID(in.Channel, in.Account, in.Thread)); err != nil {
		t.Fatal(err)
	}
	if _, err = e.applyReplyRoutes(t.Context(), in, d, []editedReplyBubble{{Text: "收到。", RouteID: betaRoute.ID}}, routes); err == nil {
		t.Fatal("forgotten route was re-signed")
	}
	after, _ := json.Marshal(routes)
	if string(before) != string(after) {
		t.Fatal("persisted route changed during validation")
	}
}

func TestRouteEditorCannotSupplyFactIDsOrProofMetadata(t *testing.T) {
	for _, raw := range []string{
		`{"action":"reply","bubbles":[{"text":"收到。","route_id":"R1","source_event_ids":["S1"]}]}`,
		`{"action":"reply","bubbles":[{"text":"收到。","route_id":"R1"}],"reply_contexts":[]}`,
		`{"action":"reply","messages":["收到。"]}`,
	} {
		if _, err := decodeRouteEditedReply(raw); err == nil {
			t.Fatal("editor could overwrite host proof", raw)
		}
	}
	e, in, d := namedReplyFixture(t)
	routes, err := e.replyRoutes(t.Context(), in, d)
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(routes)
	for _, forbidden := range []string{"source_event_ids", "source_versions", "evidence_epoch", "context_version"} {
		if strings.Contains(string(wire), forbidden) {
			t.Fatal("fact/proof selection leaked into public routing interface", string(wire))
		}
	}
	for _, ref := range []string{"S1", "K1", "execution:one"} {
		if _, err := e.applyReplyRoutes(t.Context(), in, d, []editedReplyBubble{{Text: "收到。", RouteID: ref}}, routes); err == nil {
			t.Fatal("fact evidence became a destination route", ref)
		}
	}
}

func TestReplyRouteCannotRenewRevisionOrAuthorization(t *testing.T) {
	for _, kind := range []string{"revision", "authorization"} {
		t.Run(kind, func(t *testing.T) {
			e, in, _ := namedReplyFixture(t)
			access := &memoryAuthorizationFixture{stamp: "route-grant"}
			e.Tools = access
			d, err := e.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{"收到。"}})
			if err != nil {
				t.Fatal(err)
			}
			d, err = e.bindReplyEvidence(t.Context(), in, d)
			if err != nil {
				t.Fatal(err)
			}
			routes, err := e.replyRoutes(t.Context(), in, d)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "authorization" {
				access.stamp = "withdrawn"
			} else {
				if _, err = e.Store.db.Exec("UPDATE events SET revision=revision+1 WHERE id=?", routes[0].Proof.SourceEventIDs[0]); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = e.applyReplyRoutes(t.Context(), in, d, []editedReplyBubble{{Text: "收到。", RouteID: routes[0].ID}}, routes); err == nil {
				t.Fatal("old route was renewed", kind)
			}
		})
	}
}
