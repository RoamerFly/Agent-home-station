package pluginruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"wechat-bot/internal/pluginhost"
)

type invocationSession struct {
	calls int
	call  func(pluginhost.Invocation, string, json.RawMessage) (json.RawMessage, error)
}

func (s *invocationSession) Health(context.Context) error { return nil }
func (s *invocationSession) Close() error                 { return nil }
func (s *invocationSession) Call(_ context.Context, c pluginhost.Invocation, name string, args json.RawMessage) (json.RawMessage, error) {
	s.calls++
	return s.call(c, name, args)
}

type invocationPolicy struct {
	check    func(pluginhost.Job) error
	validate func(pluginhost.Job, json.RawMessage) (VerifiedResult, error)
}

func (p invocationPolicy) Check(_ context.Context, j pluginhost.Job, _ pluginhost.Capability) error {
	if p.check != nil {
		return p.check(j)
	}
	return nil
}
func (p invocationPolicy) Validate(_ context.Context, j pluginhost.Job, _ pluginhost.Capability, raw json.RawMessage) (VerifiedResult, error) {
	if p.validate != nil {
		return p.validate(j, raw)
	}
	return VerifiedResult{Result: raw}, nil
}

func TestInstalledInvocationCheckpointsAndNeverReplaysUnknownWork(t *testing.T) {
	for _, mode := range []string{"sync", "interrupted", "invalid-result", "changed-source", "async", "cancel-race", "known-failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, i := installedFixture(t)
			request := CallRequest{Plugin: i.Package.ID, Capability: "echo", Context: invocation(), Arguments: json.RawMessage(`{"text":"synthetic"}`)}
			if mode == "async" {
				request.Capability = "async"
			}
			var active pluginhost.Job
			policy := invocationPolicy{check: func(j pluginhost.Job) error { active = j; return nil }}
			session := &invocationSession{call: func(g pluginhost.Invocation, name string, a json.RawMessage) (json.RawMessage, error) {
				if g.Account != "a" || name != request.Capability {
					t.Fatal("host binding lost")
				}
				if mode == "interrupted" {
					return nil, errors.New("connection closed after submit")
				}
				if mode == "cancel-race" {
					j, e := s.Job(ctx, active.ID)
					if e != nil {
						t.Fatal(e)
					}
					if _, e = s.RequestJobCancel(ctx, j.ID, j.Revision); e != nil {
						t.Fatal(e)
					}
				}
				return json.RawMessage(`{"text":"verified synthetic result"}`), nil
			}}
			c, e := NewController(s, startFunc(func(context.Context, pluginhost.Package, pluginhost.Grant) (pluginhost.Session, error) {
				return session, nil
			}))
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close()
			if e = c.Start(ctx, i.Package.ID, i.Revision); e != nil {
				t.Fatal(e)
			}
			if mode == "invalid-result" {
				policy.validate = func(pluginhost.Job, json.RawMessage) (VerifiedResult, error) {
					return VerifiedResult{}, errors.New("asset lineage rejected")
				}
			}
			if mode == "async" {
				policy.validate = func(pluginhost.Job, json.RawMessage) (VerifiedResult, error) {
					return VerifiedResult{RemoteID: "remote-original"}, nil
				}
			}
			if mode == "known-failure" {
				policy.validate = func(pluginhost.Job, json.RawMessage) (VerifiedResult, error) {
					return VerifiedResult{Failed: true, Result: json.RawMessage(`{"ok":false,"error":"no_results"}`)}, nil
				}
			}
			if mode == "changed-source" {
				policy.check = func(j pluginhost.Job) error {
					if session.calls > 0 {
						return errors.New("source forgotten")
					}
					return nil
				}
			}
			got, e := c.Invoke(ctx, request, policy)
			uncertain := mode == "interrupted" || mode == "invalid-result" || mode == "changed-source"
			if (e != nil) != uncertain || session.calls != 1 {
				t.Fatal(got, e, session.calls)
			}
			want := "completed"
			if mode == "known-failure" {
				want = "failed"
			}
			if uncertain {
				want = "needs_review"
			}
			if mode == "async" {
				want = "running"
			}
			if got.Job.State != want {
				t.Fatal("wrong persisted state", got)
			}
			if mode == "cancel-race" && !got.Job.CancelRequested {
				t.Fatal("cancel request lost")
			}
			if mode == "async" && got.Job.RemoteID != "remote-original" {
				t.Fatal("lost remote identity")
			}
			if !uncertain && mode != "async" && len(got.Job.Result) == 0 {
				t.Fatal("result not retained")
			}
			// A new controller simulates runtime restart; only explicit Start runs
			// a process, and submitting the same logical request cannot call again.
			if e = c.Close(); e != nil {
				t.Fatal(e)
			}
			c, e = NewController(s, startFunc(func(context.Context, pluginhost.Package, pluginhost.Grant) (pluginhost.Session, error) {
				return session, nil
			}))
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close()
			i, e = s.Installed(ctx, i.Package.ID)
			if e != nil {
				t.Fatal(e)
			}
			if e = c.Start(ctx, i.Package.ID, i.Revision); e != nil {
				t.Fatal(e)
			}
			retry, e := c.Invoke(ctx, request, invocationPolicy{})
			if e != nil || !retry.Reused || retry.Job.ID != got.Job.ID || session.calls != 1 {
				t.Fatal("request replayed", retry, e, session.calls)
			}
			request.Arguments = json.RawMessage(`{"text":"different"}`)
			if _, e = c.Invoke(ctx, request, invocationPolicy{}); !errors.Is(e, pluginhost.ErrRequestConflict) || session.calls != 1 {
				t.Fatal("changed request reused key", e)
			}
		})
	}
}

func TestInstalledInvocationRejectsMissingPolicyUnknownToolsAndForeignAccount(t *testing.T) {
	s, i := installedFixture(t)
	session := &invocationSession{call: func(pluginhost.Invocation, string, json.RawMessage) (json.RawMessage, error) {
		t.Fatal("unauthorized process call")
		return nil, nil
	}}
	c, e := NewController(s, startFunc(func(context.Context, pluginhost.Package, pluginhost.Grant) (pluginhost.Session, error) {
		return session, nil
	}))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	ctx := context.Background()
	if e = c.Start(ctx, i.Package.ID, i.Revision); e != nil {
		t.Fatal(e)
	}
	r := CallRequest{Plugin: i.Package.ID, Capability: "echo", Context: invocation(), Arguments: json.RawMessage(`{}`)}
	if _, e = c.Invoke(ctx, r, nil); e == nil {
		t.Fatal("missing verifier accepted")
	}
	r.Capability = "injected-tool"
	if _, e = c.Invoke(ctx, r, invocationPolicy{}); e == nil {
		t.Fatal("undeclared tool accepted")
	}
	r.Capability = "echo"
	r.Context.Account = "other-account"
	if _, e = c.Invoke(ctx, r, invocationPolicy{}); e == nil {
		t.Fatal("foreign account accepted")
	}
	page, e := s.Jobs(ctx, i.Package.ID, 0, 20)
	if e != nil || len(page.Items) != 0 {
		t.Fatal("rejected calls created tasks", page, e)
	}
}
