package station

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/comfyui"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/stationbrowser"
)

func noticeDraftArtifact(t *testing.T, f *localHomeFixture, job string) localArtifact {
	t.Helper()
	_, page := f.request("GET", "/api/local/publications?app_id=synthetic-publisher", nil, false)
	items := page["tasks"].(map[string]any)["items"].([]any)
	for _, raw := range items {
		row := raw.(map[string]any)
		if row["job_id"] == job {
			id := row["artifact_ids"].([]any)[0].(string)
			a, err := f.handler.home.artifacts.read(id)
			if err != nil {
				t.Fatal(err)
			}
			return a
		}
	}
	t.Fatal("missing original draft artifact")
	return localArtifact{}
}

func draftNoticeArtifactLater(t *testing.T, f *localHomeFixture, artifact string, trace string) (string, error) {
	t.Helper()
	tool := noticeToolSet(t, f, trace, true)
	raw, _ := json.Marshal(map[string]any{"action": "draft", "app_id": "synthetic-publisher", "connection_id": "publish", "title": "后续请求的作品", "artifact_ids": []string{artifact}})
	value, err := tool.Call(t.Context(), localPublicationToolName, raw)
	if err != nil {
		return "", err
	}
	var view apphost.PublicationView
	if err := json.Unmarshal([]byte(value), &view); err != nil {
		t.Fatal(err)
	}
	if view.JobID == "" {
		t.Fatal("missing later task")
	}
	return view.JobID, nil
}

func TestArtifactNotificationSourcesFollowCrossRequestAndParent(t *testing.T) {
	for _, parent := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "derived-parent"}[parent], func(t *testing.T) {
			p := &noticeDerivedPublisher{}
			f, job := noticeDerivedDraft(t, false, p)
			a := noticeDraftArtifact(t, f, job)
			originalID := a.ID
			if parent {
				tool := noticeToolSet(t, f, "derive-new-request", true)
				grant, err := tool.access.resolve()
				if err != nil {
					t.Fatal(err)
				}
				source, ok := tool.artifactSource(grant)
				if !ok {
					t.Fatal("missing child source")
				}
				path := filepath.Join(grant.Directory, "works", "derived.txt")
				if err := os.WriteFile(path, []byte("合成通知启发的作品"), 0600); err != nil {
					t.Fatal(err)
				}
				a, err = f.handler.home.artifacts.publishFile(t.Context(), localArtifactInput{Key: tool.traceID, homeFile: true, parent: &localArtifactLink{ID: a.ID, SHA256: a.SHA256}}, source, tool.access, grant, path, nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := f.handler.chat.checkArtifactNoticeSources(t.Context(), a); err != nil {
				t.Fatal("valid source rejected", err)
			}
			later, err := draftNoticeArtifactLater(t, f, a.ID, "later-before-revoke")
			if err != nil {
				t.Fatal("valid cross-request reuse rejected", err)
			}
			setSyntheticNoticeAgent(t, f, false)
			// Local originals, immutable snapshots and IDs remain accessible.
			if data := localArtifactDownload(t, f, a.ID); string(data) != "合成通知启发的作品" {
				t.Fatal("revocation destroyed local work")
			}
			if original, err := f.handler.home.artifacts.read(originalID); err != nil || original.ID != originalID {
				t.Fatal("original ID changed", err)
			}
			if err := f.handler.chat.checkArtifactNoticeSources(t.Context(), a); err == nil {
				t.Fatal("old artifact lost notice proof")
			}
			if _, err := draftNoticeArtifactLater(t, f, a.ID, "later-after-revoke"); err == nil {
				t.Fatal("new request laundered revoked source")
			}
			code, out := f.request("POST", "/api/local/publications", map[string]any{"action": "submit", "job_id": later}, true)
			if p.submits != 0 || out["state"] == "published" {
				t.Fatalf("later draft submitted after revocation: %d %v", code, out)
			}
			setSyntheticNoticeAgent(t, f, true)
			if _, err := draftNoticeArtifactLater(t, f, a.ID, "later-after-renew"); err == nil {
				t.Fatal("new grant retroactively authorized artifact")
			}
		})
	}
}

func TestArtifactNotificationSourcesPersistAcrossStationRestart(t *testing.T) {
	p := &noticeDerivedPublisher{}
	f, job := noticeDerivedDraft(t, false, p)
	a := noticeDraftArtifact(t, f, job)
	f.server.Close()
	if err := f.handler.Close(); err != nil {
		t.Fatal(err)
	}
	registry, err := apphost.New(douyinapp.New(filepath.Join(f.dir, "apps", "douyin")).Registration(), apphost.Registration{Application: apphost.Application{ID: "synthetic-publisher", Name: "合成发布应用", InstallationID: "synthetic-publisher-v1", Registration: "synthetic"}, Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewLocalHomeHandlerWithApps(f.dir, f.root, "synthetic-local-password", "", registry)
	if err != nil {
		t.Fatal(err)
	}
	f = newLocalAppObservationFixtureWithHandler(t, handler, f.dir, f.root)
	f.login()
	f.handler.home.douyinWeb.available = func() bool { return true }
	if _, err := draftNoticeArtifactLater(t, f, a.ID, "reuse-after-restart"); err != nil {
		t.Fatal("durable proof lost", err)
	}
	setSyntheticNoticeAgent(t, f, false)
	if _, err := draftNoticeArtifactLater(t, f, a.ID, "revoked-after-restart"); err == nil {
		t.Fatal("restart discarded durable provenance")
	}
}

func TestUnknownNoticeReadCannotAuthorizeNewArtifactDisclosure(t *testing.T) {
	f, m, _ := syntheticNoticeFixture(t, true)
	m.readNotice = func(context.Context, string, stationbrowser.DouyinNoticeRequest, func() error) (stationbrowser.DouyinNoticePage, error) {
		return stationbrowser.DouyinNoticePage{}, context.DeadlineExceeded
	}
	tool := noticeToolSet(t, f, "unknown-source-artifact", true)
	if _, err := tool.Call(t.Context(), localInboxToolName, inboxArgs(nil)); err == nil {
		t.Fatal("expected uncertain read")
	}
	grant, err := tool.access.resolve()
	if err != nil {
		t.Fatal(err)
	}
	source, ok := tool.artifactSource(grant)
	if !ok {
		t.Fatal("missing source")
	}
	path := filepath.Join(grant.Directory, "works", "uncertain.txt")
	if err := os.WriteFile(path, []byte("local work retained"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := f.handler.home.artifacts.publishFile(t.Context(), localArtifactInput{Key: tool.traceID, homeFile: true}, source, tool.access, grant, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.handler.chat.checkArtifactNoticeSources(t.Context(), a); err == nil {
		t.Fatal("unknown read invented a source grant")
	}
	// Earlier versions journaled successful reads without a grant version.
	// Such records must not silently inherit the account's present authority.
	if err := tool.run.RecordResult(t.Context(), tool.run.Operations[0].Key, `{"state":"read","body_retained":false}`); err != nil {
		t.Fatal(err)
	}
	if err := f.handler.chat.checkArtifactNoticeSources(t.Context(), a); err == nil {
		t.Fatal("legacy read invented a source grant")
	}
	if string(localArtifactDownload(t, f, a.ID)) != "local work retained" {
		t.Fatal("local work not retained")
	}
}

func TestProjectArtifactRetainsOriginalNotificationSource(t *testing.T) {
	f, m, _ := syntheticNoticeFixture(t, true)
	root := t.TempDir()
	writeLocalComfyChatWorkflow(t, root)
	identity, err := f.handler.home.identity.Read()
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 48, 24))); err != nil {
		t.Fatal(err)
	}
	backend := &syntheticLocalComfy{root: root, fish: identity.FishID, image: comfyui.Image{Data: data.Bytes(), SHA256: localComfyDigest(data.Bytes()), Width: 48, Height: 24}}
	setUpLocalComfyChat(t, f, backend)
	m.readNotice = func(_ context.Context, _ string, in stationbrowser.DouyinNoticeRequest, check func() error) (stationbrowser.DouyinNoticePage, error) {
		return stationbrowser.DouyinNoticePage{Account: in.Account, PagesRead: 1, RenderedText: "合成创作灵感"}, check()
	}
	calls := 0
	job := ""
	jobPattern := regexp.MustCompile(`job_id[^a-f0-9]+([a-f0-9]{64})`)
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return &syntheticObservationModel{call: func(_ context.Context, _ string, turns []map[string]string, _ []mcptools.Tool) (string, error) {
			calls++
			switch calls {
			case 1:
				return localToolCall("discover_tools", map[string]any{"query": localInboxToolName}), nil
			case 2:
				return localToolCall(localInboxToolName, map[string]any{"action": "read", "app_id": "douyin", "allow_mark_read": true}), nil
			case 3:
				return localToolCall(localComfyListTool, map[string]any{}), nil
			case 4:
				return localToolCall(localComfyStartTool, map[string]any{"workflow_id": "smoke", "inputs": map[string]int{"width": 48}}), nil
			}
			matches := jobPattern.FindStringSubmatch(localModelHistory(turns))
			if len(matches) != 2 {
				t.Fatal("missing project task receipt")
			}
			job = matches[1]
			return `{"action":"reply","messages":["合成制作任务已登记。"]}`, nil
		}}, nil
	}
	if code, out := localChatSend(f, "notice-project-source", "根据获准的通知制作图片，允许标记已读"); code != 200 || out["status"] != "completed" || job == "" {
		t.Fatalf("project chat: %d %v", code, out)
	}
	// Completion happens in another request, but the artifact retains the
	// original creation source rather than this status query's source.
	backend.state = comfyui.Completed
	ids := requestLocalComfyStatus(t, f, "notice-project-result", job, "status")
	if len(ids) != 2 {
		t.Fatalf("project outputs: %v", ids)
	}
	a, err := f.handler.home.artifacts.read(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if a.Source == nil || a.Source.MessageID != "notice-project-source" || a.Request != job || a.ProjectTool != localComfyPackage {
		t.Fatalf("project lost original source: %+v", a)
	}
	if err := f.handler.chat.checkArtifactNoticeSources(t.Context(), a); err != nil {
		t.Fatal("valid project source rejected", err)
	}
	setSyntheticNoticeAgent(t, f, false)
	if err := f.handler.chat.checkArtifactNoticeSources(t.Context(), a); err == nil {
		t.Fatal("project result laundered revoked notice source")
	}
	if !bytes.Equal(localArtifactDownload(t, f, a.ID), data.Bytes()) {
		t.Fatal("revocation removed local project output")
	}
}
