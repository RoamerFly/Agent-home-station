//go:build windows

package station

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"wechat-bot/internal/stationbrowser"
)

// This billed, opt-in test reads the workstation's existing DeepSeek model
// settings and key through localModelStore.ClientSnapshot. It never saves to
// that store. The chat host, media and synthetic Douyin Creator transport use
// only this worktree's ignored temp directory. Point AHS_DOUYIN_LIVE_MODEL_CONFIG
// at the formal workstation data/main_setting/authority/local-model.json.
func TestLiveDouyinAgentSyntheticPublication(t *testing.T) {
	configured := os.Getenv("AHS_DOUYIN_LIVE_MODEL_CONFIG")
	if configured == "" {
		t.Skip("set AHS_DOUYIN_LIVE_MODEL_CONFIG to the formal workstation local-model.json to opt into a billed synthetic-platform test")
	}
	privateRoot, formalModels := douyinLivePrivateTempAndFormalModel(t, configured)
	runRoot := filepath.Join(privateRoot, "runs")
	if err := os.MkdirAll(runRoot, 0700); err != nil {
		t.Fatal("isolated test directory unavailable")
	}
	runRoot, err := filepath.EvalSymlinks(runRoot)
	if err != nil || !douyinLiveChildPath(privateRoot, runRoot) {
		t.Fatal("isolated test directory escaped the ignored worktree temp directory")
	}
	for _, name := range []string{"TMP", "TEMP", "TMPDIR"} {
		t.Setenv(name, runRoot)
	}
	actualTemp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil || !douyinLiveSamePath(runRoot, actualTemp) {
		t.Fatal("test temporary files are not isolated in this worktree")
	}

	f, _, _, _ := webPublicationFixture(t)
	if f.handler.chat.modelFactory != nil {
		t.Fatal("scripted model unexpectedly replaced the configured model")
	}
	_, web := f.request("GET", localDouyinWebRoute, nil, false)
	code, web := f.request("POST", localDouyinWebRoute, map[string]any{
		"action": "set_works", "revision": web["revision"], "allow_works": false,
	}, true)
	if code != 200 {
		t.Fatal("cannot disable synthetic Douyin reads")
	}
	code, web = f.request("POST", localDouyinWebRoute, map[string]any{
		"action": "set_read", "revision": web["revision"], "allow_read": false,
	}, true)
	if code != 200 || web["works_enabled"] != false || web["read_enabled"] != false || web["publish_enabled"] != true {
		t.Fatal("isolated fixture must grant publication without Douyin reads")
	}
	browserAttempts := douyinLiveBlockBrowser(f.handler.home.douyinWeb)
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatal("opted-in synthetic video generation requires ffmpeg and ffprobe")
		}
	}
	media := syntheticDouyinAudioMedia(t, true)
	_, home := f.request("GET", "/api/local/home", nil, false)
	path := filepath.Join(home["directory"].(string), "works", "synthetic-video.mp4")
	if err := os.WriteFile(path, media, 0600); err != nil {
		t.Fatal("cannot prepare isolated synthetic video")
	}
	transport := &syntheticCreatorTransport{f: f, media: media}
	transport.bind(f.handler.home.douyinWeb)

	modelView, err := formalModels.View()
	if err != nil || modelView.Provider != "deepseek" || !modelView.KeyConfigured {
		t.Fatal("formal workstation DeepSeek model is not ready")
	}
	isolatedModels := f.handler.chat.models
	f.handler.chat.models = formalModels
	t.Cleanup(func() { f.handler.chat.models = isolatedModels })

	const requestID = "live-synthetic-douyin-publication"
	const prompt = "这是隔离夹具：家目录 works/synthetic-video.mp4 已有合成视频。请自行发现并使用工作站工具，先把该文件登记成不可变成品，再在已连接的抖音网页连接 web-page-text 创建发布草稿：标题严格为“合成视频标题”，正文严格为“合成视频正文”，仅自己可见。随后提交原草稿，并按原任务编号查询发布状态，最后简短报告回执。不要打开浏览器、启动进程或调用外部 HTTP 工具。"
	code, response := localChatSend(f, requestID, prompt)
	if code != 200 || response["status"] != "completed" {
		t.Fatalf("real-model local chat did not complete: HTTP %d, status=%v, code=%v", code, response["status"], response["code"])
	}
	if browserAttempts.Load() != 0 {
		t.Fatal("model attempted a non-synthetic Douyin browser operation")
	}
	if transport.uploads != 1 || transport.submits != 1 || transport.queries < 1 {
		t.Fatalf("real model did not complete the synthetic Creator chain: uploads=%d submits=%d status_queries=%d", transport.uploads, transport.submits, transport.queries)
	}

	artifacts, err := os.ReadDir(filepath.Join(f.dir, "artifacts"))
	if err != nil || len(artifacts) != 1 || !artifactID(artifacts[0].Name(), "work-") {
		t.Fatal("real model did not register exactly one immutable video artifact")
	}
	artifact := artifacts[0].Name()
	if !bytes.Equal(localArtifactDownload(t, f, artifact), media) {
		t.Fatal("registered artifact differs from synthetic video")
	}
	store, in, err := f.handler.chat.conversation()
	if err != nil {
		t.Fatal("isolated conversation unavailable")
	}
	source, err := store.LocalRequestSource(t.Context(), in, requestID)
	if err != nil {
		t.Fatal("original local request source unavailable")
	}
	in.Source = source
	proofs, err := store.LocalToolEvidence(t.Context(), in, localArtifactPublishTool)
	if err != nil || len(proofs) != 1 || proofs[0].Result != artifact || proofs[0].Target != "works/synthetic-video.mp4" {
		t.Fatal("real model did not invoke artifact_publish for the original source")
	}
	code, listed := f.request("GET", "/api/local/publications?app_id=douyin&limit=10", nil, false)
	if code != 200 {
		t.Fatal("isolated publication ledger unavailable")
	}
	tasks, _ := listed["tasks"].(map[string]any)
	items, _ := tasks["items"].([]any)
	if len(items) != 1 {
		t.Fatal("real model did not create exactly one Douyin publication task")
	}
	job, _ := items[0].(map[string]any)
	ids, _ := job["artifact_ids"].([]any)
	if job["state"] != "published" || job["remote_id"] != "7391234567890123456" || len(ids) != 1 || ids[0] != artifact {
		t.Fatal("synthetic publication receipt does not match the registered video")
	}
	t.Log("real model chose artifact and publication tools; one synthetic Creator submit and status query; zero Douyin browser operations")
}

func douyinLivePrivateTempAndFormalModel(t *testing.T, configured string) (string, *localModelStore) {
	t.Helper()
	if !filepath.IsAbs(configured) {
		t.Fatal("live-model config must be the absolute formal workstation local-model.json path")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal("cannot resolve test package directory")
	}
	worktree, err := filepath.EvalSymlinks(filepath.Join(cwd, "..", ".."))
	if err != nil {
		t.Fatal("cannot resolve worktree root")
	}
	if filepath.Base(worktree) != "douyin" || filepath.Base(filepath.Dir(worktree)) != "agent-home-workstation-worktrees" {
		t.Fatal("live-model test must run from the douyin worktree")
	}
	if _, err := os.Stat(filepath.Join(worktree, "go.mod")); err != nil {
		t.Fatal("test must run from the station package in its worktree")
	}
	privateRoot := filepath.Join(worktree, "temp", "douyin-agent-live")
	if err := os.MkdirAll(privateRoot, 0700); err != nil {
		t.Fatal("ignored worktree temp directory unavailable")
	}
	resolvedRoot, err := filepath.EvalSymlinks(privateRoot)
	if err != nil || !douyinLiveSamePath(privateRoot, resolvedRoot) {
		t.Fatal("ignored temp directory is missing or redirects outside this worktree")
	}
	formalRoot := filepath.Join(filepath.Dir(filepath.Dir(worktree)), "agent-home-workstation")
	formalData := filepath.Join(formalRoot, "dist", "release", "data")
	formalModels := newLocalModelStore(formalData)
	expectedPath, err := filepath.EvalSymlinks(formalModels.path)
	if err != nil {
		t.Fatal("formal workstation model record is unavailable")
	}
	configPath, err := filepath.EvalSymlinks(configured)
	if err != nil || !douyinLiveSamePath(expectedPath, configPath) {
		t.Fatal("live-model config must resolve to the formal workstation's existing model record")
	}
	info, err := os.Stat(configPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 128<<10 {
		t.Fatal("formal workstation model record is unavailable or invalid")
	}
	return privateRoot, formalModels
}

func douyinLiveSamePath(a, b string) bool {
	rel, err := filepath.Rel(a, b)
	return err == nil && rel == "."
}

func douyinLiveChildPath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func douyinLiveBlockBrowser(m *localDouyinWebLogin) *atomic.Int32 {
	attempts := new(atomic.Int32)
	deny := func() error {
		attempts.Add(1)
		return errors.New("Douyin browser transport is disabled in this isolated test")
	}
	m.open = func(context.Context, string) (localDouyinLoginWindow, error) { return nil, deny() }
	m.openCreator = m.open
	m.openCreatorWorks = m.open
	m.readPage = func(context.Context, string, func() error) (stationbrowser.DouyinWebPage, error) {
		return stationbrowser.DouyinWebPage{}, deny()
	}
	m.readWorks = func(context.Context, string, stationbrowser.DouyinCreatorWorksRequest, func() error) (stationbrowser.DouyinCreatorWorksPage, error) {
		return stationbrowser.DouyinCreatorWorksPage{}, deny()
	}
	m.readImages = func(context.Context, []string, func() error) ([]stationbrowser.DouyinWebMedia, error) {
		return nil, deny()
	}
	m.readVideo = func(context.Context, string, string, func() error) (stationbrowser.DouyinWebPage, error) {
		return stationbrowser.DouyinWebPage{}, deny()
	}
	m.readComments = func(context.Context, string, stationbrowser.DouyinCommentsRequest, func() error) (stationbrowser.DouyinCommentPage, error) {
		return stationbrowser.DouyinCommentPage{}, deny()
	}
	m.probeNotice = func(context.Context, string, func() error) (string, error) { return "", deny() }
	m.readNotice = func(context.Context, string, stationbrowser.DouyinNoticeRequest, func() error) (stationbrowser.DouyinNoticePage, error) {
		return stationbrowser.DouyinNoticePage{}, deny()
	}
	m.interact = func(context.Context, string, stationbrowser.DouyinInteractionRequest, func() error) (stationbrowser.DouyinInteractionResult, error) {
		return stationbrowser.DouyinInteractionResult{}, deny()
	}
	m.sampleVideo = func(context.Context, string, string, time.Duration, func() error) (stationbrowser.PlaybackSample, error) {
		return stationbrowser.PlaybackSample{}, deny()
	}
	m.readMedia = func(context.Context, string, string, func() error) (stationbrowser.DouyinWebMedia, error) {
		return stationbrowser.DouyinWebMedia{}, deny()
	}
	return attempts
}
