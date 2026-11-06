package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func applicationJobFixture() Job {
	return Job{
		Version:        "adapter-v1",
		PackageDigest:  "authorization-v7",
		ConfigRevision: "connection-v3",
		Tool:           "publish_draft",
		Context: Invocation{
			Account: "fish-1", Thread: "local-owner", Requester: "local-owner",
			Purpose: "publish a verified local work", RequestKey: "draft-1",
			SourceVersions: map[string]int{"artifact": 4},
		},
		Arguments: json.RawMessage(`{"artifact_id":"work-synthetic","target":"draft"}`),
	}
}

func TestCreateApplicationJobUsesDurableHostLedgerWithoutPluginInstall(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir(), "fish-1")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	input := applicationJobFixture()
	first, created, err := store.CreateApplicationJob(ctx, "douyin", input)
	if err != nil || !created || first.Plugin != "application/douyin" || first.State != "pending" {
		t.Fatalf("first application job = %#v, created=%v, error=%v", first, created, err)
	}
	retry, created, err := store.CreateApplicationJob(ctx, "douyin", input)
	if err != nil || created || retry.ID != first.ID {
		t.Fatalf("idempotent application retry = %#v, created=%v, error=%v", retry, created, err)
	}
	input.ConfigRevision = "connection-v4"
	if _, _, err = store.CreateApplicationJob(ctx, "douyin", input); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("changed authorization version was accepted: %v", err)
	}
	input.ConfigRevision = "connection-v3"
	input.Context.Account = "other-fish"
	if _, _, err = store.CreateApplicationJob(ctx, "douyin", input); err == nil {
		t.Fatal("another fish account was accepted")
	}
	claimed, err := store.ClaimJob(ctx, first.ID, first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	acknowledged, err := store.AcknowledgeJob(ctx, first.ID, claimed.Revision, "remote-draft-1")
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := store.RequestJobCancel(ctx, first.ID, acknowledged.Revision)
	if err != nil || !cancelled.CancelRequested || cancelled.Recovery() != "query" {
		t.Fatalf("application cancellation was not retained: %#v, %v", cancelled, err)
	}
	page, err := store.Jobs(ctx, "application/douyin", 0, 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != first.ID {
		t.Fatalf("application page = %#v, %v", page, err)
	}
}

func TestApplicationJobsCannotUseProjectPluginEntryPoint(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir(), "fish-1")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	input := applicationJobFixture()
	input.Plugin = "application/douyin"
	if _, _, err = store.CreateJob(ctx, input); err == nil {
		t.Fatal("untrusted plugin path created an application job")
	}
	if _, _, err = store.CreateApplicationJob(ctx, "bad/id", applicationJobFixture()); err == nil {
		t.Fatal("invalid application id was accepted")
	}
	missing := applicationJobFixture()
	missing.ConfigRevision = ""
	if _, _, err = store.CreateApplicationJob(ctx, "douyin", missing); err == nil {
		t.Fatal("missing authorization version was accepted")
	}
	project := applicationJobFixture()
	project.Plugin, project.Version, project.PackageDigest, project.ConfigRevision = "speech", "1", "missing", "0"
	if _, _, err = store.CreateJob(ctx, project); err == nil {
		t.Fatal("ordinary plugin job bypassed installed package checks")
	}
}
