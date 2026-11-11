//go:build windows

package station

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/mediadecode"
	"wechat-bot/internal/stationbrowser"
	"wechat-bot/internal/stationruntime"
)

type syntheticCreatorTransport struct {
	f                                *localHomeFixture
	media                            []byte
	uploads, submits, queries, finds int
	lost, revoke                     bool
	profile, task, manifest          string
}

func (s *syntheticCreatorTransport) bind(m *localDouyinWebLogin) {
	m.submitWork = s.submit
	m.queryWork = func(_ context.Context, profile, id string, authorize func() error) (stationbrowser.CreatorPublicationResult, error) {
		if err := authorize(); err != nil {
			return stationbrowser.CreatorPublicationResult{}, err
		}
		if profile != s.profile || id != "7391234567890123456" {
			return stationbrowser.CreatorPublicationResult{}, errors.New("synthetic query identity changed")
		}
		s.queries++
		return stationbrowser.CreatorPublicationResult{Stage: "published", RemoteID: id, Submitted: true, Published: true}, nil
	}
	m.findWork = func(_ context.Context, profile string, in stationbrowser.CreatorPublicationQuery, authorize func() error) (stationbrowser.CreatorPublicationResult, error) {
		if err := authorize(); err != nil {
			return stationbrowser.CreatorPublicationResult{}, err
		}
		if profile != s.profile || in.Title != "合成视频标题" || in.Body != "合成视频正文" || in.Visibility != "private" || in.CreatedAt == "" {
			return stationbrowser.CreatorPublicationResult{}, errors.New("synthetic recovery query changed original")
		}
		s.finds++
		return stationbrowser.CreatorPublicationResult{Stage: "published", RemoteID: "7391234567890123456", Submitted: true, Published: true}, nil
	}
}

func (s *syntheticCreatorTransport) submit(ctx context.Context, profile string, in stationbrowser.CreatorPublicationRequest, authorize func() error) (stationbrowser.CreatorPublicationResult, error) {
	if err := authorize(); err != nil {
		return stationbrowser.CreatorPublicationResult{}, err
	}
	if s.profile != "" && profile != s.profile {
		return stationbrowser.CreatorPublicationResult{}, errors.New("synthetic upload profile changed")
	}
	s.profile = profile
	if !localDouyinPublicationHex(in.TaskID) || !localDouyinPublicationHex(in.ManifestSHA256) || in.Title != "合成视频标题" || in.Body != "合成视频正文" || in.Visibility != "private" || in.Submitted || in.MediaPath == "" {
		return stationbrowser.CreatorPublicationResult{}, errors.New("synthetic upload lost immutable draft")
	}
	s.task, s.manifest = in.TaskID, in.ManifestSHA256
	data, err := os.ReadFile(in.MediaPath)
	if err != nil || !bytes.Equal(data, s.media) || !strings.Contains(in.MediaPath, "publication-staging") {
		return stationbrowser.CreatorPublicationResult{}, errors.New("synthetic upload staged different bytes")
	}
	s.uploads++
	if err := in.Progress(ctx, "uploading_video"); err != nil {
		return stationbrowser.CreatorPublicationResult{}, err
	}
	if s.revoke {
		_, view := s.f.request("GET", localDouyinWebRoute, nil, false)
		if code, _ := s.f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_publish", "revision": view["revision"], "allow_publish": false}, true); code != 200 {
			return stationbrowser.CreatorPublicationResult{}, errors.New("synthetic revoke failed")
		}
	}
	if err := in.BeforeSubmit(ctx); err != nil {
		return stationbrowser.CreatorPublicationResult{}, err
	}
	// Prove the irreversible checkpoint exists before the fake external send.
	identity, err := s.f.handler.home.identity.Read()
	if err != nil {
		return stationbrowser.CreatorPublicationResult{}, err
	}
	host, err := s.f.handler.chat.projectTools.forFish(identity.FishID)
	if err != nil {
		return stationbrowser.CreatorPublicationResult{}, err
	}
	state, err := host.store.ReadHostState(ctx, "application-douyin", in.TaskID)
	var checkpoint localDouyinPublicationCheckpoint
	if err != nil || json.Unmarshal(state.Records["progress"], &checkpoint) != nil || !checkpoint.Submitted || checkpoint.TaskID != in.TaskID || checkpoint.ManifestSHA256 != in.ManifestSHA256 || checkpoint.Bytes != int64(len(data)) || checkpoint.MediaSHA256 != sha256Hex(data) {
		return stationbrowser.CreatorPublicationResult{}, errors.New("synthetic submit preceded durable matching checkpoint")
	}
	if err := authorize(); err != nil {
		return stationbrowser.CreatorPublicationResult{}, err
	}
	s.submits++
	if s.lost {
		return stationbrowser.CreatorPublicationResult{Submitted: true}, stationbrowser.ErrCreatorPublicationUnknown
	}
	return stationbrowser.CreatorPublicationResult{Stage: "accepted", RemoteID: "7391234567890123456", Submitted: true}, nil
}

// The scripted model drives the normal chat and publication host. The
// opt-in variant also generates the MP4 through the real isolated runner.
// Both variants replace the external creator transport with a local fixture.
func creatorPublicationChat(t *testing.T, lost, revoke, actualProcess bool) (*localHomeFixture, *syntheticCreatorTransport, string, string) {
	t.Helper()
	f, _, _, _ := webPublicationFixture(t)
	m := f.handler.home.douyinWeb
	_, home := f.request("GET", "/api/local/home", nil, false)
	path := filepath.Join(home["directory"].(string), "works", "synthetic-video.mp4")
	var media []byte
	var ffmpeg string
	if actualProcess {
		cfg, err := stationruntime.LoadWindowsRunner("", f.dir, f.root, os.Getenv("AHS_WINDOWS_RUNTIME_CONFIG"))
		if err != nil || cfg == nil {
			t.Fatal("test runtime unavailable", err)
		}
		f.handler.chat.windows = cfg
		ffmpeg = filepath.Join(filepath.Dir(filepath.Dir(cfg.Pythonw)), "windows-media", "ffmpeg.exe")
		if _, err := os.Stat(ffmpeg); err != nil {
			t.Fatal("packaged FFmpeg unavailable", err)
		}
	} else {
		media = syntheticDouyinAudioMedia(t, true)
		if err := os.WriteFile(path, media, 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := &syntheticCreatorTransport{f: f, media: media, lost: lost, revoke: revoke}
	s.bind(m)
	var artifact, job string
	calls := 0
	processID := ""
	started := false
	generated := !actualProcess
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return &syntheticObservationModel{call: func(_ context.Context, _ string, turns []map[string]string, _ []mcptools.Tool) (string, error) {
			if !generated {
				if !started {
					started = true
					return localToolCall(localProcessTool, map[string]any{"action": "start", "path": home["directory"], "argv": []string{ffmpeg, "-v", "error", "-nostdin", "-y", "-f", "lavfi", "-i", "color=c=blue:s=64x48:r=5:d=3", "-f", "lavfi", "-i", "sine=f=800:r=16000:d=3", "-c:v", "mpeg4", "-threads", "1", "-c:a", "aac", path}, "timeout_seconds": 60}), nil
				}
				var process agenthost.Process
				for i := len(turns) - 1; i >= 0; i-- {
					var envelope struct{ Tool, Result string }
					if json.Unmarshal([]byte(turns[i]["content"]), &envelope) == nil && envelope.Tool == localProcessTool && json.Unmarshal([]byte(envelope.Result), &process) == nil && process.ID != "" {
						break
					}
				}
				if process.ID == "" || processID != "" && processID != process.ID {
					t.Fatal("video process did not preserve its task identity")
				}
				processID = process.ID
				if process.State == "running" {
					time.Sleep(150 * time.Millisecond)
					return localToolCall(localProcessTool, map[string]any{"action": "status", "id": processID}), nil
				}
				if process.State != "completed" {
					t.Fatalf("isolated FFmpeg failed: %+v", process)
				}
				actual, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				metadata, err := mediadecode.ProbeMetadata(t.Context(), path)
				if err != nil || metadata.Width != 64 || metadata.Height != 48 || metadata.Seconds < 2.9 {
					t.Fatalf("generated video not verified: %+v %v", metadata, err)
				}
				frames, _, err := mediadecode.ExtractFramesSized(t.Context(), path, t.TempDir(), 0, 1, 1, 64)
				if err != nil || len(frames) != 1 {
					t.Fatalf("generated video frame not readable: %v", err)
				}
				wav, seconds, err := mediadecode.ExtractAudioRange(t.Context(), path, filepath.Join(t.TempDir(), "sample.wav"), 0, 1)
				if err != nil || seconds < 0.9 || len(wav) < 1000 {
					t.Fatalf("generated video audio not readable: %v", err)
				}
				s.media = actual
				generated = true
			}
			calls++
			history := localModelHistory(turns)
			switch calls {
			case 1:
				return localToolCall(localArtifactPublishTool, map[string]any{"path": "works/synthetic-video.mp4"}), nil
			case 2:
				return localToolCall("discover_tools", map[string]any{"query": localPublicationToolName}), nil
			case 3:
				ids := localPublicationWorkIDs(history)
				if len(ids) != 1 {
					t.Fatal("media artifact absent")
				}
				artifact = ids[0]
				return localToolCall(localPublicationToolName, map[string]any{"action": "draft", "app_id": "douyin", "connection_id": "web-page-text", "title": "合成视频标题", "body": "合成视频正文", "artifact_ids": []string{artifact}, "platform_options": map[string]any{"visibility": "private"}}), nil
			case 4:
				job = lastPublicationTask(turns)
				if job == "" {
					t.Fatal("ordinary-web publication task absent")
				}
				return localToolCall(localPublicationToolName, map[string]any{"action": "submit", "job_id": job}), nil
			}
			if !lost && !revoke && !strings.Contains(history, "7391234567890123456") {
				t.Fatal("accepted work ID absent")
			}
			if strings.Contains(history, "publication-staging") || strings.Contains(history, "web-login") {
				t.Fatal("host-only staging or profile escaped into model")
			}
			return `{"action":"reply","messages":["已登记合成发布流程，按原任务核对。"]}`, nil
		}}, nil
	}
	text := "将已制作的合成视频以仅自己可见发布，并保留原任务回执"
	if actualProcess {
		text = "制作一段带测试音的合成视频，以仅自己可见发布，并保留原任务回执"
	}
	if code, out := localChatSend(f, "creator-video-chat", text); code != 200 || out["status"] != "completed" || job == "" {
		t.Fatalf("normal publication chat: %d %v", code, out)
	}
	if code, _ := localChatSend(f, "creator-video-chat", text); code != 200 || calls != 5 {
		t.Fatal("request replay invoked model or upload")
	}
	return f, s, job, artifact
}

func TestLiveDouyinPublicationFromWindowsVideoProcessThroughChat(t *testing.T) {
	if os.Getenv("AHS_WINDOWS_RUNTIME_CONFIG") == "" {
		t.Skip("opt-in isolated FFmpeg production with synthetic publication transport")
	}
	f, s, job, artifact := creatorPublicationChat(t, false, false, true)
	if s.uploads != 1 || s.submits != 1 || len(s.media) < 100 {
		t.Fatal("normal process-to-publication chain incomplete")
	}
	if !bytes.Equal(localArtifactDownload(t, f, artifact), s.media) {
		t.Fatal("video artifact differs from isolated output")
	}
	code, out := f.request("POST", "/api/local/publications", map[string]any{"action": "status", "job_id": job}, true)
	if code != 200 || out["state"] != "published" || s.queries != 1 {
		t.Fatalf("synthetic platform receipt: %d %v", code, out)
	}
}

func lastPublicationTask(turns []map[string]string) string {
	for i := len(turns) - 1; i >= 0; i-- {
		var envelope struct{ Tool, Result string }
		var view apphost.PublicationView
		if json.Unmarshal([]byte(turns[i]["content"]), &envelope) == nil && envelope.Tool == localPublicationToolName && json.Unmarshal([]byte(envelope.Result), &view) == nil && view.JobID != "" {
			return view.JobID
		}
	}
	return ""
}

func TestDouyinPublicationVideoNormalChatStagesAndReconciles(t *testing.T) {
	f, s, job, artifact := creatorPublicationChat(t, false, false, false)
	if s.uploads != 1 || s.submits != 1 {
		t.Fatalf("upload counts: upload=%d submit=%d", s.uploads, s.submits)
	}
	_, view := f.request("GET", localDouyinWebRoute, nil, false)
	if code, _ := f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_publish", "revision": view["revision"], "allow_publish": false}, true); code != 200 {
		t.Fatal(code)
	}
	code, out := f.request("POST", "/api/local/publications", map[string]any{"action": "status", "job_id": job}, true)
	if code != 200 || out["state"] != "published" || s.queries != 1 || s.submits != 1 || s.uploads != 1 {
		t.Fatalf("receipt after revoke: %d %v queries=%d", code, out, s.queries)
	}
	if !bytes.Equal(localArtifactDownload(t, f, artifact), s.media) {
		t.Fatal("immutable video changed")
	}
	_, _ = f.request("POST", "/api/local/publications", map[string]any{"action": "resume", "job_id": job}, true)
	if s.uploads != 1 || s.submits != 1 {
		t.Fatal("completed task uploaded twice")
	}
}

func TestDouyinPublicationVideoLostReceiptRecoversAfterRestart(t *testing.T) {
	f, s, job, _ := creatorPublicationChat(t, true, false, false)
	if s.submits != 1 || s.uploads != 1 {
		t.Fatal("original submission missing")
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
	f.handler.home.douyinWeb.available = func() bool { return true }
	s.f = f
	s.bind(f.handler.home.douyinWeb)
	// Remove only this test's private staged files. Read-only receipt recovery
	// must not need the staged upload or recreate it after the service restarts.
	files, err := filepath.Glob(filepath.Join(f.dir, "apps", "douyin", "publication-staging", "*.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	code, out := f.request("POST", "/api/local/publications", map[string]any{"action": "status", "job_id": job}, true)
	if code != 200 || out["state"] != "published" || out["remote_id"] != "7391234567890123456" || s.finds != 1 || s.uploads != 1 || s.submits != 1 {
		t.Fatalf("lost receipt: %d %v find=%d upload=%d submit=%d", code, out, s.finds, s.uploads, s.submits)
	}
	files, err = filepath.Glob(filepath.Join(f.dir, "apps", "douyin", "publication-staging", "*.mp4"))
	if err != nil || len(files) != 0 {
		t.Fatal("recovery recreated upload", files, err)
	}
}

func TestDouyinPublicationVideoRevocationBeforeSubmitBlocksExternalWrite(t *testing.T) {
	f, s, job, _ := creatorPublicationChat(t, false, true, false)
	if s.uploads != 1 || s.submits != 0 {
		t.Fatalf("revoked write counts: upload=%d submit=%d", s.uploads, s.submits)
	}
	_, _ = f.request("POST", "/api/local/publications", map[string]any{"action": "resume", "job_id": job}, true)
	if s.submits != 0 || s.uploads != 1 {
		t.Fatal("revoked task resumed external effect")
	}
}
