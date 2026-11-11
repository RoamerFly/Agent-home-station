package station

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/pluginhost"
)

func memoryEvidenceTools(t *testing.T, f *localHomeFixture, request string) (*localToolSet, companion.Input) {
	t.Helper()
	h := f.handler.chat
	store, in, err := h.conversation()
	if err != nil {
		t.Fatal(err)
	}
	text := "查询抖音银河记忆"
	in.Text, in.OriginalText, in.TraceID = text, &text, request
	in.MessageTime = time.Now().UTC().Format(time.RFC3339Nano)
	in.Source = &companion.SourceRef{MessageID: request}
	if err := store.Observe(in, localConversationConfig(""), time.Now()); err != nil {
		t.Fatal(err)
	}
	in.Source, _ = store.SourceForInput(in)
	tools := h.tools(store, in)
	tools.run, err = store.OpenLocalToolRun(t.Context(), in, true)
	if err != nil {
		t.Fatal(err)
	}
	return tools, in
}

func memoryEvidenceFixture(t *testing.T) (*localHomeFixture, *localDouyinWebLogin, localDouyinWebRecord, localDouyinMemorySource) {
	t.Helper()
	f, m := newLocalDouyinWebFixture(t)
	view := enableSyntheticDouyinWebRead(t, f)
	for _, change := range []map[string]any{
		{"action": "set_works", "allow_works": true},
		{"action": "set_memory", "memory_scope": douyinMemoryWorks, "allow_memory": true},
	} {
		change["revision"] = view["revision"]
		code, out := f.request("POST", localDouyinWebRoute, change, true)
		if code != 200 {
			t.Fatal(code, out)
		}
		view = out
	}
	tools, _ := memoryEvidenceTools(t, f, "setup-source")
	record, err := tools.douyinMemoryState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	source, ok := douyinMemorySource(record, tools.access.fishID, tools.access.ownerID, douyinMemoryWorks)
	if !ok {
		t.Fatal("missing synthetic source")
	}
	if err := m.captureMemory(t.Context(), record, douyinMemoryWorks, "synthetic-creator", []localDouyinMemoryRecord{{
		ID: "synthetic-work", Work: "7300000000000000000", Label: "银河摘要", Text: "银河正文只在主动读取时返回", At: time.Now().UTC().Format(localDouyinMemoryTime),
	}}); err != nil {
		t.Fatal(err)
	}
	return f, m, record, source
}

func memoryEvidenceSearch(t *testing.T, tools *localToolSet, source localDouyinMemorySource) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"app_id": "douyin", "action": "search", "source_id": source.Scope, "query": "银河"})
	result, err := tools.Call(t.Context(), localWechatMemoryTool, raw)
	if err != nil || !strings.Contains(result, "银河") || len(tools.douyinMemoryProofs) != 1 {
		t.Fatal(result, err)
	}
}

func TestMemoryEvidenceDurableRecoveryAndReplay(t *testing.T) {
	f, _, _, source := memoryEvidenceFixture(t)
	for _, request := range []string{"memory-completed", "memory-interrupted"} {
		calls := 0
		f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
			return localModelFunc(func(context.Context, string, []map[string]string) (string, error) {
				calls++
				if calls == 1 {
					return localToolCall(localWechatMemoryTool, map[string]any{"app_id": "douyin", "action": "search", "source_id": source.Scope, "query": "银河"}), nil
				}
				if request == "memory-interrupted" {
					return "", errors.New("synthetic interruption")
				}
				return `{"action":"reply","messages":["已找到合成银河记录。"]}`, nil
			}), nil
		}
		want := 200
		if request == "memory-interrupted" {
			want = 502
		}
		if code, out := localChatSend(f, request, "查询抖音银河记忆"); code != want {
			t.Fatal(code, out)
		}
	}
	tools := projectFollowupFixtureTools(t, f, "memory-completed")
	if len(tools.run.Operations) != 1 {
		t.Fatal(tools.run.Operations)
	}
	op := tools.run.Operations[0]
	if strings.Contains(op.Result, "银河") || strings.Contains(op.Result, "reference") {
		t.Fatal("checkpoint retained query/body/reference", op)
	}
	decoded, err := decodeMemoryEvidence(op, tools.access.fishID, tools.access.ownerID)
	if err != nil || decoded.Douyin.Source != source || decoded.Douyin.Source.binding == "" {
		t.Fatal("private binding lost in JSON", decoded, err)
	}
	if _, err := decodeMemoryEvidence(op, tools.access.fishID, "another-owner"); err == nil {
		t.Fatal("owner substitution passed")
	}
	if err := tools.restoreMemoryEvidence(t.Context()); err != nil || len(tools.douyinMemoryRefs) != 0 || len(tools.douyinMemoryPages) != 0 {
		t.Fatal("restored private bodies or references", err)
	}
	// Reconstruct the host and its SQLite journals, without opening a platform.
	f.server.Close()
	if err := f.handler.Close(); err != nil {
		t.Fatal(err)
	}
	registry, err := apphost.New(douyinapp.New(filepath.Join(f.dir, "apps", "douyin")).Registration())
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewLocalHomeHandlerWithApps(f.dir, f.root, "synthetic-local-password", "", registry)
	if err != nil {
		t.Fatal(err)
	}
	f = newLocalAppObservationFixtureWithHandler(t, handler, f.dir, f.root)
	f.login()
	tools = projectFollowupFixtureTools(t, f, "memory-completed")
	if err := tools.restoreMemoryEvidence(t.Context()); err != nil || len(tools.douyinMemoryProofs) != 1 {
		t.Fatal("reopened proof unavailable", err)
	}
	if code, out := localChatSend(f, "memory-completed", "查询抖音银河记忆"); code != 200 || out["reused"] != true {
		t.Fatal(code, out)
	}
	// A legitimate retry loads versions, not an old result or a stale ref.
	modelCalls := 0
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
			modelCalls++
			if strings.Contains(localModelHistory(turns), "银河正文只在") {
				t.Fatal("recovery preloaded source body")
			}
			return `{"action":"reply","messages":["原资料需按需重新读取。"]}`, nil
		}), nil
	}
	if code, out := localChatSend(f, "memory-interrupted", "查询抖音银河记忆"); code != 200 || modelCalls != 1 {
		t.Fatal(code, out, modelCalls)
	}
	// Explicit revocation invalidates replay and proof restoration, before a model call.
	_, view := f.request("GET", localDouyinWebRoute, nil, false)
	if code, out := f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_memory", "memory_scope": douyinMemoryWorks, "allow_memory": false, "revision": view["revision"]}, true); code != 200 {
		t.Fatal(code, out)
	}
	if code, _ := localChatSend(f, "memory-completed", "查询抖音银河记忆"); code != 409 || modelCalls != 1 {
		t.Fatal("revoked replay called model", code, modelCalls)
	}
	if err := tools.restoreMemoryEvidence(t.Context()); err == nil {
		t.Fatal("revoked proof restored")
	}
}

func TestMemoryEvidenceArtifactsKeepParentAndPrivateAudience(t *testing.T) {
	f, m, record, source := memoryEvidenceFixture(t)
	tools, in := memoryEvidenceTools(t, f, "artifact-memory-source")
	memoryEvidenceSearch(t, tools, source)
	grant, err := tools.access.resolve()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(grant.Directory, "works", "private.txt")
	if err := os.WriteFile(path, []byte("synthetic private work"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := f.handler.home.artifacts.publishFile(t.Context(), localArtifactInput{Key: in.TraceID, homeFile: true}, in.Source, tools.access, grant, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	parentID := a.ID
	childTools, childIn := memoryEvidenceTools(t, f, "derived-memory-child")
	path = filepath.Join(grant.Directory, "works", "child.txt")
	if err := os.WriteFile(path, []byte("synthetic derived private work"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err = f.handler.home.artifacts.publishFile(t.Context(), localArtifactInput{Key: childIn.TraceID, homeFile: true, parent: &localArtifactLink{ID: a.ID, SHA256: a.SHA256}}, childIn.Source, childTools.access, grant, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if body := localArtifactDownload(t, f, a.ID); string(body) != "synthetic derived private work" {
		t.Fatal("valid owner access failed")
	}
	if _, _, _, err := f.handler.chat.publicationArtifactAccess(t.Context(), localPublicationArtifact(a)); err == nil || !strings.Contains(err.Error(), "私下") {
		t.Fatal("derived memory publicly disclosed", err)
	}
	origin, _ := json.Marshal(localPublicationOrigin{Request: in.TraceID, Source: in.Source})
	job := pluginhost.Job{Context: pluginhost.Invocation{Account: in.Account, Thread: in.Sender, Requester: in.Sender, Authorization: localCheckpointKey(grant), SourceVersions: map[string]int{in.Source.EventID: in.Source.Revision}}}
	if err := f.handler.chat.validatePublication(t.Context(), job, apphost.PublicationDraft{Origin: origin}); err == nil || !strings.Contains(err.Error(), "私下") {
		t.Fatal("text-only memory publicly disclosed", err)
	}
	// A content correction changes the source generation without changing grants.
	if err := m.captureMemory(t.Context(), record, douyinMemoryWorks, "synthetic-creator", []localDouyinMemoryRecord{{ID: "synthetic-work", Work: "7300000000000000000", Label: "更正银河摘要", Text: "corrected synthetic source", At: time.Now().UTC().Format(localDouyinMemoryTime)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.handler.home.artifacts.read(a.ID); err == nil {
		t.Fatal("derived artifact shed parent source version")
	}
	if _, err := f.handler.home.artifacts.readRecord(parentID); err != nil {
		t.Fatal("correction destroyed immutable local work", err)
	}
	if code, _ := f.request("GET", "/api/local/artifacts/"+a.ID+"/download", nil, false); code != 404 {
		t.Fatal("stale download allowed", code)
	}
}

func TestMemoryEvidenceFollowupStopsBeforeReadingResult(t *testing.T) {
	f, m, record, source := memoryEvidenceFixture(t)
	h := f.handler.chat
	h.turn.Lock()
	defer h.turn.Unlock()
	tools, in := memoryEvidenceTools(t, f, "memory-task")
	memoryEvidenceSearch(t, tools, source)
	grant, err := tools.access.resolve()
	if err != nil {
		t.Fatal(err)
	}
	job, err := tools.store.ScheduleLocalTaskFollowup(t.Context(), in, *in.Source, "synthetic-process-not-read", grant.Revision, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	job, err = tools.store.UpdateLocalTaskFollowup(t.Context(), in, job.ID, job.Revision, "ready", time.Now().UTC(), "")
	if err != nil {
		t.Fatal(err)
	}
	job, err = tools.store.UpdateLocalTaskFollowup(t.Context(), in, job.ID, job.Revision, "generating", time.Now().UTC(), "")
	if err != nil || h.checkLocalTaskContinuation(t.Context(), tools.store, in, job.ID) != nil {
		t.Fatal("valid continuation refused", err)
	}
	if err := m.captureMemory(t.Context(), record, douyinMemoryWorks, "different-synthetic-creator", nil); err != nil {
		t.Fatal(err)
	}
	if err := h.checkLocalTaskContinuation(t.Context(), tools.store, in, job.ID); err == nil {
		t.Fatal("account replacement allowed continuation")
	}
	// Test the worker admission independently of a generating/crash state.
	waiting, err := tools.store.ScheduleLocalTaskFollowup(t.Context(), in, *in.Source, "another-synthetic-process", grant.Revision, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	modelCalls := 0
	h.modelFactory = func() (companion.LanguageModel, error) { modelCalls++; return nil, errors.New("must not call model") }
	h.followups.advance(t.Context(), tools.store, in, waiting)
	latest, err := tools.store.GetLocalTaskFollowup(t.Context(), in, waiting.ID)
	if err != nil || latest.State != "cancelled" || latest.Reason != "memory_source_changed" || modelCalls != 0 {
		t.Fatal(latest, err, modelCalls)
	}
}

func TestMemoryEvidenceWechatRechecksDurableSource(t *testing.T) {
	x := newSurfShareFixture(t)
	const thread = "target@chatroom"
	x.request("PUT", x.path+"/policy", map[string]any{"reply_threads": []string{thread}, "history_threads": []string{thread}}, 200)
	x.request("PUT", x.path+"/memory-grants", map[string]any{"revision": 0, "request_key": "source-proof-grant", "station_reads": []string{thread}}, 200)
	memory, err := companion.NewStore(filepath.Join(x.worker.dir, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	literal := "旅行合成原话"
	original := companion.Input{Channel: "wechat", Account: "one", Thread: thread, Sender: "synthetic-owner", Mode: "direct", TraceID: "synthetic-source-event", Text: literal, OriginalText: &literal, MessageTime: time.Now().UTC().Format(time.RFC3339Nano)}
	original.Source = &companion.SourceRef{MessageID: original.TraceID, OccurredAt: original.MessageTime}
	config := companion.Defaults()
	config.Enabled, config.Permissions.Remember = true, true
	if err := memory.Observe(original, config, time.Now()); err != nil {
		t.Fatal(err)
	}
	source, ok := memory.SourceForInput(original)
	if !ok {
		t.Fatal("missing synthetic source")
	}
	if _, err := memory.SaveMemory(t.Context(), companion.MemoryEntry{Scope: companion.ScopeID("wechat", "one", thread), Subject: "synthetic-owner", Kind: "fact", Manual: true, Summary: "旅行合成记忆", Detail: "旅行按需合成详情", Sources: []companion.MemorySource{{EventID: source.EventID, Quote: literal}}}, 0); err != nil {
		t.Fatal(err)
	}
	tools, in := memoryEvidenceTools(t, x.f, "wechat-proof-request")
	raw, _ := json.Marshal(map[string]any{"action": "search", "connection_id": x.id, "account_id": "one", "thread_id": thread, "query": "旅行"})
	if result, err := tools.Call(t.Context(), localWechatMemoryTool, raw); err != nil || !strings.Contains(result, "旅行") {
		t.Fatal(result, err)
	}
	tools = projectFollowupFixtureTools(t, x.f, in.TraceID)
	if err := tools.restoreMemoryEvidence(t.Context()); err != nil || len(tools.wechatMemoryProofs) != 1 || len(tools.wechatMemoryRefs) != 0 {
		t.Fatal("wechat restored stale body/ref", err)
	}
	if err := x.f.handler.chat.checkRequestMemoryEvidence(t.Context(), tools.store, in); err != nil {
		t.Fatal("current durable wechat evidence rejected", err)
	}
	if err := memory.Clear(companion.ScopeID("wechat", "one", thread)); err != nil {
		t.Fatal(err)
	}
	if err := x.f.handler.chat.checkRequestMemoryEvidence(t.Context(), tools.store, in); err == nil {
		t.Fatal("forget barrier did not invalidate durable proof")
	}
	if len(x.sends) != 0 {
		t.Fatal("proof validation sent real/synthetic messages")
	}
}

func TestMemoryEvidenceUnrelatedChatAndMalformedCheckpoint(t *testing.T) {
	f := localChatFixture(t)
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(context.Context, string, []map[string]string) (string, error) {
			return `{"action":"reply","messages":["合成正常本机回答。"]}`, nil
		}), nil
	}
	for range 2 {
		if code, out := localChatSend(f, "ordinary-request", "普通本机聊天"); code != 200 {
			t.Fatal(code, out)
		}
	}
	tools, in := memoryEvidenceTools(t, f, "malformed-proof")
	op := companion.LocalToolCheckpoint{Key: "incomplete-memory-evidence", Tool: localWechatMemoryTool, Grant: memoryEvidenceGrant(in.Account, in.Sender), Target: "source_versions"}
	if err := tools.run.Reserve(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	tools.run.Operations = append(tools.run.Operations, op)
	if err := tools.restoreMemoryEvidence(t.Context()); err == nil {
		t.Fatal("incomplete proof silently discarded")
	}
	proof := localMemoryEvidence{Version: 1, Wechat: &localWechatMemoryProof{Source: localWechatMemorySource{Connection: "synthetic", Account: "one", Thread: "synthetic-group"}, Revision: 1, Grant: time.Now().UTC(), Instance: "synthetic-instance", Epoch: "synthetic-epoch"}}
	raw, _ := json.Marshal(proof)
	op.Key, op.Result = memoryEvidenceKey(proof), string(raw)
	if decoded, err := decodeMemoryEvidence(op, in.Account, in.Sender); err != nil || *decoded.Wechat != *proof.Wechat {
		t.Fatal("wechat proof roundtrip", decoded, err)
	}
	op.Result = strings.Replace(op.Result, "synthetic-epoch", "tampered-epoch", 1)
	if _, err := decodeMemoryEvidence(op, in.Account, in.Sender); err == nil {
		t.Fatal("tampered version accepted")
	}
}
