package deepseek

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestModelTransientFailureRecoversSameInputAndReportsAttempts(t *testing.T) {
	for _, firstStatus := range []int{429, 503} {
		t.Run(http.StatusText(firstStatus), func(t *testing.T) {
			calls := 0
			var bodies []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				body, _ := io.ReadAll(r.Body)
				bodies = append(bodies, string(body))
				if calls == 1 {
					w.WriteHeader(firstStatus)
					_, _ = w.Write([]byte(`{"error":"private diagnostic"}`))
					return
				}
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"模型恢复测试通过"},"finish_reason":"stop"}]}`))
			}))
			defer server.Close()
			client := &Client{endpoint: server.URL, key: "test-key", http: server.Client()}
			var events []string
			ctx := WithRequestObserver(context.Background(), func(event string, d Diagnostics) {
				events = append(events, event)
				if d.MaxAttempts != 2 || d.Attempts < 1 || d.Attempts > 2 {
					t.Fatal(d)
				}
			})
			answer, err := client.request(ctx, []byte(`{"messages":["current prompt only"]}`))
			if err != nil || answer != "模型恢复测试通过" || calls != 2 || bodies[0] != bodies[1] {
				t.Fatal(answer, err, calls, bodies)
			}
			if strings.Join(events, ",") != "model_retry,model_recovered" {
				t.Fatal(events)
			}
		})
	}
}

func TestLongRetryAfterIsRespectedAndCancellationStopsBackoff(t *testing.T) {
	for _, cancelDuringWait := range []bool{false, true} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if !cancelDuringWait {
				w.Header().Set("Retry-After", "120")
			}
			w.WriteHeader(429)
		}))
		client := &Client{endpoint: server.URL, http: server.Client()}
		ctx, cancel := context.WithCancel(context.Background())
		ctx = WithRequestObserver(ctx, func(string, Diagnostics) {
			if cancelDuringWait {
				cancel()
			}
		})
		_, err := client.request(ctx, []byte("{}"))
		if err == nil || calls != 1 {
			t.Fatal(err, calls)
		}
		if !cancelDuringWait {
			d, ok := ErrorDiagnostics(err)
			if !ok || d.RetryDelayMS < int64((120*time.Second).Milliseconds()) || !d.Retryable {
				t.Fatal("long provider retry-after was not retained for durable recovery", d)
			}
		}
		if cancelDuringWait && !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation identity lost", err)
		}
		cancel()
		server.Close()
	}
}

type recoveryTransport func(*http.Request) (*http.Response, error)

func (f recoveryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNetworkErrorsAreBoundedAndNeverExposePrivateDetails(t *testing.T) {
	calls := 0
	client := &Client{endpoint: "https://private.invalid/secret", http: &http.Client{Transport: recoveryTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, &url.Error{Op: "Post", URL: "https://private.invalid?api_key=secret-value", Err: io.ErrUnexpectedEOF}
	})}}
	_, err := client.request(context.Background(), []byte("{}"))
	d, ok := ErrorDiagnostics(err)
	if !ok || calls != 2 || d.Attempts != 2 || d.Stage != "connection_or_headers" || d.Code != "model_connection_closed" {
		t.Fatal(err, d, calls)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private.invalid") {
		t.Fatal("private endpoint leaked", err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err = client.request(ctx, []byte("{}"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("deadline identity lost", err)
	}
}

func TestResponseReadFailureIsNotMisreportedAsSizeLimit(t *testing.T) {
	client := &Client{endpoint: "https://test.invalid", http: &http.Client{Transport: recoveryTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(&brokenResponse{})}, nil
	})}}
	_, err := client.request(context.Background(), []byte("{}"))
	d, ok := ErrorDiagnostics(err)
	if !ok || d.Stage != "response_body" || d.HTTPStatus != 200 || d.Code != "model_connection_closed" || d.Attempts != 2 {
		t.Fatal(err, d)
	}
}

type brokenResponse struct{}

func (*brokenResponse) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
