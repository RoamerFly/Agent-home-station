// Package mcptools provides a deliberately small, bounded bridge to MCP tools.
// It is intended for use by a conversational application, not as a general
// purpose network proxy: commands and endpoints are configured by the owner,
// never selected by a model.
package mcptools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	protocolVersion = "2025-06-18"
	maxFrame        = 1 << 20
	maxBody         = 2 << 20
	requestTimeout  = 20 * time.Second
)

var toolName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
var headerName = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// ServerConfig is an owner-controlled MCP endpoint. Transport is "stdio" or
// "http" ("streamable-http" is accepted as an alias). Tools is an allowlist.
type ServerConfig struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	URL       string            `json:"url"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env"`
	// Headers are owner-configured HTTP request headers, for example an
	// Authorization bearer token. They are never model-controlled.
	Headers        map[string]string `json:"headers"`
	Enabled        bool              `json:"enabled"`
	Tools          []string          `json:"tools"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type Spec Tool

// BuiltinTools exposes only the local, deterministic schemas, without starting
// or contacting an external MCP server.
func BuiltinTools() []Tool { return builtin() }

type Hub struct {
	servers  []ServerConfig
	mu       sync.Mutex
	sessions map[string]*mcpSession
	closed   bool
}

func New(config []ServerConfig) (*Hub, error) {
	seen := map[string]bool{}
	out := make([]ServerConfig, 0, len(config))
	for _, c := range config {
		if c.TimeoutSeconds < 0 || c.TimeoutSeconds > 900 {
			return nil, errors.New("MCP timeout must be between 0 and 900 seconds")
		}
		if !c.Enabled {
			continue
		}
		if !toolName.MatchString(c.ID) {
			return nil, fmt.Errorf("invalid MCP server id %q", c.ID)
		}
		if strings.Contains(c.ID, "__") {
			return nil, fmt.Errorf("MCP server id %q must not contain __", c.ID)
		}
		if seen[c.ID] {
			return nil, fmt.Errorf("duplicate MCP server id %q", c.ID)
		}
		seen[c.ID] = true
		c.Transport = strings.ToLower(strings.TrimSpace(c.Transport))
		switch c.Transport {
		case "stdio":
			if strings.TrimSpace(c.Command) == "" {
				return nil, fmt.Errorf("MCP server %q has no command", c.ID)
			}
		case "http", "streamable-http":
			if err := validMCPURL(c.URL); err != nil {
				return nil, fmt.Errorf("MCP server %q: %w", c.ID, err)
			}
			c.Transport = "http"
		default:
			return nil, fmt.Errorf("MCP server %q has unsupported transport %q", c.ID, c.Transport)
		}
		for _, n := range c.Tools {
			if !toolName.MatchString(n) {
				return nil, fmt.Errorf("invalid allowed tool %q", n)
			}
		}
		for k, v := range c.Headers {
			if !headerName.MatchString(k) || strings.ContainsAny(v, "\r\n") {
				return nil, fmt.Errorf("invalid HTTP header for MCP server %q", c.ID)
			}
		}
		out = append(out, c)
	}
	return &Hub{servers: out, sessions: map[string]*mcpSession{}}, nil
}

func builtin() []Tool {
	return []Tool{
		{Name: "web_audio_search", Description: "Search public music catalogs (Incompetech, then Internet Archive) and resolve real downloadable audio candidates; many catalog tracks are instrumental, not mainstream original recordings. Input {query:string}. Use song title/artist or music style; translate broad styles to English when appropriate. Does not download or send. Never substitute a different recording for a requested exact song.", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","minLength":1,"maxLength":200}},"required":["query"],"additionalProperties":false}`)},
		{Name: "web_search", Description: "Search public web pages. Input: {query:string}.", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","minLength":1,"maxLength":512}},"required":["query"],"additionalProperties":false}`)},
		{Name: "web_fetch", Description: "Fetch and extract readable text from a public HTTP(S) page. Input: {url:string}.", InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","format":"uri","maxLength":2048}},"required":["url"],"additionalProperties":false}`)},
	}
}

// List returns built-ins even if a configured external server is unavailable.
// In that case it also returns the first external error.
func (h *Hub) List(ctx context.Context) ([]Tool, error) {
	all := builtin()
	var first error
	for _, c := range h.servers {
		ts, err := h.serverList(ctx, c)
		if err != nil {
			if first == nil {
				first = fmt.Errorf("MCP %s: %w", c.ID, err)
			}
			continue
		}
		for _, t := range ts {
			if !toolName.MatchString(t.Name) || !allowed(c, t.Name) {
				continue
			}
			t.Name = c.ID + "__" + t.Name
			all = append(all, t)
		}
	}
	return all, first
}

func (h *Hub) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if !json.Valid(args) {
		return "", errors.New("tool arguments must be JSON")
	}
	switch name {
	case "web_audio_search":
		return webAudioSearch(ctx, args)
	case "web_search":
		return webSearch(ctx, args)
	case "web_fetch":
		return webFetch(ctx, args)
	}
	parts := strings.SplitN(name, "__", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	for _, c := range h.servers {
		if c.ID == parts[0] {
			if !allowed(c, parts[1]) {
				return "", errors.New("tool is not allowed")
			}
			return h.serverCall(ctx, c, parts[1], args)
		}
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

func (h *Hub) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for _, s := range h.sessions {
		s.close()
	}
	h.sessions = map[string]*mcpSession{}
	return nil
}
func allowed(c ServerConfig, name string) bool {
	if len(c.Tools) == 0 {
		return true
	}
	for _, x := range c.Tools {
		if x == name {
			return true
		}
	}
	return false
}

func (h *Hub) serverList(ctx context.Context, c ServerConfig) ([]Tool, error) {
	out := []Tool{}
	cursor := ""
	seen := map[string]bool{}
	bytes := 0
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var raw json.RawMessage
		if err := h.request(ctx, c, "tools/list", params, &raw); err != nil {
			return nil, err
		}
		bytes += len(raw)
		if bytes > 32<<20 {
			return nil, errors.New("MCP tool catalogue exceeds32MiB transport budget")
		}
		var r struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
			Next string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		for _, t := range r.Tools {
			out = append(out, Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
		}
		if r.Next == "" {
			return out, nil
		}
		if seen[r.Next] {
			return nil, errors.New("MCP tools/list repeated a pagination cursor")
		}
		seen[r.Next] = true
		cursor = r.Next
	}
}
func (h *Hub) serverCall(ctx context.Context, c ServerConfig, n string, args json.RawMessage) (string, error) {
	var raw json.RawMessage
	p := map[string]any{"name": n, "arguments": json.RawMessage(args)}
	if err := h.request(ctx, c, "tools/call", p, &raw); err != nil {
		return "", err
	}
	var r struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Data     string `json:"data"`
			MIMEType string `json:"mimeType"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", err
	}
	var b strings.Builder
	media := []map[string]string{}
	for _, x := range r.Content {
		if x.Type == "image" || x.Type == "audio" {
			kind := "image"
			if x.Type == "audio" {
				kind = "voice"
			}
			if len(media) >= 3 {
				return "", errors.New("MCP returned too many media items")
			}
			media = append(media, map[string]string{"kind": kind, "mime_type": x.MIMEType, "data_base64": x.Data})
		}
		if x.Type == "text" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(x.Text)
			if b.Len() >= 16*1024 {
				out := trimBytes(b.String(), 16*1024)
				b.Reset()
				b.WriteString(out)
			}
		}
	}
	out := trimBytes(b.String(), 16*1024)
	if r.IsError {
		return out, errors.New("MCP tool reported an error")
	}
	if len(media) > 0 {
		encoded, _ := json.Marshal(map[string]any{"text": out, "media": media})
		return string(encoded), nil
	}
	return out, nil
}
func (h *Hub) request(ctx context.Context, c ServerConfig, method string, params any, result *json.RawMessage) error {
	if scope, _ := ctx.Value(sessionScopeKey{}).(string); scope != "" {
		return h.scopedRequest(ctx, c, scope, method, params, result)
	}
	if c.Transport == "stdio" {
		return stdioRequest(ctx, c, method, params, result)
	}
	return httpRequest(ctx, c, method, params, result)
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func rpcErr(r rpcResponse) error {
	if r.Error != nil {
		// Remote error messages are untrusted and can accidentally include a
		// configured credential. Keep diagnostics safe for application logs.
		return fmt.Errorf("MCP RPC error (%d)", r.Error.Code)
	}
	return nil
}
func initParams() map[string]any {
	return map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "wechat-bot", "version": "1"}}
}

func stdioRequest(ctx context.Context, c ServerConfig, method string, params any, result *json.RawMessage) (retErr error) {
	ctx, cancel := serverTimeout(ctx, c)
	cmd := exec.CommandContext(ctx, c.Command, c.Args...)
	cmd.WaitDelay = 2 * time.Second
	// nil connects directly to the null device. An io.Discard writer would
	// create a copy pipe which inherited descendants could keep alive.
	cmd.Stderr = nil
	env := os.Environ()
	for k, v := range c.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	// A descendant may inherit stdout even after the configured process is
	// killed. Close our pipe handles at cancellation so readRPC cannot wait
	// forever on that descendant's still-open descriptor.
	stop := context.AfterFunc(ctx, func() {
		_ = in.Close()
		_ = out.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	// Some MCP servers deliberately stay alive after stdin closes. Cancel and
	// kill before waiting so a successful short-lived request cannot stall until
	// the request deadline merely because such a server ignores EOF.
	defer func() {
		cancelled := ctx.Err()
		stop()
		cancel()
		_ = in.Close()
		_ = out.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		if cancelled != nil && retErr != nil {
			retErr = cancelled
		}
	}()
	r := bufio.NewReaderSize(out, maxFrame+1)
	w := bufio.NewWriter(in)
	call := func(id int, m string, p any, reply bool) (rpcResponse, error) {
		b, e := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: m, Params: p})
		if e != nil {
			return rpcResponse{}, e
		}
		if len(b) > maxFrame {
			return rpcResponse{}, errors.New("MCP request too large")
		}
		if _, e = w.Write(append(b, '\n')); e != nil {
			return rpcResponse{}, e
		}
		if e = w.Flush(); e != nil {
			return rpcResponse{}, e
		}
		if !reply {
			return rpcResponse{}, nil
		}
		return readRPC(r, id)
	}
	rsp, err := call(1, "initialize", initParams(), true)
	if err != nil {
		return err
	}
	if err = rpcErr(rsp); err != nil {
		return err
	}
	if _, err = call(0, "notifications/initialized", map[string]any{}, false); err != nil {
		return err
	}
	rsp, err = call(2, method, params, true)
	if err != nil {
		return err
	}
	if err = rpcErr(rsp); err != nil {
		return err
	}
	*result = rsp.Result
	return nil
}
func readRPC(r *bufio.Reader, id int) (rpcResponse, error) {
	for i := 0; i < 20; i++ {
		line, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			return rpcResponse{}, errors.New("MCP frame exceeds 1 MiB")
		}
		if err != nil {
			return rpcResponse{}, err
		}
		var v rpcResponse
		if json.Unmarshal(bytes.TrimSpace(line), &v) != nil || v.ID != id {
			continue
		}
		return v, nil
	}
	return rpcResponse{}, errors.New("no matching MCP response")
}

func httpRequest(ctx context.Context, c ServerConfig, method string, params any, result *json.RawMessage) error {
	ctx, cancel := serverTimeout(ctx, c)
	defer cancel()
	client, err := mcpClient(c.URL)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	session := ""
	do := func(id int, m string, p any, want bool) (rpcResponse, error) {
		b, e := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: m, Params: p})
		if e != nil {
			return rpcResponse{}, e
		}
		req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(b))
		if e != nil {
			return rpcResponse{}, e
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", protocolVersion)
		for k, v := range c.Headers {
			req.Header.Set(k, v)
		}
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		resp, e := client.Do(req)
		if e != nil {
			return rpcResponse{}, e
		}
		defer resp.Body.Close()
		if got := resp.Header.Get("Mcp-Session-Id"); got != "" {
			session = got
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return rpcResponse{}, fmt.Errorf("MCP HTTP status %s", resp.Status)
		}
		if !want {
			return rpcResponse{}, nil
		}
		return readHTTPRPC(resp.Body, id)
	}
	rsp, err := do(1, "initialize", initParams(), true)
	if err != nil {
		return err
	}
	if err = rpcErr(rsp); err != nil {
		return err
	}
	if _, err = do(0, "notifications/initialized", map[string]any{}, false); err != nil {
		return err
	}
	rsp, err = do(2, method, params, true)
	if err != nil {
		return err
	}
	if err = rpcErr(rsp); err != nil {
		return err
	}
	*result = rsp.Result
	return nil
}
func readHTTPRPC(body io.Reader, id int) (rpcResponse, error) {
	br := bufio.NewReaderSize(body, maxFrame+1)
	var first []byte
	var err error
	for {
		first, err = br.Peek(1)
		if err != nil {
			return rpcResponse{}, err
		}
		if !strings.ContainsRune(" \t\r\n", rune(first[0])) {
			break
		}
		_, _ = br.ReadByte()
	}
	if first[0] == '{' || first[0] == '[' {
		var r rpcResponse
		if err := json.NewDecoder(&boundedReader{r: br, left: maxFrame}).Decode(&r); err != nil {
			return rpcResponse{}, err
		}
		if r.ID == id {
			return r, nil
		}
		return rpcResponse{}, errors.New("no matching MCP HTTP response")
	}
	// A streamable HTTP server may keep an SSE connection open indefinitely.
	// Consume event lines and return as soon as the matching complete event is
	// available instead of waiting for the server to close the connection.
	s := bufio.NewScanner(br)
	s.Buffer(make([]byte, 4096), maxFrame)
	var data strings.Builder
	for s.Scan() {
		line := s.Text()
		if line == "" {
			if data.Len() == 0 {
				continue
			}
			payload := data.String()
			data.Reset()
			if payload == "[DONE]" {
				continue
			}
			var r rpcResponse
			if json.Unmarshal([]byte(payload), &r) == nil && r.ID == id {
				return r, nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			v := strings.TrimPrefix(line, "data:")
			if strings.HasPrefix(v, " ") {
				v = v[1:]
			}
			if data.Len()+len(v)+1 > maxFrame {
				return rpcResponse{}, errors.New("MCP response exceeds 1 MiB")
			}
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(v)
		}
	}
	if err := s.Err(); err != nil {
		return rpcResponse{}, err
	}
	return rpcResponse{}, errors.New("no matching MCP HTTP response")
}

type boundedReader struct {
	r    io.Reader
	left int
}

func (r *boundedReader) Read(p []byte) (int, error) {
	if r.left <= 0 {
		return 0, errors.New("MCP response exceeds 1 MiB")
	}
	if len(p) > r.left {
		p = p[:r.left]
	}
	n, e := r.r.Read(p)
	r.left -= n
	return n, e
}

func validMCPURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil {
		return e
	}
	if u.User != nil || u.Fragment != "" || u.Hostname() == "" {
		return errors.New("MCP URL must not contain credentials or fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && isLiteralLoopback(u.Hostname()) {
		return nil
	}
	return errors.New("MCP URL must use HTTPS (HTTP is allowed only for literal loopback)")
}
func isLiteralLoopback(host string) bool {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
func mcpClient(raw string) (*http.Client, error) {
	if err := validMCPURL(raw); err != nil {
		return nil, err
	}
	// The request context carries the per-server deadline, including long generation calls.
	return &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return errors.New("MCP redirects are not allowed") }}, nil
}
func serverTimeout(ctx context.Context, c ServerConfig) (context.Context, context.CancelFunc) {
	timeout := requestTimeout
	if c.TimeoutSeconds > 0 {
		timeout = time.Duration(c.TimeoutSeconds) * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}

type searchInput struct {
	Query string `json:"query"`
}

func webSearch(ctx context.Context, args json.RawMessage) (string, error) {
	var in searchInput
	if err := json.Unmarshal(args, &in); err != nil || strings.TrimSpace(in.Query) == "" || len(in.Query) > 512 {
		return "", &WebError{Code: "query_required", Stage: "arguments"}
	}
	return searchPublic(ctx, strings.TrimSpace(in.Query), func(ctx context.Context, raw string) ([]byte, error) { b, _, err := publicGet(ctx, raw); return b, err })
}
func parseBingSearch(body []byte) ([]searchResult, error) {
	var feed struct {
		Channel struct {
			Items []struct {
				Title       string `xml:"title"`
				Link        string `xml:"link"`
				Description string `xml:"description"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, &WebError{Code: "search_invalid", Stage: "decode"}
	}
	out := []searchResult{}
	for i, x := range feed.Channel.Items {
		if i == 8 {
			break
		}
		if validPublicURL(strings.TrimSpace(x.Link)) == nil {
			out = append(out, searchResult{cleanText(x.Title), strings.TrimSpace(x.Link), cleanText(x.Description)})
		}
	}
	return out, nil
}

type fetchInput struct {
	URL string `json:"url"`
}

func webFetch(ctx context.Context, args json.RawMessage) (string, error) {
	var in fetchInput
	if err := json.Unmarshal(args, &in); err != nil {
		return "", &WebError{Code: "url_required"}
	}
	in.URL = strings.TrimSpace(in.URL)
	if in.URL == "" {
		return "", &WebError{Code: "url_required"}
	}
	if len(in.URL) > 2048 {
		return "", errors.New("web_fetch URL exceeds 2048 characters")
	}
	if err := validPublicURL(in.URL); err != nil {
		return "", err
	}
	if u, err := url.Parse(in.URL); err == nil && u.Hostname() == "api.deepseek.com" && (strings.TrimRight(u.Path, "/") == "/v1/tools/search" || strings.TrimRight(u.Path, "/") == "/v1/tools/fetch") {
		return "", &WebError{Code: "authenticated_service_endpoint"}
	}
	if text, handled, err := discoveryListPage(ctx, in.URL); handled {
		return text, err
	}
	body, contentType, final, err := publicRead(ctx, in.URL, maxBody)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(contentType, "text/") && !strings.Contains(contentType, "json") && !strings.Contains(contentType, "xml") && contentType != "" {
		return "", &WebError{Code: "not_text"}
	}
	s := string(body)
	title := ""
	if m := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`).FindStringSubmatch(s); len(m) > 1 {
		title = cleanText(m[1])
	}
	s = extractHTMLText(s)
	if s == "" {
		return "", &WebError{Code: "no_readable_text"}
	}
	out := "Source: " + final + "\n"
	if title != "" {
		out += "Title: " + title + "\n"
	}
	out += "\n" + trimBytes(s, 10000)
	if links := ExtractMediaLinks(string(body), final); len(links) > 0 {
		out += "\nPublic media candidates (not yet downloaded):\n" + strings.Join(links, "\n")
	}
	return trimBytes(out, 12000), nil
}

var unsafeHTML = []*regexp.Regexp{
	regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`),
	regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style\s*>`),
	regexp.MustCompile(`(?is)<noscript\b[^>]*>.*?</noscript\s*>`),
}
var htmlTags = regexp.MustCompile(`(?is)<[^>]+>`)

func extractHTMLText(s string) string {
	for _, re := range unsafeHTML {
		s = re.ReplaceAllString(s, " ")
	}
	return cleanText(htmlTags.ReplaceAllString(s, " "))
}
func cleanText(s string) string {
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}

func validPublicURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil {
		return &WebError{Code: "invalid_url"}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return &WebError{Code: "invalid_url"}
	}
	if u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return &WebError{Code: "invalid_url"}
	}
	return nil
}
func publicGet(ctx context.Context, raw string) ([]byte, string, error) {
	b, _, final, err := publicRead(ctx, raw, maxBody)
	return b, final, err
}

// MaxPublicMediaBytes matches the file attachment capacity. It bounds actual
// buffered content, not the size of every request or a minimum required size.
const MaxPublicMediaBytes int64 = 512 << 20

// GetPublicMedia downloads public media through the same SSRF protections as
// web_fetch. The caller supplies its media-specific byte budget.
func GetPublicMedia(ctx context.Context, raw string, maxBytes int64) ([]byte, string, error) {
	if maxBytes <= 0 || maxBytes > MaxPublicMediaBytes {
		return nil, "", &WebError{Code: "media_limit_invalid"}
	}
	return downloadPublicMedia(ctx, raw, maxBytes)
}

func publicRead(ctx context.Context, raw string, maxBytes int64) ([]byte, string, string, error) {
	return publicReadTimeout(ctx, raw, maxBytes, requestTimeout)
}
func publicReadTimeout(ctx context.Context, raw string, maxBytes int64, timeout time.Duration) ([]byte, string, string, error) {
	b, t, u, _, e := publicReadRange(ctx, raw, maxBytes, timeout, "", "")
	return b, t, u, e
}
func newPublicTransport() *http.Transport {
	d := &net.Dialer{Timeout: 4 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{Proxy: nil, TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 12 * time.Second, MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil {
			return nil, e
		}
		if len(ips) == 0 {
			return nil, errors.New("host did not resolve")
		}
		for _, ip := range ips {
			if privateIP(ip.IP) {
				return nil, &WebError{Code: "private_destination"}
			}
		}
		// Do not give the original hostname back to the dialer: that would do a
		// second DNS lookup after validation and enable DNS rebinding.
		var last error
		for _, ip := range ips {
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
			if ctx.Err() != nil {
				break
			}
		}
		return nil, last
	}}
	return tr
}
func publicReadRangeOnce(ctx context.Context, raw string, maxBytes int64, timeout time.Duration, byteRange, validator string) ([]byte, string, string, rangeHeaders, error) {
	if err := validPublicURL(raw); err != nil {
		return nil, "", "", rangeHeaders{}, err
	}
	tr := newPublicTransport()
	if session, ok := ctx.Value(publicSessionKey{}).(*publicReadSession); ok {
		if session.Transport != nil {
			tr = session.Transport
		} else {
			session.Transport = tr
		}
	} else {
		defer tr.CloseIdleConnections()
	}
	client := &http.Client{Transport: tr, Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return &WebError{Code: "redirect_limit"}
		}
		if enabled, _ := ctx.Value(publicVideoRefererKey{}).(bool); enabled && !strings.HasSuffix(req.URL.Hostname(), ".bilivideo.com") {
			return &WebError{Code: "video_public_playback_unavailable", Stage: "video_redirect"}
		}
		return validPublicURL(req.URL.String())
	}}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if e != nil {
		return nil, "", "", rangeHeaders{}, e
	}
	req.Header.Set("User-Agent", "wechat-bot-web-tool/1.0")
	if enabled, _ := ctx.Value(kuaishouRequestKey{}).(bool); enabled {
		host := req.URL.Hostname()
		if host == "www.kuaishou.com" || host == "kuaishou.com" || host == "v.m.chenzhongtech.com" || kuaishouMediaHost(host) {
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36")
			req.Header.Set("Referer", "https://www.kuaishou.com/")
		}
	}
	if enabled, _ := ctx.Value(publicVideoRefererKey{}).(bool); enabled && strings.HasSuffix(req.URL.Hostname(), ".bilivideo.com") {
		req.Header.Set("Referer", "https://www.bilibili.com/")
	}
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
		req.Header.Set("Accept-Encoding", "identity")
	}
	if validator != "" {
		req.Header.Set("If-Range", validator)
	}
	resp, e := client.Do(req)
	if e != nil {
		return nil, "", "", rangeHeaders{}, classifyWeb(e, "connection_or_headers")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, "", "", rangeHeaders{}, &WebError{Code: "http_status", Status: resp.StatusCode, Stage: "http_status", RetryAfter: retryAfter(resp.Header.Get("Retry-After"))}
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if e != nil {
		return nil, "", "", rangeHeaders{}, classifyWeb(e, "response_body")
	}
	if int64(len(b)) > maxBytes {
		return nil, "", "", rangeHeaders{}, &WebError{Code: "response_too_large"}
	}
	return b, resp.Header.Get("Content-Type"), resp.Request.URL.String(), rangeHeaders{ContentRange: resp.Header.Get("Content-Range"), Validator: resp.Header.Get("ETag"), Status: resp.StatusCode}, nil
}
func privateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
}
func trimBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	b := []byte(s)[:n]
	for !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(b) + "\n[truncated]"
}
