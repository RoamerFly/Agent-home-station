package pluginruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"wechat-bot/internal/pluginhost"
)

func TestInvocationFailureDiagnosticsAreHostOwnedAndNeverReplay(t *testing.T) {
	for _, stage := range []pluginhost.JobFailureStage{pluginhost.JobFailureDispatch, pluginhost.JobFailureAuthority, pluginhost.JobFailureValidation, pluginhost.JobFailureResultMode} {
		t.Run(string(stage), func(t *testing.T) {
			const sensitive = "synthetic-secret-73 https://private.invalid/asset?token=synthetic-key"
			ctx := context.Background()
			store, installed := installedFixture(t)
			session := &invocationSession{call: func(pluginhost.Invocation, string, json.RawMessage) (json.RawMessage, error) {
				if stage == pluginhost.JobFailureDispatch {
					return nil, errors.New(sensitive)
				}
				return json.RawMessage(`{"untrusted":"` + sensitive + `","diagnostic":{"stage":"injected","code":"forged"}}`), nil
			}}
			policy := invocationPolicy{
				check: func(pluginhost.Job) error {
					if stage == pluginhost.JobFailureAuthority && session.calls > 0 {
						return errors.New(sensitive)
					}
					return nil
				},
				validate: func(pluginhost.Job, json.RawMessage) (VerifiedResult, error) {
					if stage == pluginhost.JobFailureValidation {
						return VerifiedResult{}, errors.New(sensitive)
					}
					return VerifiedResult{RemoteID: "invalid-for-sync", Result: json.RawMessage(`{"secret":"` + sensitive + `"}`)}, nil
				},
			}
			start := startFunc(func(context.Context, pluginhost.Package, pluginhost.Grant) (pluginhost.Session, error) {
				return session, nil
			})
			controller, err := NewController(store, start)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = controller.Close() })
			if err = controller.Start(ctx, installed.Package.ID, installed.Revision); err != nil {
				t.Fatal(err)
			}
			request := CallRequest{Plugin: installed.Package.ID, Capability: "echo", Context: invocation(), Arguments: json.RawMessage(`{"text":"synthetic"}`)}
			result, err := controller.Invoke(ctx, request, policy)
			if err == nil || session.calls != 1 || result.Job.State != "needs_review" {
				t.Fatalf("failure lost: %+v %v calls=%d", result, err, session.calls)
			}
			assertDiagnostic := func(job pluginhost.Job) {
				t.Helper()
				if job.Diagnostic == nil || job.Diagnostic.Stage != stage || job.Diagnostic.Code != "plugin_invocation_failed" || len(job.Result) != 0 {
					t.Fatalf("unsafe or missing diagnostic: %+v", job)
				}
				wire, marshalErr := json.Marshal(job)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				for _, forbidden := range []string{"synthetic-secret-73", "https://private.invalid", "synthetic-key", "untrusted", "injected", "forged"} {
					if strings.Contains(string(wire), forbidden) {
						t.Fatalf("untrusted error/output leaked into job: %s", forbidden)
					}
				}
			}
			assertDiagnostic(result.Job)
			stored, err := store.Job(ctx, result.Job.ID)
			if err != nil {
				t.Fatal(err)
			}
			assertDiagnostic(stored)
			if err = controller.Close(); err != nil {
				t.Fatal(err)
			}
			controller, err = NewController(store, start)
			if err != nil {
				t.Fatal(err)
			}
			installed, err = store.Installed(ctx, installed.Package.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = controller.Start(ctx, installed.Package.ID, installed.Revision); err != nil {
				t.Fatal(err)
			}
			reused, err := controller.Invoke(ctx, request, invocationPolicy{})
			if err != nil || !reused.Reused || reused.Job.ID != stored.ID || session.calls != 1 {
				t.Fatalf("uncertain submission replayed: %+v %v calls=%d", reused, err, session.calls)
			}
			assertDiagnostic(reused.Job)
		})
	}
}

func TestInvocationCancelledCallerStillCheckpointsSafeDiagnostic(t *testing.T) {
	store, installed := installedFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &invocationSession{call: func(pluginhost.Invocation, string, json.RawMessage) (json.RawMessage, error) {
		cancel()
		return nil, context.Canceled
	}}
	controller, err := NewController(store, startFunc(func(context.Context, pluginhost.Package, pluginhost.Grant) (pluginhost.Session, error) {
		return session, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	if err = controller.Start(ctx, installed.Package.ID, installed.Revision); err != nil {
		t.Fatal(err)
	}
	result, err := controller.Invoke(ctx, CallRequest{Plugin: installed.Package.ID, Capability: "echo", Context: invocation(), Arguments: json.RawMessage(`{}`)}, invocationPolicy{})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancel cause lost", err)
	}
	job, err := store.Job(context.Background(), result.Job.ID)
	if err != nil || job.State != "needs_review" || job.Diagnostic == nil || job.Diagnostic.Stage != pluginhost.JobFailureDispatch || len(job.Result) != 0 {
		t.Fatal("cancelled caller prevented durable diagnostic", job, err)
	}
}
