// Package plugins runs owner-configured, declarative HTTP API tools.  It never
// loads code, starts a process, evaluates a template, or follows a redirect.
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"wechat-bot/internal/mcptools"
)

const (
	maxPlugins = 30
	maxSchema  = 64 << 10
	maxArgs    = 8192
	maxJSON    = 2 << 20
	maxBinary  = 25 << 20
)

var ident = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
var placeholder = regexp.MustCompile(`\{\{(secret|arg|result)\.([A-Za-z][A-Za-z0-9_.-]{0,127})\}\}`)

type Manifest struct {
	ID             string                 `json:"id"`
	Name           string                 `json:"name"`
	Version        string                 `json:"version"`
	Description    string                 `json:"description"`
	Tools          []APITool              `json:"tools"`
	MCP            *mcptools.ServerConfig `json:"mcp,omitempty"`
	ToolMediaKinds map[string]string      `json:"tool_media_kinds,omitempty"`
}
type APITool struct {
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	InputSchema    json.RawMessage   `json:"input_schema"`
	URL            string            `json:"url"`
	Method         string            `json:"method"`
	Headers        map[string]string `json:"headers,omitempty"`
	BodyTemplate   json.RawMessage   `json:"body_template,omitempty"`
	TextPath       string            `json:"text_path,omitempty"`
	MediaURLPath   string            `json:"media_url_path,omitempty"`
	MediaKind      string            `json:"media_kind,omitempty"`
	Poll           *PollSpec         `json:"poll,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
}
type PollSpec struct {
	URL             string            `json:"url"`
	Method          string            `json:"method"`
	Headers         map[string]string `json:"headers,omitempty"`
	StatusPath      string            `json:"status_path"`
	DoneValues      []string          `json:"done_values"`
	FailedValues    []string          `json:"failed_values,omitempty"`
	MediaURLPath    string            `json:"media_url_path,omitempty"`
	TextPath        string            `json:"text_path,omitempty"`
	IntervalSeconds int               `json:"interval_seconds,omitempty"`
	MaxAttempts     int               `json:"max_attempts,omitempty"`
}
type Installed struct {
	Manifest Manifest          `json:"manifest"`
	Enabled  bool              `json:"enabled"`
	Secrets  map[string]string `json:"secrets,omitempty"`
}
type MediaResult struct {
	Kind       string `json:"kind"`
	URL        string `json:"url,omitempty"`
	Data       []byte `json:"-"`
	Base64Data string `json:"data_base64,omitempty"`
	MIMEType   string `json:"mime_type,omitempty"`
	Filename   string `json:"filename,omitempty"`
}
type Output struct {
	Text  string        `json:"text"`
	Media []MediaResult `json:"media,omitempty"`
}

type Runtime struct {
	installed map[string]Installed
	tools     map[string]APITool
}

// Validate checks the untrusted descriptor, without testing credentials or a network endpoint.
func Validate(m Manifest) error {
	if !ident.MatchString(m.ID) || strings.Contains(m.ID, "__") {
		return errors.New("invalid plugin id")
	}
	if strings.TrimSpace(m.Name) == "" || len(m.Name) > 128 || len(m.Version) > 64 || len(m.Description) > 2048 {
		return errors.New("invalid plugin metadata")
	}
	if len(m.Tools) == 0 && m.MCP == nil {
		return errors.New("plugin must define an API tool or MCP service")
	}
	if len(m.Tools) > 20 {
		return errors.New("at most 20 API tools per plugin")
	}
	if len(m.ToolMediaKinds) > 40 {
		return errors.New("too many MCP media classifications")
	}
	for name, kind := range m.ToolMediaKinds {
		if !ident.MatchString(name) || !validKind(kind) {
			return errors.New("invalid MCP media classification")
		}
	}
	seen := map[string]bool{}
	for _, t := range m.Tools {
		if !ident.MatchString(t.Name) || seen[t.Name] {
			return errors.New("invalid or duplicate tool name")
		}
		seen[t.Name] = true
		if len(t.InputSchema) == 0 || len(t.InputSchema) > maxSchema || !json.Valid(t.InputSchema) {
			return errors.New("invalid input schema")
		}
		if t.TimeoutSeconds < 0 || t.TimeoutSeconds > 120 {
			return errors.New("invalid request timeout")
		}
		var schema any
		if json.Unmarshal(t.InputSchema, &schema) != nil {
			return errors.New("invalid input schema")
		}
		object, ok := schema.(map[string]any)
		if !ok || object["type"] != "object" {
			return errors.New("tool schema must describe an object")
		}
		if err := validEndpoint(t.URL, false); err != nil {
			return fmt.Errorf("invalid API URL")
		}
		if t.Method == "" {
			t.Method = http.MethodPost
		}
		if t.Method != http.MethodGet && t.Method != http.MethodPost && t.Method != http.MethodPut && t.Method != http.MethodPatch {
			return errors.New("unsupported API method")
		}
		if len(t.Headers) > 32 || len(t.BodyTemplate) > maxSchema || (len(t.BodyTemplate) > 0 && !json.Valid(t.BodyTemplate)) {
			return errors.New("invalid API template")
		}
		for k, v := range t.Headers {
			if !validHeader(k, v) {
				return errors.New("invalid API header")
			}
		}
		if t.MediaKind != "" && !validKind(t.MediaKind) {
			return errors.New("invalid media kind")
		}
		if (t.MediaURLPath != "" || t.Poll != nil && t.Poll.MediaURLPath != "") && t.MediaKind == "" {
			return errors.New("media URL requires media kind")
		}
		if t.Poll != nil {
			if err := validPoll(t.Poll); err != nil {
				return err
			}
		}
	}
	if m.MCP != nil {
		if len(m.MCP.ID) > 0 && m.MCP.ID != m.ID {
			return errors.New("MCP id is managed by plugin id")
		}
		if len(m.MCP.Headers) > 32 || len(m.MCP.Env) > 32 {
			return errors.New("MCP configuration exceeds limits")
		}
		for k, v := range m.MCP.Headers {
			if !validHeader(k, v) {
				return errors.New("invalid MCP header")
			}
		}
		for k, v := range m.MCP.Env {
			if k == "" || strings.ContainsAny(k+v, "\r\n\x00") || len(v) > 8192 {
				return errors.New("invalid MCP environment")
			}
		}
	}
	return nil
}
func validHeader(k, v string) bool {
	if strings.EqualFold(k, "authorization") || strings.EqualFold(k, "x-api-key") || strings.EqualFold(k, "api-key") || strings.EqualFold(k, "cookie") {
		if v != "" && !strings.Contains(v, "{{secret.") {
			return false
		}
	}
	return k != "" && !strings.EqualFold(k, "host") && !strings.ContainsAny(k+v, "\r\n\x00") && len(k) < 128 && len(v) <= 8192
}
func validKind(s string) bool { return s == "image" || s == "voice" || s == "video" || s == "file" }
func validPoll(p *PollSpec) error {
	if err := validEndpoint(p.URL, true); err != nil {
		return errors.New("invalid poll URL")
	}
	if p.Method != "" && p.Method != http.MethodGet {
		return errors.New("poll method must be GET")
	}
	if p.MaxAttempts < 0 || p.MaxAttempts > 12 || p.IntervalSeconds < 0 || p.IntervalSeconds > 60 {
		return errors.New("invalid poll limits")
	}
	if p.StatusPath == "" || len(p.DoneValues) == 0 {
		return errors.New("poll status path and done values required")
	}
	if len(p.Headers) > 32 {
		return errors.New("too many poll headers")
	}
	for key, value := range p.Headers {
		if !validHeader(key, value) {
			return errors.New("invalid poll header")
		}
	}
	return nil
}

func New(items []Installed) (*Runtime, error) {
	if len(items) > maxPlugins {
		return nil, fmt.Errorf("at most %d plugins", maxPlugins)
	}
	r := &Runtime{installed: map[string]Installed{}, tools: map[string]APITool{}}
	totalTools := 0
	for _, in := range items {
		if err := Validate(in.Manifest); err != nil {
			return nil, err
		}
		id := in.Manifest.ID
		if _, ok := r.installed[id]; ok {
			return nil, errors.New("duplicate plugin id")
		}
		r.installed[id] = in
		if !in.Enabled {
			continue
		}
		encoded, _ := json.Marshal(in.Manifest)
		for _, ref := range placeholder.FindAllStringSubmatch(string(encoded), -1) {
			if ref[1] == "secret" && strings.TrimSpace(in.Secrets[ref[2]]) == "" {
				return nil, errors.New("required plugin secret is not configured")
			}
		}
		totalTools += len(in.Manifest.Tools)
		if totalTools > 80 {
			return nil, errors.New("at most 80 enabled API tools")
		}
		for _, t := range in.Manifest.Tools {
			n := id + "__" + t.Name
			if _, ok := r.tools[n]; ok {
				return nil, errors.New("duplicate plugin tool")
			}
			r.tools[n] = t
		}
	}
	return r, nil
}
func (r *Runtime) Tools() []mcptools.Tool {
	out := make([]mcptools.Tool, 0, len(r.tools))
	for n, t := range r.tools {
		out = append(out, mcptools.Tool{Name: n, Description: t.Description, InputSchema: append(json.RawMessage(nil), t.InputSchema...)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// MCPServers returns separately configured external MCP servers. API tools and
// MCP tools intentionally use different paths: callers can compose this with
// their MCP hub without allowing a plugin descriptor to execute downloaded code.
// Only {{secret.KEY}} is rendered here; model arguments cannot affect a server.
func (r *Runtime) MCPServers() ([]mcptools.ServerConfig, error) {
	out := make([]mcptools.ServerConfig, 0)
	for _, in := range r.installed {
		if !in.Enabled || in.Manifest.MCP == nil {
			continue
		}
		c := *in.Manifest.MCP
		c.ID, c.Enabled = in.Manifest.ID, true
		var err error
		if c.URL, err = renderMCPString(c.URL, in.Secrets); err != nil {
			return nil, errors.New("plugin MCP configuration is incomplete")
		}
		c.Headers, err = renderMCPMap(c.Headers, in.Secrets)
		if err != nil {
			return nil, errors.New("plugin MCP configuration is incomplete")
		}
		c.Env, err = renderMCPMap(c.Env, in.Secrets)
		if err != nil {
			return nil, errors.New("plugin MCP configuration is incomplete")
		}
		out = append(out, c)
	}
	return out, nil
}
func renderMCPMap(src map[string]string, secrets map[string]string) (map[string]string, error) {
	if len(src) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		x, e := renderMCPString(v, secrets)
		if e != nil {
			return nil, e
		}
		out[k] = x
	}
	return out, nil
}
func renderMCPString(s string, secrets map[string]string) (string, error) {
	if strings.Contains(s, "{{arg.") || strings.Contains(s, "{{result.") {
		return "", errors.New("MCP strings may only contain secret placeholders")
	}
	return renderString(s, secrets, map[string]any{}, nil)
}

func (r *Runtime) Call(ctx context.Context, name string, args json.RawMessage) (Output, error) {
	t, ok := r.tools[name]
	if !ok {
		return Output{}, errors.New("unknown plugin tool")
	}
	parts := strings.SplitN(name, "__", 2)
	in := r.installed[parts[0]]
	if len(args) == 0 {
		args = []byte(`{}`)
	}
	if len(args) > maxArgs || !json.Valid(args) {
		return Output{}, errors.New("tool arguments must be a JSON object under 8192 bytes")
	}
	var arg any
	if json.Unmarshal(args, &arg) != nil {
		return Output{}, errors.New("invalid tool arguments")
	}
	if _, ok := arg.(map[string]any); !ok {
		return Output{}, errors.New("tool arguments must be a JSON object")
	}
	return invoke(ctx, t, in.Secrets, arg)
}

func invoke(ctx context.Context, t APITool, secrets map[string]string, args any) (Output, error) {
	body, err := renderBody(t.BodyTemplate, secrets, args, nil)
	if err != nil {
		return Output{}, safeErr(err)
	}
	b, ct, err := request(ctx, t.URL, t.Method, t.Headers, body, secrets, args, nil, t.TimeoutSeconds)
	if err != nil {
		return Output{}, safeErr(err)
	}
	if !isJSON(ct, b) {
		if t.MediaKind == "" {
			return Output{}, errors.New("API returned binary without media kind")
		}
		return Output{Media: []MediaResult{{Kind: t.MediaKind, Data: b, MIMEType: ct}}}, nil
	}
	result, err := decodeResult(b)
	if err != nil {
		return Output{}, errors.New("API returned invalid JSON")
	}
	if t.Poll != nil {
		result, err = poll(ctx, t, t.Poll, secrets, args, result)
		if err != nil {
			return Output{}, safeErr(err)
		}
		if t.Poll.TextPath != "" {
			t.TextPath = t.Poll.TextPath
		}
		if t.Poll.MediaURLPath != "" {
			t.MediaURLPath = t.Poll.MediaURLPath
		}
	}
	output := extract(t, result)
	if output.Text == "" && len(output.Media) == 0 {
		return Output{}, errors.New("API returned no configured output")
	}
	return output, nil
}
func poll(ctx context.Context, t APITool, p *PollSpec, secrets map[string]string, args, result any) (any, error) {
	status := stringAt(result, p.StatusPath)
	if contains(p.DoneValues, status) {
		return result, nil
	}
	if contains(p.FailedValues, status) {
		return nil, errors.New("remote task failed")
	}
	tries := p.MaxAttempts
	if tries == 0 {
		tries = 12
	}
	pause := p.IntervalSeconds
	if pause == 0 {
		pause = 2
	}
	if p.Method == "" {
		p.Method = http.MethodGet
	}
	// Resolve once against the creation response: pending responses often omit the task ID.
	pollURL, err := renderURL(p.URL, secrets, args, result)
	if err != nil {
		return nil, err
	}
	headers := p.Headers
	if headers == nil {
		origin, _ := url.Parse(t.URL)
		target, _ := url.Parse(pollURL)
		if origin.Scheme == target.Scheme && origin.Host == target.Host {
			headers = t.Headers
		}
	}
	for i := 0; i < tries; i++ {
		timer := time.NewTimer(time.Duration(pause) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		b, ct, err := request(ctx, pollURL, p.Method, headers, nil, secrets, args, nil, t.TimeoutSeconds)
		if err != nil {
			return nil, err
		}
		if !isJSON(ct, b) {
			return nil, errors.New("poll returned non-JSON")
		}
		result, err = decodeResult(b)
		if err != nil {
			return nil, errors.New("poll returned invalid JSON")
		}
		status = stringAt(result, p.StatusPath)
		if contains(p.DoneValues, status) {
			return result, nil
		}
		if contains(p.FailedValues, status) {
			return nil, errors.New("remote task failed")
		}
	}
	return nil, errors.New("remote task did not finish")
}
func request(ctx context.Context, raw, method string, headers map[string]string, body any, secrets map[string]string, args, result any, timeoutSeconds int) ([]byte, string, error) {
	u, err := renderURL(raw, secrets, args, result)
	if err != nil {
		return nil, "", err
	}
	var rd io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return nil, "", e
		}
		rd = bytes.NewReader(b)
	}
	if method == "" {
		method = http.MethodPost
	}
	if timeoutSeconds == 0 {
		timeoutSeconds = 60
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, method, u, rd)
	if e != nil {
		return nil, "", e
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		x, e := renderString(v, secrets, args, result)
		if e != nil {
			return nil, "", e
		}
		req.Header.Set(k, x)
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects are not allowed") }}
	resp, e := client.Do(req)
	if e != nil {
		return nil, "", e
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, "", fmt.Errorf("API returned HTTP status %d", resp.StatusCode)
	}
	limit := int64(maxJSON)
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "json") {
		limit = maxBinary
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if e != nil {
		return nil, "", e
	}
	if int64(len(b)) > limit {
		return nil, "", errors.New("API response too large")
	}
	return b, resp.Header.Get("Content-Type"), nil
}

func renderBody(raw json.RawMessage, s map[string]string, a, r any) (any, error) {
	if len(raw) == 0 {
		return a, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return renderValue(v, s, a, r)
}
func renderValue(v any, s map[string]string, a, r any) (any, error) {
	switch x := v.(type) {
	case string:
		if m := placeholder.FindStringSubmatch(x); m != nil && m[0] == x {
			return lookup(m[1], m[2], s, a, r)
		}
		return renderString(x, s, a, r)
	case []any:
		for i := range x {
			z, e := renderValue(x[i], s, a, r)
			if e != nil {
				return nil, e
			}
			x[i] = z
		}
	case map[string]any:
		for k := range x {
			z, e := renderValue(x[k], s, a, r)
			if e != nil {
				return nil, e
			}
			x[k] = z
		}
	}
	return v, nil
}
func renderString(x string, s map[string]string, a, r any) (string, error) {
	var first error
	out := placeholder.ReplaceAllStringFunc(x, func(m string) string {
		q := placeholder.FindStringSubmatch(m)
		v, e := lookup(q[1], q[2], s, a, r)
		if e != nil {
			first = e
			return ""
		}
		switch z := v.(type) {
		case string:
			return z
		default:
			b, _ := json.Marshal(z)
			return string(b)
		}
	})
	return out, first
}
func lookup(scope, key string, s map[string]string, a, r any) (any, error) {
	if scope == "secret" {
		v, ok := s[key]
		if !ok {
			return nil, errors.New("required secret is not configured")
		}
		return v, nil
	}
	var v any
	if scope == "arg" {
		v = a
	} else {
		v = r
	}
	for _, p := range strings.Split(key, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("template field is unavailable")
		}
		v, ok = m[p]
		if !ok {
			return nil, errors.New("template field is unavailable")
		}
	}
	return v, nil
}
func renderURL(raw string, s map[string]string, a, r any) (string, error) {
	if strings.Contains(raw, "{{secret.") || strings.Contains(raw, "{{arg.") {
		return "", errors.New("API URL cannot use secret or argument placeholders")
	}
	u, e := url.Parse(raw)
	if e != nil {
		return "", e
	}
	if u.Host == "" || placeholder.MatchString(u.Host) || placeholder.MatchString(u.RawQuery) {
		return "", errors.New("invalid API URL")
	}
	if strings.Contains(u.Path, "{{result.") {
		var missing error
		u.Path = placeholder.ReplaceAllStringFunc(u.Path, func(m string) string {
			q := placeholder.FindStringSubmatch(m)
			v, e := lookup(q[1], q[2], s, a, r)
			if e != nil {
				missing = e
				return ""
			}
			return url.PathEscape(fmt.Sprint(v))
		})
		if missing != nil || strings.Contains(u.Path, "{{") {
			return "", errors.New("missing task identifier")
		}
	}
	if err := validEndpoint(u.String(), false); err != nil {
		return "", err
	}
	return u.String(), nil
}
func validEndpoint(raw string, poll bool) error {
	u, e := url.Parse(raw)
	if e != nil {
		return e
	}
	if u.User != nil || u.Fragment != "" || u.Hostname() == "" {
		return errors.New("bad endpoint")
	}
	if strings.Contains(raw, "{{") { // only poll result markers in path are permitted
		if !poll || strings.Contains(u.Host, "{{") || strings.Contains(u.RawQuery, "{{") || !strings.Contains(u.Path, "{{result.") {
			return errors.New("bad endpoint template")
		}
		clean := placeholder.ReplaceAllString(raw, "task-id")
		if strings.Contains(clean, "{{") {
			return errors.New("invalid URL placeholder")
		}
		return validEndpoint(clean, false)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && loopback(u.Hostname()) {
		return nil
	}
	return errors.New("endpoint must be HTTPS or loopback HTTP")
}
func loopback(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}
func decodeResult(b []byte) (any, error) { var v any; err := json.Unmarshal(b, &v); return v, err }
func isJSON(ct string, b []byte) bool {
	return strings.Contains(strings.ToLower(ct), "json") || json.Valid(b)
}
func at(v any, p string) any {
	for _, x := range strings.Split(p, ".") {
		if x == "" {
			continue
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[x]
	}
	return v
}
func stringAt(v any, p string) string {
	x := at(v, p)
	if x == nil {
		return ""
	}
	return fmt.Sprint(x)
}
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func extract(t APITool, v any) Output {
	o := Output{}
	if t.TextPath != "" {
		o.Text = stringAt(v, t.TextPath)
	}
	if t.MediaURLPath != "" {
		if x := stringAt(v, t.MediaURLPath); x != "" && x != "<nil>" {
			o.Media = []MediaResult{{Kind: t.MediaKind, URL: x}}
		}
	}
	return o
}
func safeErr(error) error { return errors.New("plugin API call failed") }

// Templates are inert starter descriptors. The owner must configure the URL
// and credentials before enabling a template.
func Templates() []Manifest {
	return []Manifest{
		{ID: "text_to_speech", Name: "文字转语音 API", Version: "1", Description: "需配置：填写你的语音 API 地址、鉴权请求头和返回音频 URL 路径。", Tools: []APITool{{Name: "synthesize", Description: "将文字交给已配置的语音服务。", InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`), URL: "https://replace-with-owner-api.example/v1/tts", Method: "POST", Headers: map[string]string{"Authorization": "Bearer {{secret.API_KEY}}"}, BodyTemplate: json.RawMessage(`{"text":"{{arg.text}}"}`), MediaURLPath: "data.audio_url", MediaKind: "voice"}}},
		{ID: "music_singing", Name: "音乐/歌声任务 API", Version: "1", Description: "需配置：填写音乐或歌声生成任务 API、状态轮询路径及媒体 URL 字段。", Tools: []APITool{{Name: "create_song", Description: "创建已配置的音乐/歌声任务。", InputSchema: json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string"}},"required":["prompt"]}`), URL: "https://replace-with-owner-api.example/v1/tasks", Method: "POST", BodyTemplate: json.RawMessage(`{"prompt":"{{arg.prompt}}"}`), MediaKind: "voice", Poll: &PollSpec{URL: "https://replace-with-owner-api.example/v1/tasks/{{result.task_id}}", StatusPath: "status", DoneValues: []string{"done"}, IntervalSeconds: 30, MaxAttempts: 12, MediaURLPath: "data.audio_url"}}}},
		{ID: "video_generation", Name: "视频生成任务 API", Version: "1", Description: "需配置：填写视频生成服务 API、轮询状态和视频 URL 字段。", Tools: []APITool{{Name: "create_video", Description: "创建已配置的视频生成任务。", InputSchema: json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string"}},"required":["prompt"]}`), URL: "https://replace-with-owner-api.example/v1/tasks", Method: "POST", BodyTemplate: json.RawMessage(`{"prompt":"{{arg.prompt}}"}`), MediaKind: "video", Poll: &PollSpec{URL: "https://replace-with-owner-api.example/v1/tasks/{{result.task_id}}", StatusPath: "status", DoneValues: []string{"done"}, IntervalSeconds: 30, MaxAttempts: 12, MediaURLPath: "data.video_url"}}}},
		{ID: "external_mcp", Name: "外部 MCP 服务", Version: "1", Description: "需配置：这是外部 MCP 服务连接模板；地址、鉴权与允许工具均由管理员填写。", MCP: &mcptools.ServerConfig{ID: "external_mcp", Name: "外部 MCP 服务", Transport: "http", URL: "https://replace-with-owner-mcp.example/mcp"}},
	}
}
