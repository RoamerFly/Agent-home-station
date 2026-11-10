//go:build windows

package station

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/message"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/obsportable"
)

func TestLocalLiveInvalidEncoderLeavesTargetsUnavailable(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "station")
	if err := os.MkdirAll(filepath.Join(data, "managed-obs", "studio"), 0700); err != nil {
		t.Fatal(err)
	}
	f := newLocalHomeFixture(t, data, filepath.Join(base, "workspace"))
	f.login()
	if code, out := f.request(http.MethodPost, "/api/local/home", map[string]string{"action": "initialize"}, true); code != http.StatusOK {
		t.Fatalf("initialize: %d %#v", code, out)
	}
	if code, out := f.request(http.MethodGet, "/api/local/live", nil, false); code != http.StatusOK ||
		out["encoder"] != "invalid" || out["available"] != false {
		t.Fatalf("invalid portable runtime should not expose targets: %d %#v", code, out)
	}
}

func realOBSFixture(t *testing.T) string {
	t.Helper()
	if archive := os.Getenv("AHS_TEST_OBS_ZIP"); archive != "" {
		base := t.TempDir()
		data, workspace := filepath.Join(base, "station"), filepath.Join(base, "workspace")
		for _, path := range []string{data, workspace} {
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		if err := obsportable.Install(ctx, data, workspace, archive); err != nil {
			t.Fatalf("install isolated OBS: %v", err)
		}
		return data
	}
	data := os.Getenv("AHS_TEST_PORTABLE_OBS_FIXTURE")
	if data == "" {
		t.Skip("set AHS_TEST_OBS_ZIP to the pinned official archive or use an isolated installed fixture")
	}
	if !filepath.IsAbs(data) || filepath.Base(data) != "real-obs-fixture" {
		t.Fatal("expected a dedicated fixture directory named real-obs-fixture")
	}
	return data
}

func TestLocalLiveVerifiedEncoderDoesNotImplyAPlatformGrant(t *testing.T) {
	data := realOBSFixture(t)
	f := newLocalHomeFixture(t, data, t.TempDir())
	f.login()
	if code, out := f.request(http.MethodPost, "/api/local/home", map[string]string{"action": "initialize"}, true); code != http.StatusOK {
		t.Fatalf("initialize: %d %#v", code, out)
	}
	if code, out := f.request(http.MethodGet, "/api/local/live", nil, false); code != http.StatusOK ||
		out["encoder"] != "ready" || out["available"] != false || out["active"] != nil {
		t.Fatalf("verified local encoder must still wait for a platform grant: %d %#v", code, out)
	}
}

type localPortableOBSProvider struct {
	localLiveRouteProvider
	target *url.URL
	starts atomic.Int32
}

func (p *localPortableOBSProvider) StartLive(_ context.Context, ref apphost.LiveReference) (apphost.LiveStartResult, error) {
	p.starts.Add(1)
	return apphost.LiveStartResult{
		Receipt: apphost.LiveReceipt{Reference: ref, State: "live", RoomID: "room-1"},
		Ingest:  &apphost.LiveIngest{Destination: p.target, DialContext: (&net.Dialer{}).DialContext},
	}, nil
}

func TestLocalLivePortableOBSProgramReachesLoopbackThroughChat(t *testing.T) {
	data := realOBSFixture(t)
	program := syntheticDouyinAudioMedia(t, true)
	sink, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	received := make(chan error, 1)
	go receivePortableProgram(sink, received)

	provider := &localPortableOBSProvider{
		localLiveRouteProvider: localLiveRouteProvider{connection: apphost.Connection{
			ID: "broadcast", AccountID: "synthetic-owner", Label: "合成直播接收端",
		}},
		target: &url.URL{Scheme: "rtmp", Host: sink.Addr().String(), Path: "/fixture", Fragment: "receiver-test-key"},
	}
	registry, err := apphost.New(apphost.Registration{Application: apphost.Application{
		ID: "synthetic-live", Name: "合成平台", InstallationID: "synthetic-install", Registration: "synthetic-v1",
	}, Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	handler, err := NewLocalHomeHandlerWithApps(data, root, "synthetic-local-password", "", registry)
	if err != nil {
		t.Fatal(err)
	}
	f := newLocalAppObservationFixtureWithHandler(t, handler, data, root)
	f.login()
	if code, out := f.request(http.MethodPost, "/api/local/home", map[string]string{"action": "initialize"}, true); code != http.StatusOK {
		t.Fatalf("initialize: %d %#v", code, out)
	}
	if code, out := f.request(http.MethodGet, "/api/local/live", nil, false); code != http.StatusOK ||
		out["encoder"] != "ready" || out["available"] != true {
		t.Fatalf("authorized synthetic target unavailable: %d %#v", code, out)
	}
	_, home := f.request(http.MethodGet, "/api/local/home", nil, false)
	if err := os.WriteFile(filepath.Join(home["directory"].(string), "works", "fixture-program.mp4"), program, 0600); err != nil {
		t.Fatal(err)
	}
	var artifact string
	calls := 0
	handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return &syntheticObservationModel{call: func(_ context.Context, _ string, turns []map[string]string, _ []mcptools.Tool) (string, error) {
			calls++
			switch calls {
			case 1:
				return localToolCall(localArtifactPublishTool, map[string]any{"path": "works/fixture-program.mp4"}), nil
			case 2:
				ids := localPublicationWorkIDs(localModelHistory(turns))
				if len(ids) != 1 {
					t.Fatalf("missing program artifact: %v", ids)
				}
				artifact = ids[0]
				return localToolCall("discover_tools", map[string]any{"query": "app_live"}), nil
			case 3:
				args := localLiveTestStart()
				args["program_artifact_id"], args["program_sha256"], args["program_loop"] = artifact, sha256Hex(program), true
				return localToolCall(localLiveToolName, args), nil
			default:
				history := localModelHistory(turns)
				if !strings.Contains(history, `\"program_artifact_id\":\"`+artifact) || !strings.Contains(history, `\"state\":\"live\"`) {
					t.Fatal("chat did not receive the session and original program receipt", history)
				}
				return `{"action":"reply","messages":["合成本机节目已启动。"]}`, nil
			}
		}}, nil
	}
	const requestID, requestText = "portable-obs-fixture-chat", "把这份合成节目循环播放60秒"
	if code, out := localChatSend(f, requestID, requestText); code != http.StatusOK || out["status"] != "completed" || provider.starts.Load() != 1 {
		t.Fatalf("start from chat: %d %#v, dispatches %d", code, out, provider.starts.Load())
	}
	code, live := f.request(http.MethodGet, "/api/local/live", nil, false)
	started, _ := live["active"].(map[string]any)
	id, _ := started["id"].(string)
	if code != http.StatusOK || id == "" || started["state"] != "live" || started["room_id"] != "room-1" ||
		started["program_artifact_id"] != artifact || started["program_sha256"] != sha256Hex(program) || started["program_loop"] != true {
		t.Fatalf("owner view did not retain original chat program: %d %#v", code, live)
	}
	select {
	case err := <-received:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("OBS did not publish local H.264 video and AAC audio")
	}
	for deadline := time.Now().Add(time.Second); ; {
		code, view := f.request(http.MethodPost, "/api/local/live", map[string]string{"action": "status", "id": id}, true)
		output, _ := view["local_output"].(map[string]any)
		if code == http.StatusOK && output["state"] == "forwarding" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("local media state: %d %#v", code, view)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code, replay := localChatSend(f, requestID, requestText); code != http.StatusOK ||
		replay["status"] != "completed" || provider.starts.Load() != 1 || calls != 4 {
		t.Fatalf("replay created another broadcast: %d %#v, model calls %d", code, replay, calls)
	}
	code, stopped := f.request(http.MethodPost, "/api/local/live", map[string]string{"action": "stop", "id": id}, true)
	for deadline := time.Now().Add(3 * time.Second); code == http.StatusOK && stopped["state"] == "stopping" && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		code, stopped = f.request(http.MethodPost, "/api/local/live", map[string]string{"action": "reconcile", "id": id}, true)
	}
	if code != http.StatusOK || stopped["state"] != "ended" || stopped["local_stopped"] != true || stopped["remote_ended"] != true {
		t.Fatalf("stop: %d %#v", code, stopped)
	}
}

func receivePortableProgram(sink net.Listener, received chan<- error) {
	socket, err := sink.Accept()
	if err != nil {
		received <- err
		return
	}
	defer socket.Close()
	_ = socket.SetDeadline(time.Now().Add(30 * time.Second))
	receiver := &gortmplib.ServerConn{RW: socket}
	if err = receiver.Initialize(); err != nil {
		received <- err
		return
	}
	if err = receiver.AcceptConn(); err != nil {
		received <- err
		return
	}
	if !receiver.Publish || receiver.URL.Path != "/fixture/receiver-test-key" {
		received <- errors.New("wrong synthetic receiver stream")
		return
	}
	if err = receiver.AcceptAction(); err != nil {
		received <- err
		return
	}
	var audio, video, confirmed bool
	for {
		msg, err := receiver.Read()
		if err != nil {
			if !confirmed {
				received <- err
			}
			return
		}
		switch m := msg.(type) {
		case *message.Video:
			video = video || m.Codec == message.CodecH264 && m.PacketType == message.VideoPacketTypeAU && len(m.AU) > 0
		case *message.Audio:
			audio = audio || m.AACType == message.AudioAACTypeAU && len(m.AU) > 0
		}
		if !confirmed && audio && video {
			confirmed = true
			received <- nil
		}
	}
}
