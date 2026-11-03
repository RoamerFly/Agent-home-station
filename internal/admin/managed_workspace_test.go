package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestManagedContextCannotBeSpoofedAcrossAccountsOrConversations(t *testing.T) {
	f, p := trustedAgent(t)
	installProjectPluginTestRuntime(t, p)
	owner := p.executionContext()
	cfg, e := p.hostConfig()
	if e != nil || cfg.Isolation == nil {
		t.Fatal(e)
	}
	p.trace.Thread = "source@chatroom"
	p.trace.Sender = "member"
	f.manager.config.GroupRules = append(f.manager.config.GroupRules, GroupRule{ID: p.trace.Thread, Mode: "mention"})
	member, e := p.hostConfig()
	if e != nil {
		t.Fatal(e)
	}
	if len(member.Roots) != 1 || member.Roots[0] == owner.Private {
		t.Fatal("member mounted account")
	}
	b, _ := json.Marshal(map[string]string{"path": filepath.Join(owner.Private, "secret.md"), "content": "forbidden"})
	if _, e = p.Call(context.Background(), "server_write", b); e == nil {
		t.Fatal("member wrote account file")
	}
	if _, e = p.Call(context.Background(), "workspace_read", json.RawMessage(`{"global":true,"name":"MEMORY.md"}`)); e == nil {
		t.Fatal("global notes leaked")
	}
	if _, e = p.Call(context.Background(), "agent_global_search", json.RawMessage(`{}`)); e == nil {
		t.Fatal("global memory leaked")
	}
}
func TestGlobalSearchIncludesOriginalTimeAndAccountBoundary(t *testing.T) {
	f, p := trustedAgent(t)
	c := f.manager.agent.config()
	c.CrossConversation = true
	c.PrivilegedThreads = []string{"*"}
	if e := f.manager.agent.save(c); e != nil {
		t.Fatal(e)
	}
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.config.GroupRules = append(f.manager.config.GroupRules, GroupRule{ID: "source@chatroom", Mode: "mention"})
	at := time.Now().Add(-time.Hour).UTC()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "source@chatroom", Sender: "member", TraceID: "original-file-msg"}
	if e := f.manager.memory.Observe(func() companion.Input { in.Text = "synthetic document"; return in }(), f.manager.config.Companion, at); e != nil {
		t.Fatal(e)
	}
	result, e := p.Call(context.Background(), "agent_global_search", json.RawMessage(`{"query":"synthetic"}`))
	if e != nil || !strings.Contains(result, "original-file-msg") || !strings.Contains(result, at.Format("2006-01-02")) {
		t.Fatal(result, e)
	}
	p.trace.Account = "another-account"
	if _, e = p.Call(context.Background(), "agent_global_search", json.RawMessage(`{}`)); e == nil {
		t.Fatal("cross account")
	}
}
func TestCrossMediaQueueBindsDestinationAndRetainsAllVoiceParts(t *testing.T) {
	f, p, n := forwardFixture(t)
	f.manager.config.Companion.Permissions.SendVoice = true
	original := assetOriginal{VoiceComplete: true, Data: []byte("synthetic-audio"), MIME: "audio/wav", Filename: "test-song.wav"}
	uploads := []stagedUpload{}
	for i := 0; i < 5; i++ {
		uploads = append(uploads, stagedUpload{UploadSource: linuxnative.UploadSource{ID: agentID("part", string(rune('0'+i))), Filename: "part.wav", ContentType: "audio/wav", ByteLength: 15}, Account: "test", Kind: "voice"})
	}
	asset, e := f.manager.rememberAssetFormats(p.trace, original, uploads)
	if e != nil {
		t.Fatal(e)
	}
	task := agentTask{ID: "cross-parent", Account: "test", Thread: p.trace.Thread, Sender: p.trace.Sender, State: "running", Next: time.Now()}
	if e = f.manager.agent.task(task); e != nil {
		t.Fatal(e)
	}
	p.trace.TaskID = task.ID
	raw, _ := json.Marshal(map[string]string{"asset_id": asset.ID, "source_thread_id": p.trace.Thread, "target_thread_id": "source@chatroom", "kind": "voice", "request_key": "cross-voice"})
	result, e := p.Call(context.Background(), "wechat_send_media", raw)
	if e != nil {
		t.Fatal(result, e)
	}
	seeds := f.manager.takeMediaSeeds()
	if len(seeds) != 5 || n.submits.Load() != 0 {
		t.Fatal("not durable complete preparation", len(seeds), n.submits.Load(), result)
	}
	for _, seed := range seeds {
		if seed.SourceID == "" || seed.Key == "" || seed.Input.Thread != "source@chatroom" || seed.OriginThread != "wxid_owner" {
			t.Fatal("wrong route", seed)
		}
	}
	if _, e = p.Call(context.Background(), "wechat_send_media", raw); e != nil || len(f.manager.takeMediaSeeds()) != 5 {
		t.Fatal("duplicate queue", e)
	}
	// A crash before queue handoff must recover the original batch identities.
	f.manager.mediaSeeds = nil
	if e = os.Remove(filepath.Join(f.manager.dir, "media-recovery.json")); e != nil {
		t.Fatal(e)
	}
	if e = f.manager.restoreCrossMedia(); e != nil || len(f.manager.takeMediaSeeds()) != 5 {
		t.Fatal("journal did not restore queue", e)
	}
	pending, _ := f.manager.agent.lookupTask(task.ID, "test", p.trace.Thread)
	pending.State = "completed"
	if e = f.manager.agent.task(pending); e != nil {
		t.Fatal(e)
	}
	f.manager.agent.retireSettled()
	if len(f.manager.agent.active("test", p.trace.Thread)) != 1 {
		t.Fatal("task retired before cross delivery")
	}
	for index, seed := range seeds {
		status := linuxnative.ToolStatus{ID: agentID("job", seed.Key), State: "accepted", ThreadID: seed.Input.Thread, SourceID: seed.SourceID, ServerID: fmt.Sprint(index + 100)}
		if e = f.manager.settleCrossMediaPart(seed.Trace, seed.Key, status); e != nil {
			t.Fatal(e)
		}
		if index == 0 {
			f.manager.mediaSeeds = nil
			os.Remove(filepath.Join(f.manager.dir, "media-recovery.json"))
			if e = f.manager.restoreCrossMedia(); e != nil || len(f.manager.takeMediaSeeds()) != 4 {
				t.Fatal("accepted part was restored", e)
			}
		}
	}
	if len(f.manager.takeMediaSeeds()) != 0 || len(f.manager.agent.active("test", p.trace.Thread)) != 0 {
		t.Fatal("accepted batch remained pending")
	}
	p.trace.Sender = "member"
	if _, e = p.Call(context.Background(), "wechat_send_media", raw); e == nil {
		t.Fatal("member forwarding")
	}
}
func TestStructuredReceivedFileCachesOriginalAndExcludesCredentialMetadata(t *testing.T) {
	f, _, n := forwardFixture(t)
	sub := 6
	n.message.Body = ""
	n.message.AppMessageType = &sub
	n.message.Attachments = []linuxnative.Attachment{{Kind: "file", Filename: "notes.txt", State: "identified"}}
	text, e := (mediaUnderstanding{manager: f.manager}).Analyze(context.Background(), n.message)
	if e != nil || !strings.Contains(text, "asset_id") || strings.Contains(text, "分享卡片") {
		t.Fatal(text, e)
	}
	assets, e := f.manager.loadAssets()
	if e != nil || len(assets) != 1 {
		t.Fatal(assets, e)
	}
	if _, e = os.Stat(f.manager.assetPath(assets[0].Hash)); e != nil {
		t.Fatal(e)
	}
	firstID := n.message.ID
	n.message.ID = "second-identical-file-message"
	if _, e = (mediaUnderstanding{manager: f.manager}).Analyze(context.Background(), n.message); e != nil {
		t.Fatal(e)
	}
	assets, e = f.manager.loadAssets()
	if e != nil || len(assets) != 1 {
		t.Fatal("identical file duplicated", e)
	}
	seen := map[string]bool{}
	for _, request := range assets[0].Requests {
		seen[request.TraceID] = true
	}
	if !seen[firstID] || !seen[n.message.ID] {
		t.Fatal("repeat file lost message identity", seen)
	}
}

func TestOwnerGlobalExecutionScopeIsExplicitAndNotInherited(t *testing.T) {
	f, p := trustedAgent(t)
	c := f.manager.agent.config()
	c.CrossConversation = true
	if e := f.manager.agent.save(c); e != nil {
		t.Fatal(e)
	}
	local, e := p.scopedHostConfig(json.RawMessage(`{}`))
	if e != nil {
		t.Fatal(e)
	}
	x := p.executionContext()
	for _, root := range local.Roots {
		if root == x.Private {
			t.Fatal("implicit global mount")
		}
	}
	global, e := p.scopedHostConfig(json.RawMessage(`{"global":true}`))
	if e != nil || global.Roots[0] != x.Private {
		t.Fatal(global, e)
	}
	again, _ := p.scopedHostConfig(json.RawMessage(`{}`))
	if again.Roots[0] != x.Conversation {
		t.Fatal("scope leaked")
	}
	p.trace.Sender = "member"
	if _, e = p.scopedHostConfig(json.RawMessage(`{"global":true}`)); e == nil {
		t.Fatal("member global scope")
	}
}

func TestOwnerCannotWritePublicControlDirectories(t *testing.T) {
	f, p := trustedAgent(t)
	installProjectPluginTestRuntime(t, p)
	c := f.manager.agent.config()
	c.CrossConversation = true
	if e := f.manager.agent.save(c); e != nil {
		t.Fatal(e)
	}
	x := p.executionContext()
	local, e := p.scopedHostConfig(json.RawMessage(`{}`))
	if e != nil {
		t.Fatal(e)
	}
	projects := filepath.Join(x.Public, "projects")
	if len(local.Roots) != 2 || local.Roots[0] != x.Conversation || local.Roots[1] != projects {
		t.Fatalf("owner roots leaked public control parent: %#v", local.Roots)
	}
	for _, directory := range []string{"models", "runtimes", "manifests"} {
		path := filepath.Join(x.Public, directory, "owner-must-not-write.txt")
		raw, _ := json.Marshal(map[string]string{"path": path, "content": "forbidden"})
		// Privileged calls intentionally return a durable needs_review result on
		// admission failure, rather than a Go error that could invite replay.
		if _, e = p.Call(context.Background(), "server_write", raw); e != nil {
			t.Fatalf("unexpected durable admission error for %q: %v", directory, e)
		}
		if _, e = os.Stat(path); !os.IsNotExist(e) {
			t.Fatalf("owner wrote public control directory %q: %v", directory, e)
		}
	}
	global, e := p.scopedHostConfig(json.RawMessage(`{"global":true}`))
	if e != nil || len(global.Roots) != 2 || global.Roots[0] != x.Private || global.Roots[1] != projects {
		t.Fatalf("global roots widened beyond private/projects: %#v, %v", global.Roots, e)
	}
}
