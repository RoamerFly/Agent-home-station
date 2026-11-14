package station

import (
	"context"
	"testing"
)

func TestLocalDouyinWebInteractionPermissionsAreExplicitAndRevokeWithDependencies(t *testing.T) {
	f, m := newLocalDouyinWebFixture(t)
	initializeDouyinWeb(t, f)
	_, view := f.request("POST", localDouyinWebRoute, map[string]any{"action": "begin", "revision": 0, "approve_profile": true}, true)
	_, view = f.request("POST", localDouyinWebRoute, map[string]any{"action": "confirm", "revision": view["revision"], "attempt_id": view["attempt_id"]}, true)
	if view["read_enabled"] != true || view["comments_enabled"] != true || view["likes_enabled"] != false || view["replies_enabled"] != false {
		t.Fatalf("read-only permissions must default on while interactions stay off: %v", view)
	}
	code, view := f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_interactions", "revision": view["revision"], "allow_likes": true}, true)
	if code != 200 || view["likes_enabled"] != true {
		t.Fatalf("likes did not require an explicit opt-in: %d %v", code, view)
	}
	_, view = f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_interactions", "revision": view["revision"], "allow_replies": true}, true)
	if view["likes_enabled"] != true || view["replies_enabled"] != true {
		t.Fatalf("reply permission was not independently saved: %v", view)
	}
	_, view = f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_read", "revision": view["revision"], "allow_read": true, "allow_comments": false}, true)
	if view["comments_enabled"] != false || view["likes_enabled"] != true || view["replies_enabled"] != false {
		t.Fatalf("comment revocation must only revoke dependent replies: %v", view)
	}
	_, view = f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_read", "revision": view["revision"], "allow_read": true, "read_scope": "home"}, true)
	if view["likes_enabled"] != false || view["replies_enabled"] != false {
		t.Fatalf("scope reduction must revoke interactions: %v", view)
	}

	// A withdrawal must remain durable while the browser service is stopping.
	_, view = f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_read", "revision": view["revision"], "allow_read": true, "read_scope": "home_and_videos"}, true)
	_, view = f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_interactions", "revision": view["revision"], "allow_likes": true}, true)
	stopping, stop := context.WithCancel(context.Background())
	stop()
	m.ctx = stopping
	code, view = f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_interactions", "revision": view["revision"], "allow_likes": false}, true)
	if code != 200 || view["likes_enabled"] != false {
		t.Fatalf("offline withdrawal failed: %d %v", code, view)
	}
}
