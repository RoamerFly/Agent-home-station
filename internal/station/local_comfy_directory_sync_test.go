package station

import (
	"encoding/json"
	"errors"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"wechat-bot/internal/pluginhost"
)

func TestInterruptedComfyJobsDoNotBlockPackageRecovery(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows project-tool installation")
	}
	f := localComfyFixture(t)
	record, err := f.handler.home.identity.Read()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeLocalComfyChatWorkflow(t, root)
	backend := &syntheticLocalComfy{root: root, fish: record.FishID, running: true}
	setUpLocalComfyChat(t, f, backend)
	s := f.handler.chat.projectTools
	host, err := s.forFish(record.FishID)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := host.store.Installed(t.Context(), localComfyPackage)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := host.store.Configuration(t.Context(), localComfyPackage)
	if err != nil {
		t.Fatal(err)
	}
	makeJob := func(key string) pluginhost.Job {
		t.Helper()
		job, created, err := host.store.CreateJob(t.Context(), pluginhost.Job{
			Plugin: localComfyPackage, Version: installed.Package.Version, PackageDigest: installed.Digest,
			ConfigRevision: strconv.Itoa(configuration.Revision), Tool: localComfyStartTool,
			Arguments: json.RawMessage(`{}`), Context: pluginhost.Invocation{
				Account: host.store.Account(), Thread: "synthetic-comfy-recovery", Requester: "owner",
				Purpose: "synthetic package recovery", RequestKey: key,
			},
		})
		if err != nil || !created {
			t.Fatalf("create ComfyUI job: %v", err)
		}
		return job
	}
	pending := makeJob("pending")
	running := makeJob("running")
	running, err = host.store.ClaimJob(t.Context(), running.ID, running.Revision)
	if err != nil {
		t.Fatal(err)
	}
	running, err = host.store.AcknowledgeJob(t.Context(), running.ID, running.Revision, running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := s.settleInterruptedComfyJobs(t.Context(), host, installed.Digest); err != nil || changed {
		t.Fatalf("live backend jobs were settled: changed=%t err=%v", changed, err)
	}
	backend.running = false
	if changed, err := s.settleInterruptedComfyJobs(t.Context(), host, installed.Digest); err != nil || !changed {
		t.Fatalf("interrupted backend jobs not settled: changed=%t err=%v", changed, err)
	}
	for _, original := range []pluginhost.Job{pending, running} {
		job, err := host.store.Job(t.Context(), original.ID)
		if err != nil || job.State != "failed" || job.ID != original.ID {
			t.Fatalf("original job not retained as failed: %+v %v", job, err)
		}
		if original.State == "running" && !strings.Contains(string(job.Result), "original_backend_not_running") {
			t.Fatalf("lost backend reason missing: %s", job.Result)
		}
	}
	if busy, err := host.store.HasUnfinishedJobs(t.Context(), localComfyPackage); err != nil || busy {
		t.Fatalf("package recovery remains blocked: busy=%t err=%v", busy, err)
	}
}

func TestComfyToolExplainsAutomaticStartAndSpecificFailure(t *testing.T) {
	if !strings.Contains(localComfyStartToolSpec().Description, "服务空闲关闭时会自动启动") {
		t.Fatal("ComfyUI tool did not explain its automatic startup")
	}
	for _, tc := range []struct{ input, reason string }{
		{"station_comfyui_port_occupied_no_adoption", "comfyui_port_occupied"},
		{"station_comfyui_start_timeout", "comfyui_start_timeout"},
		{"station_comfyui_start_failed_check_private_log", "comfyui_process_start_failed"},
	} {
		reason, next := localComfyStartupFailure(errors.New(tc.input))
		if reason != tc.reason || next == "" {
			t.Fatalf("startup failure not actionable: %q %q", reason, next)
		}
	}
}
