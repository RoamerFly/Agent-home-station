package pluginruntime

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wechat-bot/internal/pluginhost"
)

// This policy validates only synthetic parser output. It is not the production
// artifact/Channels adapter and cannot expose this pending package to an Agent.
type offlineSharePolicy struct{ offlineWebPolicy }

type offlineBiliPolicy struct{ offlineWebPolicy }

func (p *offlineBiliPolicy) Service(_ context.Context, _ pluginhost.Job, _ pluginhost.Capability, method string, raw json.RawMessage) (json.RawMessage, error) {
	var request struct {
		URL string `json:"url"`
	}
	if method != "wechat/http_read" || json.Unmarshal(raw, &request) != nil {
		return nil, errors.New("unexpected synthetic request")
	}
	const page = "https://www.bilibili.com/video/BV0000000001/?p=2"
	const api = "https://api.bilibili.com/x/player/playurl?bvid=BV0000000001&cid=102&qn=32&fnval=0&fourk=0"
	var body, kind string
	switch {
	case p.calls == 0 && request.URL == page:
		body = `window.__INITIAL_STATE__={"videoData":{"bvid":"BV0000000001","cid":101,"title":"合成作品","pages":[{"cid":101,"part":"一"},{"cid":102,"part":"二"}]}};`
		kind = "text/html"
	case p.calls == 1 && request.URL == api:
		body = `{"code":0,"data":{"timelength":720000,"durl":[{"url":"https://synthetic.bilivideo.com/video.mp4?token=private","size":1234}]}}`
		kind = "application/json"
	default:
		return nil, errors.New("unexpected video or part")
	}
	p.calls++
	if p.revokeAfterRead {
		p.revoked = true
	}
	return json.Marshal(map[string]any{"ok": true, "url": request.URL, "content_type": kind, "body_base64": base64.StdEncoding.EncodeToString([]byte(body))})
}

func (p *offlineBiliPolicy) Validate(_ context.Context, _ pluginhost.Job, _ pluginhost.Capability, raw json.RawMessage) (VerifiedResult, error) {
	var result struct {
		OK         bool     `json:"ok"`
		ID         string   `json:"object_id"`
		Source     string   `json:"source_url"`
		Duration   int64    `json:"duration_ms"`
		Coverage   string   `json:"coverage"`
		Observed   bool     `json:"video_observed"`
		Downloaded bool     `json:"media_downloaded"`
		Locators   []string `json:"private_media_locators"`
	}
	if json.Unmarshal(raw, &result) != nil || !result.OK || result.ID != "BV0000000001" || result.Source != "https://www.bilibili.com/video/BV0000000001/?p=2" || result.Duration != 720000 || result.Coverage != "media_locator_only" || result.Observed || result.Downloaded || len(result.Locators) != 1 || result.Locators[0] != "https://synthetic.bilivideo.com/video.mp4?token=private" {
		return VerifiedResult{}, errors.New("incorrect synthetic Bilibili result")
	}
	return VerifiedResult{Result: json.RawMessage(`{"object_id":"BV0000000001","video_observed":false}`)}, nil
}

type offlineChannelsPolicy struct {
	offlineWebPolicy
	store *pluginhost.Store
}

func (p *offlineChannelsPolicy) Service(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, method string, raw json.RawMessage) (json.RawMessage, error) {
	if method != "wechat/http_route" {
		return nil, errors.New("unexpected channels service")
	}
	bind := func(route string, body json.RawMessage) error {
		var object map[string]any
		if json.Unmarshal(body, &object) != nil {
			return errors.New("invalid body")
		}
		switch route {
		case "channels-parse":
			if object["url"] != "https://weixin.qq.com/sph/synthetic123" {
				return errors.New("wrong source")
			}
		case "channels-feed":
			if object["exportId"] != "export/synthetic" {
				return errors.New("wrong resolved work")
			}
		default:
			return errors.New("route outside synthetic request")
		}
		return nil
	}
	return ProxyHTTPRoute(ctx, p.store, j, cap, raw, bind, func(_ context.Context, endpoint string, headers map[string]string, body json.RawMessage, _ int64, _ time.Duration) (json.RawMessage, error) {
		p.calls++
		switch endpoint {
		case "https://yuanbao.tencent.com/api/weixin/get_parse_result":
			if headers["Cookie"] != "synthetic-yuanbao=private" {
				return nil, errors.New("wrong credential")
			}
			return json.RawMessage(`{"code":0,"data":{"playable_url":"https://channels.weixin.qq.com/finder-preview/detail?token=synthetic-token&eid=export/synthetic"}}`), nil
		case "https://channels.weixin.qq.com/finder-preview/api/feed/get_feed_info":
			if headers["Cookie"] != "" || headers["Authorization"] != "" {
				return nil, errors.New("credential leaked to preview")
			}
			if headers["Referer"] != "https://channels.weixin.qq.com/finder-preview/detail?token=synthetic-token&eid=export/synthetic" {
				return nil, errors.New("preview source referer lost")
			}
			return json.RawMessage(`{"errCode":0,"data":{"authorInfo":{"nickname":"合成作者"},"feedInfo":{"description":"合成标题","videoUrl":"https://media.example/synthetic.mp4?token=private","decodeKey":"18446744073709551615","objectId":"12345"}}}`), nil
		}
		return nil, errors.New("unexpected endpoint")
	})
}
func (p *offlineChannelsPolicy) Validate(_ context.Context, _ pluginhost.Job, _ pluginhost.Capability, raw json.RawMessage) (VerifiedResult, error) {
	var value struct {
		OK       bool   `json:"ok"`
		ID       string `json:"object_id"`
		Key      string `json:"private_decode_key"`
		Observed bool   `json:"video_observed"`
	}
	if json.Unmarshal(raw, &value) != nil || !value.OK || value.ID != "12345" || value.Key != "18446744073709551615" || value.Observed {
		return VerifiedResult{}, errors.New("channels fixture resolution failed")
	}
	return VerifiedResult{Result: json.RawMessage(`{"object_id":"12345","video_observed":false}`)}, nil
}

func (p *offlineSharePolicy) Service(_ context.Context, _ pluginhost.Job, _ pluginhost.Capability, method string, raw json.RawMessage) (json.RawMessage, error) {
	var request struct {
		URL string `json:"url"`
	}
	const page = "https://www.kuaishou.com/short-video/3xsynthetic12345"
	if method != "wechat/http_read" || json.Unmarshal(raw, &request) != nil || request.URL != page {
		return nil, errors.New("unexpected synthetic request")
	}
	p.calls++
	if p.revokeAfterRead {
		p.revoked = true
	}
	body := `window.__APOLLO_STATE__={"defaultClient":{"VisionVideoDetailPhoto:3xsynthetic12345":{"id":"3xsynthetic12345","caption":"合成视频","duration":155100,"photoUrl":"https://synthetic.kwaicdn.com/video.mp4?synthetic=private"},"VisionVideoDetailPhoto:other":{"id":"other","photoUrl":"https://other.kwaicdn.com/wrong.mp4"}}}`
	return json.Marshal(map[string]any{"ok": true, "url": page, "content_type": "text/html; charset=utf-8", "body_base64": base64.StdEncoding.EncodeToString([]byte(body))})
}

func (p *offlineSharePolicy) Validate(_ context.Context, _ pluginhost.Job, _ pluginhost.Capability, raw json.RawMessage) (VerifiedResult, error) {
	var result struct {
		OK         bool     `json:"ok"`
		ID         string   `json:"object_id"`
		Coverage   string   `json:"coverage"`
		Observed   bool     `json:"video_observed"`
		Downloaded bool     `json:"media_downloaded"`
		Locators   []string `json:"private_media_locators"`
	}
	if json.Unmarshal(raw, &result) != nil || !result.OK || result.ID != "3xsynthetic12345" || result.Coverage != "media_locator_only" || result.Observed || result.Downloaded || len(result.Locators) != 1 || result.Locators[0] != "https://synthetic.kwaicdn.com/video.mp4?synthetic=private" {
		return VerifiedResult{}, errors.New("incorrect synthetic work or coverage")
	}
	// Do not leak signed-media handoff data into retained tool/model output.
	return VerifiedResult{Result: json.RawMessage(`{"coverage":"media_locator_only","video_observed":false,"object_id":"3xsynthetic12345"}`)}, nil
}

func TestLiveShareParserIsolatedExactWorkAndRevocation(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in real sandbox; synthetic HTTP only")
	}
	python, err := filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	helper, _ := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	s, err := pluginhost.OpenStore(t.TempDir(), "a")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Pending package must remain rejected. Only an isolated copy is promoted
	// to a test fixture, containing the implemented public parser capability.
	pending, err := s.Prepare(ctx, "../../plugins/share-reader")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Install(ctx, pending.Token, pending.Digest, []string{"network"}); err == nil {
		t.Fatal("pending production package installable")
	}
	dir := t.TempDir()
	if err = os.CopyFS(dir, os.DirFS("../../plugins/share-reader")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "capabilities.json"))
	if err != nil {
		t.Fatal(err)
	}
	var caps struct {
		Version      int                     `json:"version"`
		Capabilities []pluginhost.Capability `json:"capabilities"`
	}
	if err = json.Unmarshal(raw, &caps); err != nil {
		t.Fatal(err)
	}
	var selected []pluginhost.Capability
	for _, cap := range caps.Capabilities {
		if cap.ID == "share.resolve_public" || cap.ID == "share.resolve_channels" {
			cap.Implementation = "ready"
			cap.Discoverable = true
			selected = append(selected, cap)
		}
	}
	if len(selected) != 2 {
		t.Fatal("parser capability missing")
	}
	caps.Capabilities = selected
	raw, _ = json.Marshal(caps)
	if err = os.WriteFile(filepath.Join(dir, "capabilities.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	p := pending.Package
	p.ID = "synthetic-share-parser"
	p.Status = "ready"
	p.Permissions = []string{"network"}
	p.Dependencies = nil
	p.Files["capabilities.json"] = hex.EncodeToString(sum[:])
	raw, _ = json.Marshal(p)
	if err = os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	prepared, err := s.Prepare(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	i, err := s.Install(ctx, prepared.Token, prepared.Digest, p.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveConfiguration(ctx, p.ID, pluginhost.ConfigUpdate{Revision: 0, Values: map[string]json.RawMessage{}, Secrets: map[string]string{"yuanbao_cookie": "synthetic-yuanbao=private", "assistant_cookie": "synthetic-assistant=private"}}); err != nil {
		t.Fatal(err)
	}
	c, err := NewController(s, InstalledExecutor(s, Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = c.Start(ctx, i.Package.ID, i.Revision); err != nil {
		t.Fatal(err)
	}
	policy := &offlineSharePolicy{}
	r := CallRequest{Plugin: i.Package.ID, Capability: "share.resolve_public", Context: invocation(), Arguments: json.RawMessage(`{"url":"https://www.kuaishou.com/short-video/3xsynthetic12345"}`)}
	first, err := c.Invoke(ctx, r, policy)
	if err != nil || first.Job.State != "completed" || policy.calls != 1 {
		t.Fatal("parser invocation", first, err, policy.calls)
	}
	reused, err := c.Invoke(ctx, r, policy)
	if err != nil || !reused.Reused || policy.calls != 1 {
		t.Fatal("replayed parser invocation", err)
	}
	biliPolicy := &offlineBiliPolicy{}
	biliRequest := CallRequest{Plugin: i.Package.ID, Capability: "share.resolve_public", Context: invocation(), Arguments: json.RawMessage(`{"url":"https://www.bilibili.com/video/BV0000000001/?p=2"}`)}
	biliRequest.Context.RequestKey = "synthetic-bili-part"
	bili, err := c.Invoke(ctx, biliRequest, biliPolicy)
	if err != nil || bili.Job.State != "completed" || biliPolicy.calls != 2 {
		t.Fatal("Bilibili exact part", bili.Job.State, err, biliPolicy.calls)
	}
	biliAgain, err := c.Invoke(ctx, biliRequest, biliPolicy)
	if err != nil || !biliAgain.Reused || biliPolicy.calls != 2 {
		t.Fatal("Bilibili source replayed", err, biliPolicy.calls)
	}
	biliRevoked := &offlineBiliPolicy{offlineWebPolicy: offlineWebPolicy{revokeAfterRead: true}}
	biliRequest.Context.RequestKey = "synthetic-bili-revoked"
	blocked, err := c.Invoke(ctx, biliRequest, biliRevoked)
	if err == nil || blocked.Job.State != "needs_review" || len(blocked.Job.Result) != 0 || biliRevoked.calls != 1 {
		t.Fatal("Bilibili continued after revocation", blocked.Job.State, err, biliRevoked.calls)
	}
	channelsPolicy := &offlineChannelsPolicy{store: s}
	channelsRequest := CallRequest{Plugin: i.Package.ID, Capability: "share.resolve_channels", Context: invocation(), Arguments: json.RawMessage(`{"url":"https://weixin.qq.com/sph/synthetic123","expected_object_id":"12345"}`)}
	channelsRequest.Context.RequestKey = "channels-source"
	resolved, err := c.Invoke(ctx, channelsRequest, channelsPolicy)
	if err != nil || resolved.Job.State != "completed" || channelsPolicy.calls != 2 {
		t.Fatal("mediated credential routes", resolved, err, channelsPolicy.calls)
	}
	policy.revokeAfterRead = true
	r.Context.RequestKey = "revoked-share"
	result, err := c.Invoke(ctx, r, policy)
	if err == nil || result.Job.State != "needs_review" || len(result.Job.Result) > 0 {
		t.Fatal("revoked share exposed", result, err)
	}
}
