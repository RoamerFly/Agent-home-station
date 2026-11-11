package station

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/hostfile"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/stationbrowser"
)

func surfFollowupFixture(t *testing.T) (*surfShareFixture, *localDouyinWebLogin, string, *localSurfActionWorker) {
	t.Helper()
	x, m, scope := surfAppFixture(t)
	h := x.f.handler.chat
	h.surfActions.cancel()
	<-h.surfActions.done
	w := newLocalSurfActionWorker(h)
	h.surfActions = w
	x.worker.mu.Lock()
	policy := x.worker.policy
	policy.Enabled = true
	policy.Paused = false
	err := x.worker.save(policy)
	x.worker.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	h.modelFactory = func() (companion.LanguageModel, error) {
		t.Error("automatic interaction invoked a model")
		return nil, errors.New("no model in interaction reconciliation")
	}
	return x, m, scope, w
}

func surfFollowupTask(t *testing.T, x *surfShareFixture, scope, trace string) string {
	t.Helper()
	tools, ctx := surfAppRun(t, x, trace)
	id := surfAppReadVideo(t, tools, ctx, scope)
	surfAppCall(t, tools, ctx, map[string]any{"action": "read", "scope_id": scope, "content_id": id, "field": "comments", "limit": 20})
	view := surfAppCall(t, tools, ctx, map[string]any{"action": "create", "scope_id": scope, "content_id": id, "kind": "reply_comment", "comment_id": "101", "text": "原任务的合成回复"})
	return view["job_id"].(string)
}

func surfFollowupState(t *testing.T, x *surfShareFixture) (*pluginhost.Store, pluginhost.Invocation, map[string]surfActionWait) {
	t.Helper()
	h := x.f.handler.chat
	_, actor, err := h.interactionHost()
	if err != nil {
		t.Fatal(err)
	}
	project, err := h.projectTools.forFish(actor.Account)
	if err != nil {
		t.Fatal(err)
	}
	_, rows, err := readSurfWaits(t.Context(), project.store, actor)
	if err != nil {
		t.Fatal(err)
	}
	return project.store, actor, rows
}

func clearSyntheticReplyWindow(t *testing.T, x *surfShareFixture) {
	t.Helper()
	p := &localDouyinWebInteraction{home: x.f.handler.home}
	a, err := p.access(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := hostfile.WritePrivate(filepath.Join(x.f.dir, "apps", "douyin", "reply-pacing", a.record.FishID, a.record.Profile+".json"), mustLocalDouyinJSON(douyinReplyPacing{Next: time.Now().Add(-time.Second).UTC()})); err != nil {
		t.Fatal(err)
	}
}

func TestSurfActionFollowupWaitsReopensAndResumesOriginal(t *testing.T) {
	x, m, scope, w := surfFollowupFixture(t)
	now := time.Now().UTC()
	job := surfFollowupTask(t, x, scope, "surf-deferred")
	var clicks atomic.Int32
	m.interact = func(ctx context.Context, _ string, in stationbrowser.DouyinInteractionRequest, authorize func() error) (stationbrowser.DouyinInteractionResult, error) {
		if in.Submitted {
			t.Error("unexpected receipt lookup before first submission")
		}
		if err := in.BeforeSubmit(ctx); err != nil {
			return stationbrowser.DouyinInteractionResult{}, err
		}
		if err := authorize(); err != nil {
			return stationbrowser.DouyinInteractionResult{}, err
		}
		clicks.Add(1)
		return stationbrowser.DouyinInteractionResult{State: "applied", RemoteID: "900001", Submitted: true, Code: "applied"}, nil
	}
	w.tick(t.Context(), now)
	_, _, rows := surfFollowupState(t, x)
	if len(rows) != 0 || clicks.Load() != 0 {
		t.Fatal("draft creation authorised a background submission", rows)
	}
	p := &localDouyinWebInteraction{home: x.f.handler.home}
	a, err := p.access(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	until, err := p.replyPacing(a, time.Now().UTC(), douyinReplyReserve)
	if err != nil {
		t.Fatal(err)
	}
	x.request("POST", "/api/local/interactions", map[string]string{"action": "submit", "job_id": job}, 200)
	w.notify()
	w.tick(t.Context(), time.Now().UTC())
	_, actor, rows := surfFollowupState(t, x)
	if len(rows) != 1 || !rows[job].Next.Equal(until) || clicks.Load() != 0 {
		t.Fatal("did not honor durable spacing", rows, until)
	}
	view := x.request("GET", x.path+"/surf", nil, 200)
	if view["interaction_continuations"].(map[string]any)["waiting"] != float64(1) {
		t.Fatal("owner cannot read continuation state", view)
	}
	if w.summary(surfWaitNamespace(pluginhost.Invocation{Account: actor.Account, Requester: "another-owner"}), x.id) != nil {
		t.Fatal("another owner read cached continuation metadata")
	}
	// Reopen the real application SQLite file and reconstruct the worker.
	h := x.f.handler.chat
	h.projectTools.mu.Lock()
	prior := h.projectTools.host
	if err := prior.controller.Close(); err != nil {
		t.Fatal(err)
	}
	if err := prior.store.Close(); err != nil {
		t.Fatal(err)
	}
	h.projectTools.host = nil
	h.projectTools.mu.Unlock()
	newWorker := newLocalSurfActionWorker(h)
	h.surfActions = newWorker
	newWorker.tick(t.Context(), until.Add(-time.Second))
	if clicks.Load() != 0 {
		t.Fatal("restart ignored persisted deadline")
	}
	clearSyntheticReplyWindow(t, x)
	newWorker.tick(t.Context(), until.Add(time.Second))
	_, _, rows = surfFollowupState(t, x)
	if clicks.Load() != 1 || len(rows) != 0 {
		t.Fatal("original deferred task did not settle", clicks.Load(), rows)
	}
	status := x.request("POST", "/api/local/interactions", map[string]string{"action": "status", "job_id": job}, 200)
	if status["state"] != "applied" || status["remote_id"] != "900001" || len(x.sends) != 0 {
		t.Fatal("resumed receipt or no WeChat sends", status)
	}
}

func TestSurfActionFollowupUnknownIsLookupOnlyAfterReconstruction(t *testing.T) {
	x, m, scope, w := surfFollowupFixture(t)
	job := surfFollowupTask(t, x, scope, "surf-unknown-auto")
	var clicks, queries atomic.Int32
	m.interact = func(ctx context.Context, _ string, in stationbrowser.DouyinInteractionRequest, authorize func() error) (stationbrowser.DouyinInteractionResult, error) {
		if in.Submitted {
			if in.BeforeSubmit != nil {
				t.Error("query has submit callback")
			}
			queries.Add(1)
			return stationbrowser.DouyinInteractionResult{State: "unknown", Submitted: true, Code: "receipt_pending"}, nil
		}
		if err := in.BeforeSubmit(ctx); err != nil {
			return stationbrowser.DouyinInteractionResult{}, err
		}
		if err := authorize(); err != nil {
			return stationbrowser.DouyinInteractionResult{}, err
		}
		clicks.Add(1)
		return stationbrowser.DouyinInteractionResult{State: "unknown", Submitted: true, Code: "receipt_pending"}, nil
	}
	x.request("POST", "/api/local/interactions", map[string]string{"action": "submit", "job_id": job}, 200)
	now := time.Now().UTC()
	w.tick(t.Context(), now)
	_, _, rows := surfFollowupState(t, x)
	if clicks.Load() != 1 || queries.Load() != 1 || !rows[job].Next.After(now) {
		t.Fatal("unknown task not reconciled safely", clicks.Load(), queries.Load(), rows)
	}
	w.tick(t.Context(), now.Add(time.Second))
	if queries.Load() != 1 {
		t.Fatal("unknown lookup ignored durable backoff")
	}
	newWorker := newLocalSurfActionWorker(x.f.handler.chat)
	newWorker.tick(t.Context(), rows[job].Next.Add(time.Second))
	if clicks.Load() != 1 || queries.Load() != 2 {
		t.Fatal("reconstruction repeated external effect", clicks.Load(), queries.Load())
	}
}

func TestSurfActionFollowupPauseAndRevokeBlockEffect(t *testing.T) {
	for _, mode := range []string{"parent_pause", "scope_revoke", "pause_at_submit"} {
		t.Run(mode, func(t *testing.T) {
			x, m, scope, w := surfFollowupFixture(t)
			job := surfFollowupTask(t, x, scope, "surf-stop-auto")
			p := &localDouyinWebInteraction{home: x.f.handler.home}
			a, err := p.access(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			until, err := p.replyPacing(a, time.Now().UTC(), douyinReplyReserve)
			if err != nil {
				t.Fatal(err)
			}
			x.request("POST", "/api/local/interactions", map[string]string{"action": "submit", "job_id": job}, 200)
			w.tick(t.Context(), time.Now().UTC())
			clearSyntheticReplyWindow(t, x)
			pause := func() {
				x.worker.mu.Lock()
				policy := x.worker.policy
				policy.Paused = true
				err := x.worker.save(policy)
				x.worker.mu.Unlock()
				if err != nil {
					t.Error(err)
				}
			}
			if mode == "parent_pause" {
				pause()
			}
			if mode == "scope_revoke" {
				x.request("PUT", x.path+"/surf-app-grants", map[string]any{"revision": 1, "key": "stop-scope", "scopes": []any{}}, 200)
			}
			var effects atomic.Int32
			m.interact = func(ctx context.Context, _ string, in stationbrowser.DouyinInteractionRequest, authorize func() error) (stationbrowser.DouyinInteractionResult, error) {
				if mode == "pause_at_submit" {
					pause()
				}
				if err := in.BeforeSubmit(ctx); err != nil {
					return stationbrowser.DouyinInteractionResult{}, err
				}
				if err := authorize(); err != nil {
					return stationbrowser.DouyinInteractionResult{}, err
				}
				effects.Add(1)
				return stationbrowser.DouyinInteractionResult{State: "applied", RemoteID: "900002", Submitted: true, Code: "applied"}, nil
			}
			w.tick(t.Context(), until.Add(time.Second))
			if effects.Load() != 0 {
				t.Fatal("pause or revocation raced automatic submit")
			}
			_, _, rows := surfFollowupState(t, x)
			if len(rows) != 1 {
				t.Fatal("blocked original task lost", rows)
			}
		})
	}
}

func TestSurfActionFollowupConcurrentLeaseAndCancellation(t *testing.T) {
	x, m, scope, w := surfFollowupFixture(t)
	job := surfFollowupTask(t, x, scope, "surf-concurrent")
	p := &localDouyinWebInteraction{home: x.f.handler.home}
	a, err := p.access(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	until, err := p.replyPacing(a, time.Now().UTC(), douyinReplyReserve)
	if err != nil {
		t.Fatal(err)
	}
	x.request("POST", "/api/local/interactions", map[string]string{"action": "submit", "job_id": job}, 200)
	w.tick(t.Context(), time.Now().UTC())
	store, actor, rows := surfFollowupState(t, x)
	clearSyntheticReplyWindow(t, x)
	entered, release := make(chan struct{}), make(chan struct{})
	var callbacks, effects atomic.Int32
	m.interact = func(ctx context.Context, _ string, in stationbrowser.DouyinInteractionRequest, authorize func() error) (stationbrowser.DouyinInteractionResult, error) {
		callbacks.Add(1)
		close(entered)
		<-release
		if err := in.BeforeSubmit(ctx); err != nil {
			return stationbrowser.DouyinInteractionResult{}, err
		}
		if err := authorize(); err != nil {
			return stationbrowser.DouyinInteractionResult{}, err
		}
		effects.Add(1)
		return stationbrowser.DouyinInteractionResult{State: "applied", RemoteID: "900003", Submitted: true, Code: "applied"}, nil
	}
	host, _, err := x.f.handler.chat.interactionHostWithGuard(x.f.handler.chat.surfContinuationActive)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { w.advance(t.Context(), store, host, actor, rows[job], until.Add(time.Second)); close(done) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("automatic continuation did not enter provider")
	}
	other := newLocalSurfActionWorker(x.f.handler.chat)
	other.advance(t.Context(), store, host, actor, rows[job], until.Add(time.Second))
	if callbacks.Load() != 1 {
		close(release)
		t.Fatal("two workers claimed the same continuation", callbacks.Load())
	}
	x.request("POST", "/api/local/interactions", map[string]string{"action": "cancel", "job_id": job}, 200)
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not drain continuation")
	}
	if effects.Load() != 0 {
		t.Fatal("cancelled continuation caused an external effect")
	}
}

func TestSurfActionFollowupQueryRechecksSource(t *testing.T) {
	x, m, scope, w := surfFollowupFixture(t)
	job := surfFollowupTask(t, x, scope, "surf-stop-query")
	var queries atomic.Int32
	m.interact = func(ctx context.Context, _ string, in stationbrowser.DouyinInteractionRequest, authorize func() error) (stationbrowser.DouyinInteractionResult, error) {
		if in.Submitted {
			x.worker.mu.Lock()
			policy := x.worker.policy
			policy.Paused = true
			err := x.worker.save(policy)
			x.worker.mu.Unlock()
			if err != nil {
				t.Error(err)
			}
			if err := authorize(); err != nil {
				return stationbrowser.DouyinInteractionResult{}, err
			}
			queries.Add(1)
			return stationbrowser.DouyinInteractionResult{State: "unknown", Code: "receipt_pending", Submitted: true}, nil
		}
		if err := in.BeforeSubmit(ctx); err != nil {
			return stationbrowser.DouyinInteractionResult{}, err
		}
		return stationbrowser.DouyinInteractionResult{State: "unknown", Code: "receipt_pending", Submitted: true}, nil
	}
	x.request("POST", "/api/local/interactions", map[string]string{"action": "submit", "job_id": job}, 200)
	w.tick(t.Context(), time.Now().UTC())
	if queries.Load() != 0 {
		t.Fatal("a paused source still performed a platform receipt read")
	}
}

func TestSurfActionFollowupShutdownKeepsOriginalCheckpoint(t *testing.T) {
	x, m, scope, w := surfFollowupFixture(t)
	job := surfFollowupTask(t, x, scope, "surf-shutdown")
	p := &localDouyinWebInteraction{home: x.f.handler.home}
	a, err := p.access(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.replyPacing(a, time.Now().UTC(), douyinReplyReserve); err != nil {
		t.Fatal(err)
	}
	x.request("POST", "/api/local/interactions", map[string]string{"action": "submit", "job_id": job}, 200)
	w.tick(t.Context(), time.Now().UTC())
	store, actor, _ := surfFollowupState(t, x)
	clearSyntheticReplyWindow(t, x)
	if err := editSurfWaits(t.Context(), store, actor, func(rows map[string]surfActionWait) error {
		row := rows[job]
		row.Next = time.Now().Add(-time.Second).UTC()
		rows[job] = row
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	m.interact = func(ctx context.Context, _ string, _ stationbrowser.DouyinInteractionRequest, _ func() error) (stationbrowser.DouyinInteractionResult, error) {
		close(entered)
		<-ctx.Done()
		return stationbrowser.DouyinInteractionResult{}, ctx.Err()
	}
	h := x.f.handler.chat
	h.startSurfActionContinuations()
	w.notify()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("background loop did not restore due task")
	}
	closed := make(chan error, 1)
	go func() { closed <- x.f.handler.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("desktop close waited for platform timeout")
	}
	reopened, err := pluginhost.OpenStore(filepath.Join(x.f.dir, "project-tools", actor.Account), actor.Account)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Job(t.Context(), job); err != nil {
		t.Fatal("shutdown lost original interaction", err)
	}
	_, rows, err := readSurfWaits(t.Context(), reopened, actor)
	if err != nil || rows[job].Lease != "" || rows[job].Job != job {
		t.Fatal("shutdown closed SQLite before checkpoint was saved", rows, err)
	}
}
