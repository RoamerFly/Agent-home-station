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
)

func acceptedMemoryLinkParent(t *testing.T, f *localHomeFixture, source localDouyinMemorySource, id string) (*localToolSet, companion.Input) {
	t.Helper()
	tools, in := memoryEvidenceTools(t, f, id)
	memoryEvidenceSearch(t, tools, source)
	in.RequestScopedReply = true
	if err := tools.store.AcceptLocalConversation(t.Context(), in, []string{"PRIVATE_LINK_PARENT_RESULT"}, localConversationConfig(""), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	return tools, in
}

func TestMemoryLinkRestartArtifactAndMidModelRevocation(t *testing.T) {
	f, _, _, source := memoryEvidenceFixture(t)
	acceptedMemoryLinkParent(t, f, source, "restart-link-parent")
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(context.Context, string, []map[string]string) (string, error) {
			return `{"action":"reply","messages":["已关联，资料仍按需读取。"]}`, nil
		}), nil
	}
	if code, out := linkedChatSend(f, "restart-link-child", "restart-link-parent"); code != 200 {
		t.Fatal(code, out)
	}
	// A linked request with no new search still carries private provenance.
	tools := projectFollowupFixtureTools(t, f, "restart-link-child")
	grant, err := tools.access.resolve()
	if err != nil {
		t.Fatal(err)
	}
	origin, ok := tools.artifactSource(grant)
	if !ok {
		t.Fatal("missing child source")
	}
	path := filepath.Join(grant.Directory, "works", "linked-private.txt")
	if err := os.WriteFile(path, []byte("synthetic linked private work"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := f.handler.home.artifacts.publishFile(t.Context(), localArtifactInput{Key: tools.traceID, homeFile: true}, origin, tools.access, grant, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.handler.chat.publicationArtifactAccess(t.Context(), localPublicationArtifact(a)); err == nil {
		t.Fatal("link-only artifact lost private audience")
	}
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
	tools = projectFollowupFixtureTools(t, f, "restart-link-child")
	if err := tools.restoreMemoryEvidence(t.Context()); err != nil || !tools.memoryLinked || len(tools.douyinMemoryRefs) != 0 {
		t.Fatal("restart lost link or restored body/ref", err)
	}
	if code, out := linkedChatSend(f, "restart-link-child", "restart-link-parent"); code != 200 || out["reused"] != true {
		t.Fatal(code, out)
	}
	if body := localArtifactDownload(t, f, a.ID); string(body) != "synthetic linked private work" {
		t.Fatal("valid linked artifact unavailable")
	}
	calls := 0
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(context.Context, string, []map[string]string) (string, error) {
			calls++
			_, view := f.request("GET", localDouyinWebRoute, nil, false)
			if code, out := f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_memory", "memory_scope": douyinMemoryWorks, "allow_memory": false, "revision": view["revision"]}, true); code != 200 {
				t.Error(code, out)
			}
			return `{"action":"reply","messages":["SHOULD_NOT_BE_SAVED"]}`, nil
		}), nil
	}
	if code, _ := linkedChatSend(f, "revoked-during-linked-model", "restart-link-child"); code != 409 || calls != 1 {
		t.Fatal("mid-model revoke escaped final fence", code, calls)
	}
	if _, err := f.handler.home.artifacts.read(a.ID); err == nil {
		t.Fatal("revoked link-only artifact remained readable")
	}
	store, in, err := f.handler.chat.conversation()
	if err != nil {
		t.Fatal(err)
	}
	prior, err := store.LocalConversationRequest(t.Context(), in, "revoked-during-linked-model")
	if err != nil || prior == nil || prior.Completed {
		t.Fatal("revoked reply was saved", prior, err)
	}
}

func linkedChatSend(f *localHomeFixture, request, parent string) (int, map[string]any) {
	return f.request("POST", "/api/local/chat", map[string]any{"request_id": request, "text": "继续核对这份资料", "continue_request_id": parent}, true)
}

func TestMemoryLinkNormalEngineExplicitReadAndDefaultIsolation(t *testing.T) {
	f, _, _, source := memoryEvidenceFixture(t)
	original, _ := acceptedMemoryLinkParent(t, f, source, "linked-parent")
	var oldRef string
	for key := range original.douyinMemoryRefs {
		oldRef = key
		break
	}
	calls := 0
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, prompt string, turns []map[string]string) (string, error) {
			calls++
			history := localModelHistory(turns)
			if calls == 1 && (!strings.Contains(prompt, "明确关联") || strings.Contains(history, "PRIVATE_LINK_PARENT_RESULT") || strings.Contains(history, "银河正文只在")) {
				t.Fatal("linked initial input was preloaded", history)
			}
			switch calls {
			case 1:
				return localToolCall(localWechatMemoryTool, map[string]any{"action": "linked_sources"}), nil
			case 2:
				var packet struct {
					Result string `json:"result"`
				}
				var directory struct {
					Loaded *bool `json:"content_loaded"`
				}
				for _, turn := range turns {
					if json.Unmarshal([]byte(turn["content"]), &packet) == nil && packet.Result != "" {
						_ = json.Unmarshal([]byte(packet.Result), &directory)
					}
				}
				if !strings.Contains(history, source.Scope) || directory.Loaded == nil || *directory.Loaded {
					t.Fatal("missing source-only directory", history)
				}
				return localToolCall(localWechatMemoryTool, map[string]any{"action": "search", "app_id": "douyin", "source_id": source.Scope, "query": "银河"}), nil
			default:
				return `{"action":"reply","messages":["PRIVATE_LINK_CHILD_RESULT"]}`, nil
			}
		}), nil
	}
	if code, out := linkedChatSend(f, "linked-child", "linked-parent"); code != 200 || calls != 3 {
		t.Fatal(code, out, calls)
	}
	tools := projectFollowupFixtureTools(t, f, "linked-child")
	if err := tools.restoreMemoryEvidence(t.Context()); err != nil || !tools.memoryLinked || len(tools.douyinMemoryRefs) != 0 {
		t.Fatal("host link did not survive reconstruction", err)
	}
	args, _ := json.Marshal(map[string]any{"action": "read", "app_id": "douyin", "reference": oldRef})
	if _, err := tools.Call(t.Context(), localWechatMemoryTool, args); err == nil {
		t.Fatal("temporary reference crossed requests")
	}
	if code, out := localChatSend(f, "linked-child", "继续核对这份资料"); code != 200 || out["reused"] != true || calls != 3 {
		t.Fatal("retry lost stable link", code, out)
	}
	if code, _ := linkedChatSend(f, "linked-child", "different-parent"); code != 409 {
		t.Fatal("rebound accepted request", code)
	}
	if code, page := f.request("GET", "/api/local/chat", nil, false); code != 200 || len(page["memory_link_requests"].([]any)) != 2 {
		t.Fatal("source-only history candidates", code, page)
	}
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, prompt string, turns []map[string]string) (string, error) {
			if strings.Contains(prompt, "明确关联") || strings.Contains(localModelHistory(turns), "PRIVATE_LINK_") || strings.Contains(localModelHistory(turns), source.Scope) {
				t.Fatal("ordinary new turn inherited link")
			}
			return `{"action":"reply","messages":["普通新回合。"]}`, nil
		}), nil
	}
	if code, out := localChatSend(f, "ordinary-after-link", "开始新话题"); code != 200 {
		t.Fatal(code, out)
	}
}

func TestMemoryLinkRevocationAndTransitiveOriginChange(t *testing.T) {
	f, m, record, source := memoryEvidenceFixture(t)
	_, parentIn := acceptedMemoryLinkParent(t, f, source, "chain-parent")
	modelCalls := 0
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		modelCalls++
		return localModelFunc(func(context.Context, string, []map[string]string) (string, error) {
			return `{"action":"reply","messages":["合成关联结果。"]}`, nil
		}), nil
	}
	for _, pair := range [][2]string{{"chain-child", "chain-parent"}, {"chain-grandchild", "chain-child"}} {
		if code, out := linkedChatSend(f, pair[0], pair[1]); code != 200 {
			t.Fatal(code, out)
		}
	}
	tools := projectFollowupFixtureTools(t, f, "chain-grandchild")
	if err := tools.restoreMemoryEvidence(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Canonical original revision changes after admission. No descendants can
	// use a cached link, even while their own user events remain unchanged.
	store := tools.store
	parentIn.Text = "已更正的原请求"
	parentIn.OriginalText = &parentIn.Text
	if err := store.Observe(parentIn, localConversationConfig(""), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Call(t.Context(), "memory_search", json.RawMessage(`{}`)); err == nil {
		t.Fatal("transitive origin change did not fence tool")
	}
	if code, _ := linkedChatSend(f, "chain-grandchild", "chain-child"); code != 409 || modelCalls != 2 {
		t.Fatal("stale link replay called model", code, modelCalls)
	}
	// A different original verifies account-generation invalidation separately.
	acceptedMemoryLinkParent(t, f, source, "revoke-parent")
	if err := m.captureMemory(t.Context(), record, douyinMemoryWorks, "replacement-creator", nil); err != nil {
		t.Fatal(err)
	}
	if code, _ := linkedChatSend(f, "after-replacement", "revoke-parent"); code != 409 || modelCalls != 2 {
		t.Fatal("replacement account admitted original evidence", code, modelCalls)
	}
}

func TestMemoryLinkAdmissionSnapshotAndInterruptedRetry(t *testing.T) {
	f, _, _, source := memoryEvidenceFixture(t)
	original, _ := acceptedMemoryLinkParent(t, f, source, "snapshot-parent")
	modelCalls := 0
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		modelCalls++
		return nil, errors.New("synthetic model not ready")
	}
	if code, _ := linkedChatSend(f, "snapshot-child", "snapshot-parent"); code != 503 || modelCalls != 1 {
		t.Fatal(code, modelCalls)
	}
	tools := projectFollowupFixtureTools(t, f, "snapshot-child")
	if err := tools.restoreMemoryEvidence(t.Context()); err != nil {
		t.Fatal(err)
	}
	// An original request later obtains another source. The child retains only
	// the versions the owner actually selected at admission.
	original.wechatMemoryProofs = append(original.wechatMemoryProofs, localWechatMemoryProof{Source: localWechatMemorySource{Connection: "later-connection", Account: "later-account", Thread: "later-thread"}, Revision: 1, Grant: time.Now().UTC(), Instance: "later-instance", Epoch: "later-epoch"})
	if err := original.persistMemoryEvidence(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := tools.restoreMemoryEvidence(t.Context()); err != nil || len(tools.wechatMemoryProofs) != 0 {
		t.Fatal("link widened to later source", err)
	}
	if code, _ := linkedChatSend(f, "snapshot-child", "different-parent"); code != 409 || modelCalls != 1 {
		t.Fatal("interrupted request rebound", code, modelCalls)
	}
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		modelCalls++
		return localModelFunc(func(context.Context, string, []map[string]string) (string, error) {
			return `{"action":"reply","messages":["已从原关联恢复。"]}`, nil
		}), nil
	}
	if code, out := localChatSend(f, "snapshot-child", "继续核对这份资料"); code != 200 || modelCalls != 2 {
		t.Fatal("retry without field discarded saved link", code, out, modelCalls)
	}
	for _, parent := range []string{"missing-parent", "setup-source"} {
		if code, _ := linkedChatSend(f, "bad-"+parent, parent); code != 409 {
			t.Fatal("untrusted/uncompleted parent admitted", code)
		}
	}
	if code, _ := f.request("POST", "/api/local/chat", map[string]any{"request_id": "no-csrf", "text": "继续", "continue_request_id": "snapshot-child"}, false); code != 403 {
		t.Fatal("link bypassed owner CSRF", code)
	}
	plain, _ := memoryEvidenceTools(t, f, "tool-forgery")
	if _, err := plain.Call(t.Context(), localWechatMemoryTool, json.RawMessage(`{"action":"linked_sources","request_id":"snapshot-child"}`)); err == nil {
		t.Fatal("model forged owner link")
	}
}
