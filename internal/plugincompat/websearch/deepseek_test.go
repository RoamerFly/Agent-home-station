package websearch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDeepSeekSearchUsesOnlyOfficialEndpointAndQuery(t *testing.T) {
	called := 0
	client := &http.Client{Transport: transport(func(request *http.Request) (*http.Response, error) {
		called++
		if request.URL.String() != "https://api.deepseek.com/v1/tools/search" {
			t.Fatalf("unexpected endpoint: %s", request.URL)
		}
		if request.Header.Get("Authorization") != "Bearer synthetic-key-123" {
			t.Fatal("key was not applied to the configured official origin")
		}
		body, _ := io.ReadAll(request.Body)
		if got := string(body); got != `{"count":6,"need_content":false,"query":"music"}` && got != `{"query":"music","count":6,"need_content":false}` {
			t.Fatalf("unexpected request body: %s", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"results":[{"title":"Music","url":"https://example.com"}]}`)), Header: make(http.Header)}, nil
	})}
	items, err := Search(context.Background(), Config{BaseURL: "https://api.deepseek.com", APIKey: "synthetic-key-123", HTTP: client}, "music")
	if err != nil || len(items) != 1 || items[0].Title != "Music" || called != 1 {
		t.Fatalf("items=%+v err=%v calls=%d", items, err, called)
	}
	for _, baseURL := range []string{"https://api.deepseek.com.evil.invalid", "https://other.example", "https://api.deepseek.com/other"} {
		if _, err := Search(context.Background(), Config{BaseURL: baseURL, APIKey: "synthetic-key-123", HTTP: client}, "music"); err == nil {
			t.Fatalf("unsupported origin accepted: %s", baseURL)
		}
	}
	if called != 1 {
		t.Fatal("key sent to unsupported origin")
	}
}

func TestDeepSeekSearchReportsSafeAuthenticationDiagnostic(t *testing.T) {
	client := &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})}
	_, err := Search(context.Background(), Config{BaseURL: "https://api.deepseek.com/v1", APIKey: "synthetic-key-123", HTTP: client}, "music")
	diagnostic, ok := ErrorDiagnostics(err)
	if !ok || diagnostic.Code != "official_search_authentication_failed" || diagnostic.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("diagnostic=%+v ok=%v err=%v", diagnostic, ok, err)
	}
}

func TestDeepSeekSearchRejectsInvalidCredentialBeforeNetwork(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("must not contact network")
	})}
	_, err := Search(context.Background(), Config{BaseURL: "https://api.deepseek.com", APIKey: "short", HTTP: client}, "music")
	diagnostic, ok := ErrorDiagnostics(err)
	if !ok || diagnostic.Code != "official_search_key_missing" || calls != 0 || Supported(Config{BaseURL: "https://api.deepseek.com", APIKey: "short"}) {
		t.Fatalf("diagnostic=%+v ok=%v calls=%d err=%v", diagnostic, ok, calls, err)
	}
}
