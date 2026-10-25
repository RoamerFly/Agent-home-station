package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Summary string `json:"summary"`
	Snippet string `json:"snippet"`
}

// Only the public query is sent, to the same configured official API origin.
// No conversation, original question, device data or localization is included.
func (c *Client) Search(ctx context.Context, query string) ([]SearchResult, error) {
	u, err := url.Parse(c.endpoint)
	if err != nil || u.Scheme != "https" || u.Host != "api.deepseek.com" || (u.Path != "/chat/completions" && u.Path != "/v1/chat/completions") {
		return nil, errors.New("search_unsupported")
	}
	if strings.TrimSpace(query) == "" || utf8.RuneCountInString(query) > 128 {
		return nil, errors.New("search_query_invalid")
	}
	u.Path = "/v1/tools/search"
	body, _ := json.Marshal(map[string]any{"query": query, "count": 6, "need_content": false})
	ctx, cancel := context.WithTimeout(ctx, 18*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("search_request_invalid")
	}
	r.Header.Set("Authorization", "Bearer "+c.key)
	r.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(r)
	if err != nil {
		return nil, errors.New("search_connection_failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, requestFailure("official_search_http_error", "search", response.StatusCode, false)
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, 128*1024+1))
	if err != nil || len(b) > 128*1024 {
		return nil, errors.New("search_response_invalid")
	}
	var result struct {
		Results []SearchResult `json:"results"`
	}
	if json.Unmarshal(b, &result) != nil || len(result.Results) > 50 {
		return nil, errors.New("search_response_invalid")
	}
	return result.Results, nil
}
