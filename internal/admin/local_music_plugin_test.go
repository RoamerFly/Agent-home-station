package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/pluginhost"
)

type localMusicRecoveryNative struct {
	*fakeNative
	message linuxnative.Message
}

func (n *localMusicRecoveryNative) Message(context.Context, string) (linuxnative.Message, error) {
	return n.message, nil
}

func localMusicPluginFixture(t *testing.T, endpoint string) (*webFixture, *permittedTools, *pluginhost.Store) {
	t.Helper()
	f := setup(t)
	t.Setenv("WECHAT_WORKSPACE_ROOT", filepath.Join(f.dir, "managed-workspaces"))
	s, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := s.Prepare(t.Context(), "../../plugins/music")
	if err != nil {
		t.Fatal(err)
	}
	i, err := s.Install(t.Context(), prepared.Token, prepared.Digest, prepared.Package.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := json.Marshal(endpoint)
	_, err = s.SaveConfiguration(t.Context(), "music", pluginhost.ConfigUpdate{Values: map[string]json.RawMessage{"local_endpoint": value}, Secrets: map[string]string{"local_token": "synthetic-local-private-token"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetEnabled(t.Context(), "music", i.Revision, true); err != nil {
		t.Fatal(err)
	}
	f.manager.config.Companion.Permissions.Remember = true
	p := &permittedTools{manager: f.manager, trace: companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "synthetic-user", TraceID: "local-music-request", Text: "synthetic music request", Source: &companion.SourceRef{MessageID: "local-music-request"}}}
	if err = f.manager.memory.Observe(p.trace, f.manager.config.Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	p.trace.Source, _ = f.manager.memory.SourceForInput(p.trace)
	return f, p, s
}

func localMusicDurableJob(t *testing.T, p *localMusicPluginPolicy) (pluginhost.Job, pluginhost.Capability) {
	t.Helper()
	j, _, err := p.store.CreateJob(t.Context(), pluginhost.Job{Plugin: "music", Tool: localMusicCapability, Version: p.version, PackageDigest: p.digest, ConfigRevision: "1", Context: p.invocation, Arguments: p.arguments})
	if err != nil {
		t.Fatal(err)
	}
	j, err = p.store.ClaimJob(t.Context(), j.ID, j.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return j, pluginhost.Capability{ID: localMusicCapability, Implementation: "ready", Permissions: []string{"network"}}
}

func TestLocalMusicEndpointAndRouteCatalogue(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:8765", "http://[::1]:8765/", "http://192.168.10.2:8765", "https://music.example"} {
		if !localMusicEndpoint(endpoint) {
			t.Fatal("valid fixed endpoint rejected", endpoint)
		}
	}
	withUserInfo := (&url.URL{Scheme: "http", Host: "127.0.0.1:8765", User: url.UserPassword("synthetic", "synthetic")}).String()
	for _, endpoint := range []string{"http://public.example", "http://wsl.localhost:8765", "http://127.0.0.1:8765/api", "http://127.0.0.1:8765/?token=a", withUserInfo, "http://8.8.8.8:8765"} {
		if localMusicEndpoint(endpoint) {
			t.Fatal("unsafe endpoint accepted", endpoint)
		}
	}
	for _, route := range [][2]string{{"GET", "/health"}, {"GET", "/rvc-health"}, {"GET", "/parameters"}, {"POST", "/jobs"}, {"POST", "/rvc-jobs"}, {"GET", "/jobs/original"}, {"GET", "/jobs/original/audio"}, {"POST", "/jobs/original/resume"}, {"POST", "/jobs/original/cancel"}} {
		if _, ok := localMusicRoute(route[0], route[1]); !ok {
			t.Fatal("needed route rejected", route)
		}
	}
	for _, route := range [][2]string{{"GET", "/jobs"}, {"POST", "/health"}, {"GET", "/jobs/../audio"}, {"GET", "/jobs/%2e%2e/audio"}, {"GET", "/jobs/original?other=a"}, {"DELETE", "/jobs/original"}, {"GET", "https://other.example/health"}, {"POST", "/voice-profiles"}} {
		if _, ok := localMusicRoute(route[0], route[1]); ok {
			t.Fatal("arbitrary route accepted", route)
		}
	}
}

func TestLocalMusicHTTPRejectsRedirectsAndForeignAccountReplies(t *testing.T) {
	redirected := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected++ }))
	defer target.Close()
	for _, mode := range []string{"redirect", "foreign_account"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
					return
				}
				w.Header().Set("X-Wechat-Agent-Account", accountDirectory("other-account"))
				w.Write([]byte(`{"ready":true}`))
			}))
			defer server.Close()
			_, _, status, err := localMusicHTTP(t.Context(), "POST", server.URL, []byte(`{}`), "original-operation", "synthetic-token", accountDirectory("test"))
			if redirected != 0 || err == nil && (mode != "redirect" || status != http.StatusTemporaryRedirect) {
				t.Fatal("redirect/foreign account reply accepted", err, redirected)
			}
			if mode == "foreign_account" {
				_, code := safeToolFailure(err)
				if code != "yue_account_scope_mismatch" {
					t.Fatal("foreign account error mapping changed", code, err)
				}
			}
		})
	}
}

func TestLocalMusicMissingPluginNeverUsesLegacyConfiguration(t *testing.T) {
	f := setup(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	{
		legacySpeechFixture := readLegacySpeechTestConfig(t, f.manager)
		legacySpeechFixture.YuEEnabled = true
		legacySpeechFixture.YuEEndpoint = server.URL
		legacySpeechFixture.YuEToken = "legacy-private-token"
		writeLegacySpeechMigrationFixture(t, f.manager, legacySpeechFixture)
	}
	p := &permittedTools{manager: f.manager, trace: companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner"}}
	if _, _, err := p.yueRequest(t.Context(), "GET", "/health", nil, ""); err == nil || !strings.Contains(err.Error(), "migration_required") || calls != 0 {
		t.Fatal("legacy HTTP fallback survived", err, calls)
	}
	if health := p.probeYuE(t.Context()); health.Ready || health.State != "migration_required" {
		t.Fatal(health)
	}
}

func TestLocalMusicBoundTransportOpaqueBodyAndPrivateBinaryCheckpoint(t *testing.T) {
	want := []byte("synthetic-private-audio-bytes")
	posted := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posted++
		if r.Method != "POST" || r.URL.Path != "/rvc-jobs" || r.Header.Get("Authorization") != "Bearer synthetic-local-private-token" || r.Header.Get("Idempotency-Key") != "stable-original-operation" || r.Header.Get("X-Wechat-Agent-Account") != accountDirectory("test") {
			t.Error("host transport changed bound request")
		}
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["reference_base64"] != "synthetic-reference-audio" || body["voice_profile_id"] != "private-synthetic-rvc" {
			t.Error("host changed reference/voice")
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("X-Wechat-Agent-Account", accountDirectory("test"))
		w.Write(want)
	}))
	defer server.Close()
	_, access, _ := localMusicPluginFixture(t, server.URL)
	p, err := access.newLocalMusicPluginPolicy(t.Context(), "POST", "/rvc-jobs", map[string]string{"reference_base64": "synthetic-reference-audio", "voice_profile_id": "private-synthetic-rvc"}, "stable-original-operation")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(p.arguments, []byte("synthetic-reference-audio")) || bytes.Contains(p.arguments, []byte("private-synthetic-rvc")) || bytes.Contains(p.arguments, []byte("token")) {
		t.Fatal("private request bytes crossed stdio", string(p.arguments))
	}
	j, cap := localMusicDurableJob(t, p)
	for _, bad := range []json.RawMessage{bytes.Replace(p.arguments, []byte("/rvc-jobs"), []byte("/jobs"), 1), bytes.Replace(p.arguments, []byte("request_0"), []byte("request_1"), 1), bytes.Replace(p.arguments, []byte("stable-original-operation"), []byte("new-key"), 1)} {
		if _, err = p.Service(t.Context(), j, cap, "wechat/music_bridge", bad); err == nil || posted != 0 {
			t.Fatal("changed callback reached HTTP", err, posted)
		}
	}
	reply, err := p.Service(t.Context(), j, cap, "wechat/music_bridge", p.arguments)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(reply, want) || bytes.Contains(reply, []byte(server.URL)) {
		t.Fatal("binary/endpoint crossed stdio", string(reply))
	}
	if _, err = p.Service(t.Context(), j, cap, "wechat/music_bridge", p.arguments); err == nil || posted != 1 {
		t.Fatal("callback replayed", err, posted)
	}
	forged := bytes.Replace(reply, []byte("audio/wav"), []byte("audio/mp3"), 1)
	if _, err = p.Validate(t.Context(), j, cap, forged); err == nil {
		t.Fatal("forged transport result accepted")
	}
	verified, err := p.Validate(t.Context(), j, cap, reply)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(verified.Private, want) || bytes.Contains(verified.Private, []byte(server.URL)) || bytes.Contains(verified.Private, []byte("synthetic-local-private-token")) {
		t.Fatal("large/secret payload stored in DB checkpoint")
	}
	completed, err := p.store.CompleteJobWithPrivate(t.Context(), j.ID, j.Revision, nil, verified.Result, verified.Private)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := p.store.PrivateJobResult(t.Context(), completed, func(ctx context.Context, j pluginhost.Job) error { return p.Check(ctx, j, cap) })
	if err != nil {
		t.Fatal(err)
	}
	var saved localMusicCheckpoint
	if json.Unmarshal(raw, &saved) != nil {
		t.Fatal("checkpoint malformed")
	}
	data, err := p.readCheckpoint(t.Context(), completed, saved)
	if err != nil || !bytes.Equal(data, want) {
		t.Fatal("private binary not verified", err)
	}
	if err = os.WriteFile(filepath.Join(p.replyRoot(), j.ID, "reply.bin"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = p.readCheckpoint(t.Context(), completed, saved); err == nil {
		t.Fatal("changed private file accepted")
	}
}

func TestLocalMusicUnknownAttemptAndSourceRevocationNeverReplay(t *testing.T) {
	for _, mode := range []string{"uncertain", "source_revoked"} {
		t.Run(mode, func(t *testing.T) {
			f, access, _ := localMusicPluginFixture(t, "http://127.0.0.1:8765")
			p, err := access.newLocalMusicPluginPolicy(t.Context(), "POST", "/jobs", map[string]string{"lyrics": "synthetic"}, "original-op")
			if err != nil {
				t.Fatal(err)
			}
			j, cap := localMusicDurableJob(t, p)
			calls := 0
			p.post = func(context.Context, string, string, []byte, string, string, string) ([]byte, string, int, error) {
				calls++
				if mode == "source_revoked" {
					if err := f.manager.memory.Clear(companion.ScopeID("wechat", "test", "filehelper")); err != nil {
						t.Fatal(err)
					}
					return []byte(`{"id":"original"}`), "application/json", 200, nil
				}
				return nil, "", 0, errors.New("synthetic uncertain attempt")
			}
			for n := 0; n < 2; n++ {
				if _, err = p.Service(t.Context(), j, cap, "wechat/music_bridge", p.arguments); err == nil {
					t.Fatal("uncertain/revoked call succeeded")
				}
			}
			if calls != 1 {
				t.Fatal("POST replayed", calls)
			}
			if _, err = p.Validate(t.Context(), j, cap, json.RawMessage(`{}`)); err == nil {
				t.Fatal("uncertain/revoked result accepted")
			}
		})
	}
}

func TestLocalMusicActorAndInstallationRevocationBlockTransport(t *testing.T) {
	for _, mode := range []string{"account", "thread", "actor", "disabled", "package"} {
		t.Run(mode, func(t *testing.T) {
			_, access, _ := localMusicPluginFixture(t, "http://127.0.0.1:8765")
			p, err := access.newLocalMusicPluginPolicy(t.Context(), "POST", "/jobs", map[string]string{"lyrics": "synthetic"}, "original-operation")
			if err != nil {
				t.Fatal(err)
			}
			j, cap := localMusicDurableJob(t, p)
			switch mode {
			case "account":
				access.trace.Account = "foreign"
			case "thread":
				access.trace.Thread = "another-room"
			case "actor":
				access.trace.Sender = "another-user"
			case "disabled":
				i, _ := p.store.Installed(t.Context(), "music")
				if _, err = p.store.SetEnabled(t.Context(), "music", i.Revision, false); err != nil {
					t.Fatal(err)
				}
			case "package":
				j.PackageDigest = strings.Repeat("f", 64)
			}
			calls := 0
			p.post = func(context.Context, string, string, []byte, string, string, string) ([]byte, string, int, error) {
				calls++
				return []byte(`{}`), "application/json", 200, nil
			}
			if _, err = p.Service(t.Context(), j, cap, "wechat/music_bridge", p.arguments); err == nil || calls != 0 {
				t.Fatal("revoked actor/installation reached HTTP", mode, err, calls)
			}
		})
	}
}

func TestLocalMusicFreshObservationsStableMutationKeysAndOriginalTaskBinding(t *testing.T) {
	f, access, _ := localMusicPluginFixture(t, "http://127.0.0.1:8765")
	a, err := access.newLocalMusicPluginPolicy(t.Context(), "GET", "/health", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := access.newLocalMusicPluginPolicy(t.Context(), "GET", "/health", nil, "")
	if err != nil || a.invocation.RequestKey == b.invocation.RequestKey {
		t.Fatal("stale health observation reused", err)
	}
	if _, err = access.newLocalMusicPluginPolicy(t.Context(), "GET", "/jobs/arbitrary-other-job", nil, ""); err == nil {
		t.Fatal("arbitrary job ID granted access")
	}
	task := agentTask{ID: "original-task", Account: "test", Thread: "filehelper", Sender: access.trace.Sender, State: "waiting", MusicJob: "original-job", OriginTrace: access.trace.TraceID, MusicSpec: &songSpec{VoiceID: "requested-voice", VoiceProfile: "private-requested-profile"}, Constraints: access.taskConstraints("music", "requested-voice", "private-requested-profile", "")}
	if err = f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	access.trace.TaskID = task.ID
	a, err = access.newLocalMusicPluginPolicy(t.Context(), "POST", "/jobs/original-job/resume", map[string]bool{}, "")
	if err != nil {
		t.Fatal(err)
	}
	access.trace.TraceID = "later-observation"
	b, err = access.newLocalMusicPluginPolicy(t.Context(), "POST", "/jobs/original-job/resume", map[string]bool{}, "")
	if err != nil || a.request.Key != b.request.Key {
		t.Fatal("resume lost stable identity", err)
	}
	f.manager.config.Enabled = false
	if err = b.Check(t.Context(), pluginhost.Job{Plugin: "music", Tool: localMusicCapability, Context: b.invocation}, pluginhost.Capability{}); err != nil {
		t.Fatal("paused ingress revoked existing task", err)
	}
	if _, err = access.newLocalMusicPluginPolicy(t.Context(), "GET", "/jobs/different-job", nil, ""); err == nil {
		t.Fatal("persistent task switched target job")
	}
	task.State = "cancelled"
	if err = f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	if err = b.Check(t.Context(), pluginhost.Job{Plugin: "music", Tool: localMusicCapability, Context: b.invocation}, pluginhost.Capability{}); err == nil {
		t.Fatal("cancelled task retained transport authority")
	}
}

func TestLocalMusicJobMutationsRequireOriginalRequesterAndExactCurrentTask(t *testing.T) {
	f, access, _ := localMusicPluginFixture(t, "http://127.0.0.1:8765")
	original := access.trace.Sender
	for _, task := range []agentTask{
		{ID: "requester-a-task", Account: "test", Thread: "filehelper", Sender: original, State: "waiting", MusicJob: "requester-a-job"},
		{ID: "requester-b-task", Account: "test", Thread: "filehelper", Sender: "requester-b", State: "waiting", MusicJob: "requester-b-job"},
		{ID: "unbound-task", Account: "test", Thread: "filehelper", Sender: "requester-b", State: "waiting"},
	} {
		if err := f.manager.agent.task(task); err != nil {
			t.Fatal(err)
		}
	}
	access.trace.Source = nil
	access.trace.Sender = "requester-b"
	if _, err := access.newLocalMusicPluginPolicy(t.Context(), "GET", "/jobs/requester-a-job", nil, ""); err != nil {
		t.Fatal("same-conversation read unexpectedly denied", err)
	}
	for _, action := range []string{"resume", "cancel"} {
		if _, err := access.newLocalMusicPluginPolicy(t.Context(), "POST", "/jobs/requester-a-job/"+action, map[string]bool{}, ""); err == nil {
			t.Fatal("requester B mutated requester A job", action)
		}
	}
	for _, taskID := range []string{"requester-b-task", "unbound-task", "nonexistent-task"} {
		access.trace.TaskID = taskID
		for _, method := range []string{"GET", "POST"} {
			path := "/jobs/requester-a-job"
			var body any
			if method == "POST" {
				path += "/resume"
				body = map[string]bool{}
			}
			if _, err := access.newLocalMusicPluginPolicy(t.Context(), method, path, body, ""); err == nil {
				t.Fatal("another matching job overwrote exact current task", taskID, method)
			}
		}
	}
	access.trace.TaskID = "requester-a-task"
	access.trace.Sender = original
	if _, err := access.newLocalMusicPluginPolicy(t.Context(), "POST", "/jobs/requester-a-job/resume", map[string]bool{}, ""); err != nil {
		t.Fatal("original requester lost mutation authority", err)
	}
}

func TestParentTaskMayReadExactlyOneChildMusicStatusButCannotMutateIt(t *testing.T) {
	f, access, _ := localMusicPluginFixture(t, "http://127.0.0.1:8765")
	parent := agentTask{ID: "parent-task", Account: "test", Thread: "filehelper", Sender: access.trace.Sender, State: "running", OriginTrace: access.trace.TraceID}
	child := agentTask{ID: "child-task", ParentID: parent.ID, Account: parent.Account, Thread: parent.Thread, Sender: parent.Sender, State: "waiting", OriginTrace: parent.OriginTrace, MusicJob: "child-rvc-job", MusicSpec: &songSpec{Engine: "rvc"}}
	for _, task := range []agentTask{parent, child} {
		if err := f.manager.agent.task(task); err != nil {
			t.Fatal(err)
		}
	}
	access.trace.TaskID = parent.ID
	var proof string
	for attempt := 0; attempt < 2; attempt++ {
		policy, err := access.newLocalMusicPluginPolicy(t.Context(), "GET", "/jobs/child-rvc-job", nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if proof != "" && proof != policy.taskProof {
			t.Fatal("repeated parent status read changed its durable proof")
		}
		proof = policy.taskProof
	}
	for _, path := range []string{"/jobs/child-rvc-job/resume", "/jobs/child-rvc-job/cancel", "/jobs/child-rvc-job/audio"} {
		method, body := "POST", any(map[string]bool{})
		if path[len(path)-5:] == "audio" {
			method, body = "GET", nil
		}
		if _, err := access.newLocalMusicPluginPolicy(t.Context(), method, path, body, ""); err == nil || !strings.Contains(err.Error(), "子任务") {
			t.Fatalf("parent obtained protected child operation %s: %v", path, err)
		}
	}
	child.State = "cancelled"
	if err := f.manager.agent.task(child); err != nil {
		t.Fatal(err)
	}
	if _, err := access.newLocalMusicPluginPolicy(t.Context(), "GET", "/jobs/child-rvc-job", nil, ""); err == nil {
		t.Fatal("cancelled child remained readable through its parent")
	}
	child.State = "waiting"
	if err := f.manager.agent.task(child); err != nil {
		t.Fatal(err)
	}
	access.trace.Sender = "another-requester"
	if _, err := access.newLocalMusicPluginPolicy(t.Context(), "GET", "/jobs/child-rvc-job", nil, ""); err == nil {
		t.Fatal("another requester read a parent's child")
	}
	access.trace.Sender = parent.Sender
	other := child
	other.ID = "second-child-task"
	if err := f.manager.agent.task(other); err != nil {
		t.Fatal(err)
	}
	if _, err := access.newLocalMusicPluginPolicy(t.Context(), "GET", "/jobs/child-rvc-job", nil, ""); err == nil {
		t.Fatal("ambiguous child mapping granted status read")
	}
}

func TestLocalMusicCancelledTaskAllowsOnlyFreshAuthorizedMetadataRead(t *testing.T) {
	f, access, _ := localMusicPluginFixture(t, "http://127.0.0.1:8765")
	original := access.trace.Sender
	if err := f.manager.agent.task(agentTask{ID: "cancelled-task", Account: "test", Thread: "filehelper", Sender: original, State: "cancelled", MusicJob: "cancelled-job"}); err != nil {
		t.Fatal(err)
	}
	access.trace.TraceID = "fresh-inspection"
	if _, err := access.newLocalMusicPluginPolicy(t.Context(), "GET", "/jobs/cancelled-job", nil, ""); err != nil {
		t.Fatal("fresh original requester cannot report cancelled metadata", err)
	}
	for _, request := range [][2]string{{"GET", "/jobs/cancelled-job/audio"}, {"POST", "/jobs/cancelled-job/resume"}, {"POST", "/jobs/cancelled-job/cancel"}} {
		var body any
		if request[0] == "POST" {
			body = map[string]bool{}
		}
		if _, err := access.newLocalMusicPluginPolicy(t.Context(), request[0], request[1], body, ""); err == nil {
			t.Fatal("cancelled job granted audio or mutation", request)
		}
	}
	access.trace.TaskID = "cancelled-task"
	if _, err := access.newLocalMusicPluginPolicy(t.Context(), "GET", "/jobs/cancelled-job", nil, ""); err == nil {
		t.Fatal("cancelled original continuation regained authority")
	}
	access.trace.TaskID = ""
	access.trace.Source = nil
	access.trace.Sender = "another-requester"
	if _, err := access.newLocalMusicPluginPolicy(t.Context(), "GET", "/jobs/cancelled-job", nil, ""); err == nil {
		t.Fatal("another requester read cancelled metadata without administrator grant")
	}
	cfg := f.manager.agent.config()
	cfg.OwnerIDs = []string{access.trace.Sender}
	cfg.PrivilegedThreads = []string{"filehelper"}
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	p, err := access.newLocalMusicPluginPolicy(t.Context(), "GET", "/jobs/cancelled-job", nil, "")
	if err != nil {
		t.Fatal("explicit administrator cannot report cancelled metadata", err)
	}
	j, cap := localMusicDurableJob(t, p)
	cfg = f.manager.agent.config()
	cfg.OwnerIDs = nil
	if err = f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	calls := 0
	p.post = func(context.Context, string, string, []byte, string, string, string) ([]byte, string, int, error) {
		calls++
		return []byte(`{"state":"cancelled"}`), "application/json", 200, nil
	}
	if _, err = p.Service(t.Context(), j, cap, "wechat/music_bridge", p.arguments); err == nil || calls != 0 {
		t.Fatal("revoked administrator read cancelled metadata", err, calls)
	}
}

func TestLocalMusicSamplingErrorFieldGrammarRetainsOnlySafeNativeKeys(t *testing.T) {
	for _, code := range []string{"sampling_outside_native_spec:render.steps", "sampling_invalid_value:semantic.top_k", "sampling_foo:render.diffusion_steps", "ordinary_provider_error"} {
		if !localMusicSafeCode(code) {
			t.Fatal("actionable native field rejected", code)
		}
	}
	for _, code := range []string{"sampling_outside_native_spec:https://private.example/key", "sampling_outside_native_spec:render/steps", "sampling_outside_native_spec:render..steps", "sampling_outside_native_spec:render.steps=secret", "sampling_outside_native_spec:render.steps secret", "sampling_outside_native_spec:render.steps\nsecret", "sampling_outside_native_spec:C:\\secret", "sampling_outside_native_spec:render.1step", "ordinary:render.steps", strings.Repeat("s", 161)} {
		if localMusicSafeCode(code) {
			t.Fatal("diagnostic/path/credential-like error accepted", code)
		}
	}
}

func TestLocalMusicAdministratorOverrideRevocationStopsMutation(t *testing.T) {
	for _, phase := range []string{"before", "during"} {
		t.Run(phase, func(t *testing.T) {
			f, access, _ := localMusicPluginFixture(t, "http://127.0.0.1:8765")
			if err := f.manager.agent.task(agentTask{ID: "original-task", Account: "test", Thread: "filehelper", Sender: access.trace.Sender, State: "waiting", MusicJob: "original-job"}); err != nil {
				t.Fatal(err)
			}
			cfg := f.manager.agent.config()
			cfg.OwnerIDs = []string{"synthetic-administrator"}
			cfg.PrivilegedThreads = []string{"filehelper"}
			if err := f.manager.agent.save(cfg); err != nil {
				t.Fatal(err)
			}
			access.trace.Source = nil
			access.trace.Sender = "synthetic-administrator"
			p, err := access.newLocalMusicPluginPolicy(t.Context(), "POST", "/jobs/original-job/resume", map[string]bool{}, "")
			if err != nil {
				t.Fatal("explicit administrator override rejected", err)
			}
			j, cap := localMusicDurableJob(t, p)
			revoke := func() {
				cfg = f.manager.agent.config()
				cfg.OwnerIDs = nil
				if err := f.manager.agent.save(cfg); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			p.post = func(context.Context, string, string, []byte, string, string, string) ([]byte, string, int, error) {
				calls++
				if phase == "during" {
					revoke()
				}
				return []byte(`{"id":"original-job"}`), "application/json", 200, nil
			}
			if phase == "before" {
				revoke()
			}
			if _, err = p.Service(t.Context(), j, cap, "wechat/music_bridge", p.arguments); err == nil {
				t.Fatal("revoked administrator mutated original requester job")
			}
			want := 0
			if phase == "during" {
				want = 1
			}
			if calls != want {
				t.Fatal("revoked administrator transport replayed", calls, want)
			}
			if _, err = p.Validate(t.Context(), j, cap, json.RawMessage(`{}`)); err == nil {
				t.Fatal("revoked administrator retained result")
			}
		})
	}
}

func TestLocalMusicAdminHealthUsesSessionAuthorityAndCannotAccessJobs(t *testing.T) {
	f, _, _ := localMusicPluginFixture(t, "http://127.0.0.1:8765")
	access := &permittedTools{manager: f.manager}
	valid := true
	authority := &speechAdminTestAuthority{Account: "test", Session: "synthetic-admin-session", RequestID: "synthetic-admin-health", Check: func(ctx context.Context) error {
		if !valid {
			return errors.New("synthetic session expired")
		}
		return ctx.Err()
	}}
	ctx := context.WithValue(t.Context(), speechAdminTestContextKey{}, authority)
	for _, path := range []string{"/health", "/rvc-health", "/parameters"} {
		p, err := access.newLocalMusicPluginPolicy(ctx, "GET", path, nil, "")
		if err != nil {
			t.Fatal("admin health without chat identity rejected", path, err)
		}
		if p.invocation.Account != "test" || p.invocation.Thread != "host:admin-music" || p.invocation.Requester != "administrator" || p.invocation.EvidenceEpoch != "" || len(p.invocation.SourceVersions) != 0 || access.trace.Sender != "" {
			t.Fatal("admin observation impersonated a chat sender", p.invocation)
		}
	}
	for _, request := range [][2]string{{"GET", "/jobs/arbitrary"}, {"GET", "/jobs/arbitrary/audio"}, {"GET", "/sampling-options"}, {"POST", "/jobs"}, {"POST", "/rvc-jobs"}, {"POST", "/jobs/arbitrary/resume"}, {"POST", "/jobs/arbitrary/cancel"}} {
		var body any
		if request[0] == "POST" {
			body = map[string]bool{}
		}
		if _, err := access.newLocalMusicPluginPolicy(ctx, request[0], request[1], body, ""); err == nil {
			t.Fatal("admin health authority expanded", request)
		}
	}
	if _, err := access.newLocalMusicPluginPolicy(t.Context(), "GET", "/health", nil, ""); err == nil {
		t.Fatal("empty chat identity acquired admin health without session")
	}
	p, err := access.newLocalMusicPluginPolicy(ctx, "GET", "/health", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	j, cap := localMusicDurableJob(t, p)
	calls := 0
	p.post = func(context.Context, string, string, []byte, string, string, string) ([]byte, string, int, error) {
		calls++
		valid = false
		return []byte(`{"ready":true}`), "application/json", 200, nil
	}
	if _, err = p.Service(ctx, j, cap, "wechat/music_bridge", p.arguments); err == nil || calls != 1 {
		t.Fatal("session revoked during health retained result", err, calls)
	}
	if _, err = p.Validate(ctx, j, cap, json.RawMessage(`{}`)); err == nil {
		t.Fatal("expired health session read private checkpoint")
	}
	if _, err = access.newLocalMusicPluginPolicy(ctx, "GET", "/health", nil, ""); err == nil {
		t.Fatal("expired health session authorized another request")
	}
}

func TestLiveLocalMusicOfficialPluginControllerUsesPrivateReply(t *testing.T) {
	f, access, store := localMusicPluginFixture(t, "http://127.0.0.1:8765")
	controller := namedSpeechLiveController(t, store)
	f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
	installed, err := store.Installed(t.Context(), "music")
	if err != nil {
		t.Fatal(err)
	}
	if err = controller.Start(t.Context(), "music", installed.Revision); err != nil {
		t.Fatal("isolated controller start", err)
	}
	p, err := access.newLocalMusicPluginPolicy(t.Context(), "POST", "/rvc-jobs", map[string]string{"reference_base64": "synthetic-audio", "voice_profile_id": "private-synthetic"}, "original-local-operation")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	p.post = func(_ context.Context, method, endpoint string, body []byte, key, token, account string) ([]byte, string, int, error) {
		calls++
		if method != "POST" || endpoint != "http://127.0.0.1:8765/rvc-jobs" || key != "original-local-operation" || token != "synthetic-local-private-token" || account != accountDirectory("test") || !bytes.Contains(body, []byte("synthetic-audio")) {
			t.Fatal("unbound actual controller transport")
		}
		return []byte(`{"id":"local-original","state":"queued","engine":"rvc"}`), "application/json", 200, nil
	}
	data, mime, status, err := p.invoke(t.Context())
	if err != nil || status != 200 || mime != "application/json" || !bytes.Contains(data, []byte("local-original")) || calls != 1 {
		t.Fatal("official plugin/controller request failed", string(data), mime, status, err, calls)
	}
	if _, _, _, err = p.invoke(t.Context()); err != nil || calls != 1 {
		t.Fatal("completed operation replayed HTTP", err, calls)
	}
	if f.native.loginCalls.Load() != 0 {
		t.Fatal("offline plugin test touched WeChat")
	}
}

func TestLiveLocalMusicAdminHealthSessionContract(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/health" || r.Header.Get("X-Wechat-Agent-Account") != accountDirectory("test") {
			t.Error("administrator health escaped its fixed read")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ready":true,"bridge_version":13}`))
	}))
	defer server.Close()
	f, _, store := localMusicPluginFixture(t, server.URL)
	controller := namedSpeechLiveController(t, store)
	f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
	valid := true
	authority := &speechAdminTestAuthority{Account: "test", Session: "synthetic-admin-session", RequestID: "synthetic-admin-request", Check: func(ctx context.Context) error {
		if !valid {
			return errors.New("synthetic session expired")
		}
		return ctx.Err()
	}}
	ctx := context.WithValue(t.Context(), speechAdminTestContextKey{}, authority)
	access := &permittedTools{manager: f.manager}
	if health := access.probeYuE(ctx); !health.Ready || health.BridgeVersion != 13 || calls != 1 {
		t.Fatal("session-bound admin health failed", health, calls)
	}
	valid = false
	if _, _, err := access.yueRequest(ctx, "GET", "/health", nil, ""); err == nil || calls != 1 {
		t.Fatal("revoked session made another bridge read", err, calls)
	}
	if f.native.loginCalls.Load() != 0 {
		t.Fatal("admin health touched native WeChat")
	}
}

func TestLocalMusicProviderErrorsRequireObservedProof(t *testing.T) {
	_, access, _ := localMusicPluginFixture(t, "http://127.0.0.1:8765")
	p, err := access.newLocalMusicPluginPolicy(t.Context(), "POST", "/jobs", map[string]string{"lyrics": "synthetic"}, "original-operation")
	if err != nil {
		t.Fatal(err)
	}
	j, cap := localMusicDurableJob(t, p)
	p.post = func(context.Context, string, string, []byte, string, string, string) ([]byte, string, int, error) {
		return []byte(`{"error":"sampling_invalid_value","token":"synthetic-echoed-secret","audio_base64":"synthetic-echoed-audio"}`), "text/plain", 400, nil
	}
	raw, err := p.Service(t.Context(), j, cap, "wechat/music_bridge", p.arguments)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("synthetic-echoed")) || bytes.Contains(raw, []byte("audio_base64")) || bytes.Contains(raw, []byte("token")) {
		t.Fatal("nonessential private provider fields crossed stdio", string(raw))
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil {
		t.Fatal("invalid observation")
	}
	if _, err = p.Validate(t.Context(), j, cap, raw); err == nil {
		t.Fatal("provider error lost its classification")
	}
	result["normalized_error"] = map[string]string{"kind": "input", "code": "yue_native_parameters_invalid", "provider_code": "sampling_invalid_value", "message": "YuE原生参数校验失败：sampling_invalid_value；请查询music_sampling_options后调整"}
	output, _ := json.Marshal(result)
	if _, err = p.Validate(t.Context(), j, cap, output); err != nil {
		t.Fatal("observed provider error rejected", err)
	}
	for _, mutation := range [][2]string{{"sampling_invalid_value", "invented_code"}, {"yue_native_parameters_invalid", "rvc_service_not_ready"}, {"YuE原生参数校验失败：", "任意未经校验的诊断："}} {
		bad := bytes.Replace(output, []byte(mutation[0]), []byte(mutation[1]), 1)
		if _, err = p.Validate(t.Context(), j, cap, bad); err == nil {
			t.Fatal("forged normalized error accepted", mutation)
		}
	}
}

func TestLocalMusicAdministratorRecoveryGrantCannotExpandJobOrSource(t *testing.T) {
	f, access, _ := localMusicPluginFixture(t, "http://127.0.0.1:8765")
	native := &localMusicRecoveryNative{fakeNative: f.native, message: linuxnative.Message{ID: "original-request", ThreadID: "filehelper", SenderID: "synthetic-owner", Direction: "incoming", Body: "唱个生日歌", CreatedAt: time.Now().Format(time.RFC3339Nano)}}
	f.manager.native = native
	access.trace = companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "synthetic-owner", TraceID: "original-request"}
	task := agentTask{ID: "adoption-task", Account: "test", Thread: "filehelper", Sender: "synthetic-owner", OriginTrace: "original-request", RecoveryMessageID: "original-request", RequestedText: native.message.Body}
	ctx := withLocalMusicRecovery(t.Context(), task, "prepared-job")
	p, err := access.newLocalMusicPluginPolicy(ctx, "GET", "/jobs/prepared-job", nil, "")
	if err != nil {
		t.Fatal("explicit administrator read grant rejected", err)
	}
	for _, request := range [][2]string{{"GET", "/jobs/another-job"}, {"GET", "/jobs/prepared-job/audio"}, {"POST", "/jobs/prepared-job/resume"}, {"POST", "/jobs"}} {
		if _, err = access.newLocalMusicPluginPolicy(ctx, request[0], request[1], nil, ""); err == nil {
			t.Fatal("administrator inspection expanded authority", request)
		}
	}
	j, cap := localMusicDurableJob(t, p)
	calls := 0
	p.post = func(context.Context, string, string, []byte, string, string, string) ([]byte, string, int, error) {
		calls++
		return []byte(`{"id":"prepared-job"}`), "application/json", 200, nil
	}
	native.message.Body = "已更正的原始消息"
	if _, err = p.Service(ctx, j, cap, "wechat/music_bridge", p.arguments); err == nil || calls != 0 {
		t.Fatal("changed original source retained recovery authority", err, calls)
	}
}

func TestLiveLocalMusicPackageClassifiesProviderErrorsWithoutReplay(t *testing.T) {
	for _, test := range []struct {
		name         string
		status       int
		detail, code string
	}{
		{"account", 409, "music_account_scope_mismatch", "yue_account_scope_mismatch"},
		{"auth", 401, "unauthorized", "yue_bridge_auth_failed"},
		{"backend", 503, "backend_busy", "yue_backend_unavailable"},
		{"missing", 404, "not_found", "yue_job_not_found"},
		{"resume", 409, "yue_recovery_prompt_missing_explicit_restart_required", ""},
		{"sampling", 400, "sampling_invalid_value", ""},
		{"native-field", 400, "sampling_outside_native_spec:render.steps", ""},
		{"rvc", 503, "rvc_model_unavailable", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, access, store := localMusicPluginFixture(t, "http://127.0.0.1:8765")
			controller := namedSpeechLiveController(t, store)
			f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
			p, err := access.newLocalMusicPluginPolicy(t.Context(), "POST", "/jobs", map[string]string{"lyrics": "synthetic"}, "original-error-operation")
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			p.post = func(context.Context, string, string, []byte, string, string, string) ([]byte, string, int, error) {
				calls++
				body, _ := json.Marshal(map[string]string{"error": test.detail})
				return body, "text/plain", test.status, nil
			}
			for attempt := 0; attempt < 2; attempt++ {
				_, _, status, err := p.invoke(t.Context())
				if err == nil || status != test.status {
					t.Fatal("provider failure was not surfaced", status, err)
				}
				if test.code != "" {
					_, code := safeToolFailure(err)
					if code != test.code {
						t.Fatal("service UX code changed", code, err)
					}
				} else {
					want := test.detail
					if test.name == "resume" {
						want = "restart_missing=true"
					}
					if !strings.Contains(err.Error(), want) {
						t.Fatal("specific provider detail lost", err)
					}
				}
			}
			if calls != 1 {
				t.Fatal("provider rejection replayed HTTP", calls)
			}
		})
	}
}

func TestLiveLocalMusicLoopbackBridgeEndToEnd(t *testing.T) {
	calls := map[string]int{}
	audio := syntheticNamedSpeechWAV()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.Method+" "+r.URL.Path]++
		if r.Header.Get("Authorization") != "Bearer synthetic-local-private-token" || r.Header.Get("X-Wechat-Agent-Account") != accountDirectory("test") {
			t.Error("private host account/credential missing")
		}
		if r.Method == "POST" && r.Header.Get("Idempotency-Key") == "" {
			t.Error("mutation lost original identity")
		}
		w.Header().Set("X-Wechat-Agent-Account", accountDirectory("test"))
		if r.URL.Path == "/jobs/loopback-job/audio" {
			w.Header().Set("Content-Type", "audio/wav")
			w.Write(audio)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/health" {
			w.Write([]byte(`{"ready":true,"bridge_version":13}`))
			return
		}
		w.Write([]byte(`{"id":"loopback-job","state":"queued","engine":"rvc"}`))
	}))
	defer server.Close()
	f, access, store := localMusicPluginFixture(t, server.URL)
	controller := namedSpeechLiveController(t, store)
	f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for attempt := 0; attempt < 2; attempt++ {
		data, _, err := access.yueRequest(ctx, "GET", "/health", nil, "")
		if err != nil || !bytes.Contains(data, []byte(`"ready":true`)) {
			t.Fatal("health through installed plugin failed", err)
		}
	}
	if _, _, err := access.yueRequest(ctx, "POST", "/rvc-jobs", map[string]string{"reference_base64": "synthetic-audio"}, "loopback-original-operation"); err != nil {
		t.Fatal("submit through installed plugin failed", err)
	}
	task := agentTask{ID: "loopback-task", Account: "test", Thread: "filehelper", Sender: access.trace.Sender, State: "waiting", MusicJob: "loopback-job", OriginTrace: access.trace.TraceID, Constraints: access.taskConstraints("music", "", "", "")}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	access.trace.TaskID = task.ID
	if _, _, err := access.yueRequest(ctx, "GET", "/jobs/loopback-job", nil, ""); err != nil {
		t.Fatal("status through installed plugin failed", err)
	}
	data, mime, err := access.yueRequest(ctx, "GET", "/jobs/loopback-job/audio", nil, "")
	if err != nil || mime != "audio/wav" || !bytes.Equal(data, audio) {
		t.Fatal("private audio through installed plugin failed", mime, err)
	}
	for _, action := range []string{"resume", "cancel"} {
		if _, _, err := access.yueRequest(ctx, "POST", "/jobs/loopback-job/"+action, map[string]bool{}, ""); err != nil {
			t.Fatal(action, "through installed plugin failed", err)
		}
	}
	if calls["GET /health"] != 2 || calls["POST /rvc-jobs"] != 1 || calls["GET /jobs/loopback-job"] != 1 || calls["GET /jobs/loopback-job/audio"] != 1 || calls["POST /jobs/loopback-job/resume"] != 1 || calls["POST /jobs/loopback-job/cancel"] != 1 {
		t.Fatal("end-to-end requests repeated or stale", calls)
	}
	if f.native.loginCalls.Load() != 0 {
		t.Fatal("loopback contract touched real WeChat")
	}
}
