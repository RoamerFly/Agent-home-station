package pluginruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/pluginhost"
)

type offlineWebPolicy struct {
	calls           int
	revoked         bool
	revokeAfterRead bool
}

func (p *offlineWebPolicy) Check(_ context.Context, j pluginhost.Job, c pluginhost.Capability) error {
	if p.revoked || j.Context.Account != "a" || j.Context.Thread != "room" || j.Context.Requester != "person" {
		return errors.New("synthetic authorization revoked")
	}
	return nil
}
func (p *offlineWebPolicy) Validate(_ context.Context, j pluginhost.Job, c pluginhost.Capability, raw json.RawMessage) (VerifiedResult, error) {
	var result struct {
		OK    bool   `json:"ok"`
		Text  string `json:"text"`
		Query string `json:"query"`
	}
	if json.Unmarshal(raw, &result) != nil || !result.OK || (!strings.Contains(result.Text, "合成") && !strings.Contains(result.Query, "合成")) {
		return VerifiedResult{}, errors.New("invalid synthetic observation")
	}
	return VerifiedResult{Result: raw}, nil
}
func (p *offlineWebPolicy) Service(_ context.Context, j pluginhost.Job, c pluginhost.Capability, method string, raw json.RawMessage) (json.RawMessage, error) {
	if method != "wechat/http_read" {
		return nil, errors.New("unexpected method")
	}
	var request struct {
		URL      string `json:"url"`
		MaxBytes int    `json:"max_bytes"`
	}
	if json.Unmarshal(raw, &request) != nil || request.MaxBytes <= 0 {
		return nil, errors.New("bad request")
	}
	p.calls++
	if p.revokeAfterRead {
		p.revoked = true
	}
	body := "<title>合成页</title><script>隐藏内容</script><p>合成正文内容</p>"
	kind := "text/html; charset=utf-8"
	if strings.Contains(request.URL, "bing.com") {
		body = `<rss><channel><item><title>合成搜索结果</title><link>https://example.test/result</link><description>合成摘要</description></item></channel></rss>`
		kind = "text/xml"
	}
	return json.Marshal(map[string]any{"ok": true, "url": request.URL, "content_type": kind, "body_base64": base64.StdEncoding.EncodeToString([]byte(body))})
}
func TestLiveWebPluginMediatedNetworkAndRecovery(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in real sandbox with synthetic HTTP proxy; no actual network")
	}
	python, e := filepath.Abs(python)
	if e != nil {
		t.Fatal(e)
	}
	helper, _ := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	s, e := pluginhost.OpenStore(t.TempDir(), "a")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	prepared, e := s.Prepare(ctx, "../../plugins/web-search")
	if e != nil {
		t.Fatal(e)
	}
	i, e := s.Install(ctx, prepared.Token, prepared.Digest, []string{"network"})
	if e != nil {
		t.Fatal(e)
	}
	c, e := NewController(s, InstalledExecutor(s, Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if e = c.Start(ctx, i.Package.ID, i.Revision); e != nil {
		t.Fatal(e)
	}
	policy := &offlineWebPolicy{}
	r := CallRequest{Plugin: i.Package.ID, Capability: "web.read", Context: invocation(), Arguments: json.RawMessage(`{"url":"https://example.test/page","limit":100000}`)}
	first, e := c.Invoke(ctx, r, policy)
	if e != nil || first.Job.State != "completed" || policy.calls != 1 {
		t.Fatal("mediated read failed", first, e, policy.calls)
	}
	retry, e := c.Invoke(ctx, r, policy)
	if e != nil || !retry.Reused || policy.calls != 1 {
		t.Fatal("read replayed", retry, e, policy.calls)
	}
	r.Capability = "web.search"
	r.Context.RequestKey = "search"
	r.Arguments = json.RawMessage(`{"query":"合成","limit":100}`)
	searched, e := c.Invoke(ctx, r, policy)
	if e != nil || searched.Job.State != "completed" || policy.calls != 2 {
		t.Fatal("mediated search failed", searched, e, policy.calls)
	}
	r.Capability = "web.read"
	r.Context.RequestKey = "revoked"
	r.Arguments = json.RawMessage(`{"url":"https://example.test/page"}`)
	policy.revokeAfterRead = true
	revoked, e := c.Invoke(ctx, r, policy)
	if e == nil || revoked.Job.State != "needs_review" || len(revoked.Job.Result) > 0 {
		t.Fatal("revoked result exposed", revoked, e)
	}
}
