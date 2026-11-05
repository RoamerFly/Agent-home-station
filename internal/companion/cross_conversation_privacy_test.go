package companion

import (
	"context"
	"testing"
)

type unavailablePrivateRoute struct {
	privacyTools
	submitted int
}

func (r *unavailablePrivateRoute) PrivateReplyTarget() string { return "" }
func (r *unavailablePrivateRoute) PrivateReplyRequired() bool { return true }
func (r *unavailablePrivateRoute) RoutePrivateReply(context.Context, []string) error {
	r.submitted++
	return nil
}

func TestCrossConversationUnavailablePrivateRouteNeverFallsBackToGroup(t *testing.T) {
	route := &unavailablePrivateRoute{}
	e := &Engine{Tools: route}
	d, err := e.routeCrossConversationReply(t.Context(), Input{IsGroup: true}, Decision{Action: "reply", Messages: []string{"跨会话资料"}})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != "silent" || len(d.Messages) != 0 || d.Reason != "cross_conversation_private_route_unavailable" || route.submitted != 0 {
		t.Fatalf("unexpected fallback: decision=%#v submissions=%d", d, route.submitted)
	}
}

func TestCrossConversationMediaOnlyNeverFallsBackToSourceGroup(t *testing.T) {
	// Give the router a nominal target: media must still be stopped because the
	// text-only private route has no explicit media delivery authorization.
	routeTarget := &availablePrivateRoute{privacyTools: privacyTools{}}
	e := &Engine{Tools: routeTarget}
	d, err := e.routeCrossConversationReply(t.Context(), Input{IsGroup: true}, Decision{Action: "reply", ImageID: "cross-image", MediaIDs: []string{"cross-file"}})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != "silent" || d.ImageID != "" || len(d.MediaIDs) != 0 || d.Reason != "cross_conversation_media_requires_explicit_delivery" || routeTarget.submitted != 0 {
		t.Fatalf("media escaped source group: decision=%#v submissions=%d", d, routeTarget.submitted)
	}
}

type availablePrivateRoute struct {
	privacyTools
	submitted int
}

func (*availablePrivateRoute) PrivateReplyTarget() string { return "owner" }
func (*availablePrivateRoute) PrivateReplyRequired() bool { return true }
func (r *availablePrivateRoute) RoutePrivateReply(context.Context, []string) error {
	r.submitted++
	return nil
}
