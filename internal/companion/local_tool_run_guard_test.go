package companion

import (
	"context"
	"errors"
	"testing"
)

func TestGuardedToolRunRechecksHostAndDeduplicatesOperations(t *testing.T) {
	allowed := true
	run := NewGuardedToolRun(func(context.Context) error {
		if !allowed {
			return errors.New("revoked")
		}
		return nil
	})
	op := LocalToolCheckpoint{Key: "same", Tool: "project_rvc_convert", Grant: "owner"}
	if err := run.Reserve(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	if err := run.Reserve(context.Background(), op); err != nil || len(run.Operations) != 1 {
		t.Fatalf("duplicate operation was not idempotent: %v %#v", err, run.Operations)
	}
	allowed = false
	if err := run.RecordResult(context.Background(), op.Key, `{"state":"accepted"}`); err == nil {
		t.Fatal("revoked host guard was not rechecked")
	}
}
