// Package websearch contains compatibility adapters for configured search
// providers that have not yet been migrated into an installed plugin.
//
// It is deliberately narrow: callers supply only an endpoint and key, and the
// adapter sends only the public search query. It owns no account configuration,
// task state, chat context, or delivery logic. New providers belong in a plugin.
package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// Config is the minimum legacy account selection needed by the DeepSeek search
// protocol. HTTP is optional and is intended for offline contract tests.
type Config struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Summary string `json:"summary"`
	Snippet string `json:"snippet"`
}

type Diagnostics struct {
	Code       string
	HTTPStatus int
}

type requestError struct {
	code       string
	httpStatus int
}

func (e *requestError) Error() string { return e.code }

// ErrorDiagnostics exposes only safe operation metadata for activity records.
func ErrorDiagnostics(err error) (Diagnostics, bool) {
	var failure *requestError
	if !errors.As(err, &failure) {
		return Diagnostics{}, false
	}
	return Diagnostics{Code: failure.code, HTTPStatus: failure.httpStatus}, true
}

func requestFailure(code string, status int) error {
	return &requestError{code: code, httpStatus: status}
}

func defaultHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 90 * time.Second,
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			IdleConnTimeout:     90 * time.Second,
			MaxIdleConns:        8,
			MaxIdleConnsPerHost: 2,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect denied") },
	}
}

func endpoint(baseURL string) (*url.URL, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || u.Host != "api.deepseek.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/" && u.Path != "/v1" && u.Path != "/v1/") {
		return nil, errors.New("search_unsupported")
	}
	u.Path = "/v1/tools/search"
	return u, nil
}

func validKey(key string) bool {
	key = strings.TrimSpace(key)
	return len(key) >= 16 && utf8.ValidString(key) && !strings.ContainsAny(key, "\r\n\x00 ")
}

// Supported reports whether this exact legacy provider selection can be used
// without contacting a network. It lets callers preserve the old behavior of
// falling through when model credentials were not usable.
func Supported(config Config) bool {
	_, err := endpoint(config.BaseURL)
	return err == nil && validKey(config.APIKey)
}

// Search preserves the legacy DeepSeek paid-search request protocol. It is
// intentionally not a generic provider fallback: an unsupported provider is
// reported to the caller, which may then use its pre-existing public route.
func Search(ctx context.Context, config Config, query string) ([]SearchResult, error) {
	u, err := endpoint(config.BaseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(query) == "" || utf8.RuneCountInString(query) > 128 {
		return nil, errors.New("search_query_invalid")
	}
	if !validKey(config.APIKey) {
		return nil, requestFailure("official_search_key_missing", 0)
	}
	key := strings.TrimSpace(config.APIKey)
	body, _ := json.Marshal(map[string]any{"query": query, "count": 6, "need_content": false})
	ctx, cancel := context.WithTimeout(ctx, 18*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("search_request_invalid")
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	client := config.HTTP
	if client == nil {
		client = defaultHTTPClient()
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("search_connection_failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		code := "official_search_http_error"
		if response.StatusCode == http.StatusUnauthorized {
			code = "official_search_authentication_failed"
		}
		return nil, requestFailure(code, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 128*1024+1))
	if err != nil || len(data) > 128*1024 {
		return nil, errors.New("search_response_invalid")
	}
	var result struct {
		Results []SearchResult `json:"results"`
	}
	if json.Unmarshal(data, &result) != nil || len(result.Results) > 50 {
		return nil, errors.New("search_response_invalid")
	}
	return result.Results, nil
}
