package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/mediadecode"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

func TestStationVideoOriginalFromNativeChannels(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " unavailable")
		}
	}
	root := t.TempDir()
	sample := filepath.Join(root, "synthetic.mp4")
	if output, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=blue:s=64x48:r=5:d=1", "-c:v", "mpeg4", "-threads", "1", sample).CombinedOutput(); err != nil {
		t.Fatalf("synthetic video: %v %s", err, output)
	}
	data, err := os.ReadFile(sample)
	if err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	const key, locator, thread = "18446744073709551612", "https://private.example/video?token=synthetic-secret", "room@chatroom"
	encrypted := bytes.Clone(data)
	if err := wechatapp.DecodeChannelsPayload(encrypted, key); err != nil || bytes.Equal(data, encrypted) {
		t.Fatal("fixture did not exercise decryption", err)
	}
	now := time.Now().UTC()
	subtype := 51
	messages := map[string]linuxnative.Message{}
	for _, id := range []string{"card", "rotates", "missing", "expired", "resolve-revoked", "other", "forgotten", "revoked", "corrupt", "direct-no-object"} {
		messages[id] = linuxnative.Message{ID: id, AccountID: "one", ThreadID: thread, SenderID: "alice", ServerID: id + "-srv", CreatedAt: now.Add(-time.Hour).Format(time.RFC3339Nano), Type: 49, AppMessageType: &subtype, Direction: "incoming", Body: "synthetic card", Link: &linuxnative.LinkCard{Title: "Synthetic title"}}
	}
	other := messages["other"]
	other.ThreadID = "elsewhere@chatroom"
	messages["other"] = other
	normal := messages["card"]
	normal.ID, normal.Type, normal.AppMessageType, normal.Link = "normal-video", 43, nil, nil
	messages[normal.ID] = normal
	var objectChanged, blocked atomic.Bool
	var fetches, mutations, transfers atomic.Int32
	nativeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "/v1/accounts/one/"
		out := func(value any) { _ = json.NewEncoder(w).Encode(value) }
		switch r.Method + " " + r.URL.Path {
		case "GET " + prefix + "messages/normal-video/media":
			out(map[string]any{"items": []map[string]any{{"accountId": "one", "messageId": "normal-video", "kind": "video", "variant": "original", "actualVariant": "original", "state": "available"}}})
			return
		case "POST " + prefix + "messages/normal-video/media-transfers":
			transfers.Add(1)
			out(map[string]any{"id": "normal-job", "accountId": "one", "kind": "download", "state": "available", "assetId": "normal-asset"})
			return
		case "GET " + prefix + "media/normal-asset":
			out(map[string]any{"id": "normal-asset", "accountId": "one", "messageId": "normal-video", "kind": "video", "state": "available", "variant": "original", "actualVariant": "original", "byteLength": len(data), "contentType": "video/mp4", "sha256": hash})
			return
		case "GET " + prefix + "media/normal-asset/content":
			_, _ = w.Write(data)
			return
		}
		if r.Method != "GET" {
			mutations.Add(1)
			w.WriteHeader(405)
			return
		}
		if r.URL.Path == "/v1/status" {
			out(map[string]string{"serverId": "station-synthetic-dataset"})
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/v1/accounts/one/messages/")
		id, suffix, _ := strings.Cut(rest, "/")
		message, ok := messages[id]
		if !ok {
			w.WriteHeader(404)
			return
		}
		if suffix == "" {
			out(message)
			return
		}
		if suffix != "channels-media" {
			w.WriteHeader(404)
			return
		}
		source := linuxnative.ChannelsSource{AccountID: "one", MessageID: id, ThreadID: message.ThreadID, ObjectID: "object-" + id, ObjectNonceID: "nonce", State: "available", DecodeKey: key, MediaURL: locator + "&case=" + id}
		switch id {
		case "missing", "resolve-revoked":
			source.State, source.MediaURL = "unavailable", ""
			source.ObjectID = "export/synthetic-" + id
		case "expired":
			source.ObjectID = "export/synthetic-expired"
		case "rotates":
			if objectChanged.Load() {
				source.ObjectNonceID = "different"
			}
		case "direct-no-object":
			source.ObjectID = ""
		}
		out(source)
	}))
	defer nativeServer.Close()
	token := filepath.Join(root, "synthetic-token")
	if err = os.WriteFile(token, []byte("synthetic-token"), 0600); err != nil {
		t.Fatal(err)
	}
	native, err := linuxnative.New(linuxnative.Config{URL: nativeServer.URL, Account: "one", TokenFile: token})
	if err != nil {
		t.Fatal(err)
	}
	memory, err := companion.NewStore(filepath.Join(root, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	fetch := func(_ context.Context, url string, limit int64) ([]byte, string, error) {
		fetches.Add(1)
		if !strings.HasPrefix(url, locator+"&case=") || limit != 10<<20 {
			t.Errorf("unexpected locator or budget: %d", limit)
		}
		switch strings.TrimPrefix(url, locator+"&case=") {
		case "rotates":
			objectChanged.Store(true)
		case "revoked":
			blocked.Store(true)
		case "corrupt":
			return []byte("not a video"), "video/mp4", nil
		case "expired":
			return nil, "", &mcptools.WebError{Code: "http_status", Status: 403}
		}
		return bytes.Clone(encrypted), "video/mp4", nil
	}
	tools := &stationAttachmentTools{native: native, store: memory, dir: root, blocked: blocked.Load, fetchVideo: fetch,
		grants: map[string]time.Time{thread: now.Add(-time.Minute)}}
	tools.SetTrace(companion.Input{Channel: "wechat", Account: "one", Thread: thread, Sender: "bob", TraceID: "question", MessageTime: now.Format(time.RFC3339Nano)})
	call := func(id string) (string, error) {
		return tools.Call(t.Context(), "wechat_video_read", json.RawMessage(fmt.Sprintf(`{"message_id":%q,"max_bytes":10485760}`, id)))
	}
	deny := func(id, code string) {
		t.Helper()
		if result, err := call(id); result != "" || err == nil || code != "" && err.Error() != code {
			t.Fatal(id, result, err)
		}
	}
	deny("card", "") // No history authorization for an old card.
	if fetches.Load() != 0 {
		t.Fatal("unauthorized download")
	}
	tools.reads = map[string]time.Time{thread: now.Add(-time.Minute)}
	before := fetches.Load()
	deny("other", "")
	deny("missing", "channels_media_locator_missing")
	if fetches.Load() != before {
		t.Fatal("wrong or absent locator was downloaded")
	}
	for _, id := range []string{"card", "direct-no-object", "card", "normal-video", "normal-video"} {
		result, err := call(id)
		if err != nil {
			t.Fatal(id, err)
		}
		if strings.Contains(result, "synthetic-secret") || strings.Contains(result, key) || strings.Contains(result, "private.example") || strings.Contains(result, "Synthetic title") {
			t.Fatal("private locator or card copy entered video evidence")
		}
		var value struct {
			Asset       string             `json:"asset_id"`
			Hash        string             `json:"sha256"`
			Observed    bool               `json:"content_observed"`
			Transcribed bool               `json:"audio_transcribed"`
			Metadata    map[string]float64 `json:"metadata"`
		}
		if err = json.Unmarshal([]byte(result), &value); err != nil || !stationAttachmentID(value.Asset) || value.Hash != hash || value.Observed || value.Transcribed || value.Metadata["total_frames"] != 5 || value.Metadata["width"] != 64 {
			t.Fatal(value, err)
		}
		stored, err := os.ReadFile(filepath.Join(root, "attachments", value.Asset, "original"))
		if err != nil || !bytes.Equal(stored, data) {
			t.Fatal("original cache mismatch", err)
		}
	}
	if fetches.Load() != 2 || transfers.Load() != 1 {
		t.Fatal("wrong transfer method or repeated download", fetches.Load(), transfers.Load())
	}
	deny("rotates", "")
	deny("corrupt", "wechat_video_probe_failed")
	deny("revoked", "")
	blocked.Store(false)
	m := messages["forgotten"]
	in := companion.Input{Channel: "wechat", Account: m.AccountID, Thread: m.ThreadID, Sender: m.SenderID, TraceID: m.ID, MessageTime: m.CreatedAt, Text: "synthetic observed source"}
	cfg := companion.Defaults()
	cfg.Permissions.Remember = true
	if err = memory.Observe(in, cfg, now); err != nil {
		t.Fatal(err)
	}
	if err = memory.Clear(companion.ScopeID("wechat", "one", thread)); err != nil {
		t.Fatal(err)
	}
	before = fetches.Load()
	deny("forgotten", "")
	if fetches.Load() != before || mutations.Load() != 0 {
		t.Fatal("forgetting or no-send contract failed", fetches.Load(), mutations.Load())
	}
	entries, err := os.ReadDir(filepath.Join(root, "attachments"))
	if err != nil || len(entries) != 3 {
		t.Fatal("failed/changed source was published or staged data was left", entries, err)
	}
	var routes int
	var export string
	share := stationShareTestHost(t, root, func(_ context.Context, endpoint string, headers map[string]string, body json.RawMessage, _ int64, _ time.Duration) (json.RawMessage, error) {
		routes++
		switch {
		case strings.Contains(endpoint, "get_object_short_link"):
			if headers["Cookie"] != "synthetic-assistant-cookie" {
				t.Fatal("assistant credential missing at proxy")
			}
			var request map[string]any
			_ = json.Unmarshal(body, &request)
			export, _ = request["exportId"].(string)
			if export == "export/synthetic-resolve-revoked" {
				blocked.Store(true)
			}
			return json.RawMessage(`{"errCode":0,"data":{"shortUrl":"https://weixin.qq.com/sph/synthetic"}}`), nil
		case strings.Contains(endpoint, "get_parse_result"):
			if headers["Cookie"] != "synthetic-yuanbao-cookie" {
				t.Fatal("Yuanbao credential missing at proxy")
			}
			return json.RawMessage(fmt.Sprintf(`{"code":0,"data":{"playable_url":%q}}`, "https://channels.weixin.qq.com/finder-preview/detail?token=synthetic-preview&eid="+export)), nil
		case strings.Contains(endpoint, "get_feed_info"):
			if headers["Cookie"] != "" {
				t.Fatal("credentials escaped their declared route")
			}
			return json.RawMessage(fmt.Sprintf(`{"errCode":0,"data":{"sceneInfo":{"dynamicExportId":%q},"feedInfo":{"objectId":"12345","videoUrl":%q,"decodeKey":%q}}}`, export, locator+"&case=resolved", key)), nil
		default:
			return nil, errors.New("unexpected outbound route")
		}
	})
	tools.shareHost = func() (*stationShareHost, error) { return share, nil }
	for _, id := range []string{"missing", "expired", "missing"} {
		result, err := call(id)
		if err != nil || !strings.Contains(result, hash) || strings.Contains(result, "private.example") || strings.Contains(result, key) {
			t.Fatal(id, result, err)
		}
	}
	if routes != 6 {
		t.Fatal("wrong route count or cache not reused", routes)
	}
	before = fetches.Load()
	deny("resolve-revoked", "")
	if routes != 7 || fetches.Load() != before {
		t.Fatal("revocation did not block later parsing and media requests", routes, fetches.Load(), before)
	}
}

func stationShareTestHost(t *testing.T, root string, send pluginruntime.RouteTransport) *stationShareHost {
	t.Helper()
	ctx := t.Context()
	store, err := pluginhost.OpenStore(filepath.Join(root, "share-fixture"), "one")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	prepared, err := store.Prepare(ctx, filepath.Join("..", "..", "plugins", shareServicePluginID))
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Install(ctx, prepared.Token, prepared.Digest, []string{"network"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SaveConfiguration(ctx, shareServicePluginID, pluginhost.ConfigUpdate{Secrets: map[string]string{"yuanbao_cookie": "synthetic-yuanbao-cookie", "assistant_cookie": "synthetic-assistant-cookie"}}); err != nil {
		t.Fatal(err)
	}
	var executor pluginhost.Executor = stationShareTestExecutor{}
	if runtimeRoot := os.Getenv("AHS_SHARE_TEST_RUNTIME_ROOT"); runtimeRoot != "" {
		python := filepath.Join(runtimeRoot, "windows-python-cli")
		options := pluginruntime.Options{Python: filepath.Join(python, "python.exe"), Pythonw: filepath.Join(python, "pythonw.exe"), WindowsHelper: filepath.Join(runtimeRoot, "windows-agent-host", "windows_plugin_host.pyw"), RuntimeRoots: []string{python}}
		if !stationShareRuntimeReady(options) {
			t.Fatal("test LPAC runtime unavailable")
		}
		executor = pluginruntime.InstalledExecutor(store, options)
	}
	controller, err := pluginruntime.NewController(store, executor)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controller.Close() })
	if err = controller.EnableOnDemand(ctx, shareServicePluginID, item.Revision); err != nil {
		t.Fatal(err)
	}
	return &stationShareHost{store: store, controller: controller, send: send}
}

type stationShareTestExecutor struct{}

func (e stationShareTestExecutor) Start(context.Context, pluginhost.Package, pluginhost.Grant) (pluginhost.Session, error) {
	return e, nil
}
func (stationShareTestExecutor) Health(ctx context.Context) error { return ctx.Err() }
func (stationShareTestExecutor) Close() error                     { return nil }
func (stationShareTestExecutor) Call(ctx context.Context, _ pluginhost.Invocation, _ string, raw json.RawMessage) (json.RawMessage, error) {
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	export, _ := args["export_id"].(string)
	const link = "https://weixin.qq.com/sph/synthetic"
	call := func(route string, body map[string]any, referer string) (json.RawMessage, error) {
		raw, _ := json.Marshal(map[string]any{"route": route, "body": body, "referer": referer, "timeout_seconds": 25, "max_bytes": 2 << 20})
		return pluginruntime.InvokeHostService(ctx, "wechat/http_route", raw)
	}
	if _, err := call("channels-shortlink", map[string]any{"exportId": export, "nonceId": args["nonce_id"], "scene": 40}, ""); err != nil {
		return nil, err
	}
	if _, err := call("channels-parse", map[string]any{"type": "video_channel_url", "url": link, "scene": 1}, ""); err != nil {
		return nil, err
	}
	feed, err := call("channels-feed", map[string]any{"baseReq": map[string]string{"generalToken": "synthetic-preview"}, "exportId": export}, "https://channels.weixin.qq.com/finder-preview/detail?token=synthetic-preview&eid="+export)
	if err != nil {
		return nil, err
	}
	var packet struct {
		Data struct {
			Feed map[string]string `json:"feedInfo"`
		} `json:"data"`
	}
	if err = json.Unmarshal(feed, &packet); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"ok": true, "platform": "channels", "source_url": link, "object_id": "12345", "title": "", "author": "", "private_media_locators": []string{packet.Data.Feed["videoUrl"]}, "private_decode_key": packet.Data.Feed["decodeKey"], "coverage": "media_locator_only", "video_observed": false, "media_downloaded": false, "untrusted_external_content": true})
}

func TestStationVideoDecoderUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := mediadecode.Probe(t.Context(), "irrelevant.mp4")
	if err == nil || stationProbeError(t.Context(), err).Error() != "wechat_video_decoder_unavailable" {
		t.Fatal(err)
	}
}
