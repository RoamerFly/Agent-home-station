package deepseek

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeDocumentOriginalUploadReuseAndSingleUse(t *testing.T) {
	uploads, deletes := 0, 0
	var seen []map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			deletes++
			w.Write([]byte(`{"deleted":true}`))
			return
		}
		if r.URL.Path == "/files" {
			uploads++
			reader, err := r.MultipartReader()
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			part, _ := reader.NextPart()
			value, _ := io.ReadAll(part)
			if part.FormName() != "purpose" || string(value) != "user_data" {
				t.Error("purpose must precede file")
			}
			for {
				p, e := reader.NextPart()
				if e == io.EOF {
					break
				}
				if e != nil {
					t.Error(e)
					break
				}
				b, _ := io.ReadAll(p)
				if p.FormName() == "file" && string(b) != "synthetic document 7319" {
					t.Error("original bytes changed")
				}
			}
			w.Write([]byte(`{"id":"file-synthetic"}`))
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		seen = append(seen, body)
		w.Write([]byte(`{"choices":[{"message":{"content":"read"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	c, err := NewWithAPIKey(Config{BaseURL: server.URL}, "synthetic-document-key")
	if err != nil {
		t.Fatal(err)
	}
	c.http = server.Client()
	ctx, clear := WithImageSession(context.Background())
	defer clear()
	attach := func() {
		t.Helper()
		_, e := c.AttachDocument(ctx, "sample.txt", []byte("synthetic document 7319"), false)
		if e != nil {
			t.Fatal(e)
		}
	}
	call := func() {
		t.Helper()
		if _, e := c.ChatFunctions(ctx, "read", []map[string]string{{"role": "user", "content": "read"}}, nil); e != nil {
			t.Fatal(e)
		}
	}
	attach()
	attach()
	call()
	call()
	attach()
	call()
	if uploads != 1 {
		t.Fatal("duplicate upload", uploads)
	}
	for i, want := range []int{3, 2, 3} {
		if got := len(seen[i]["messages"].([]any)); got != want {
			t.Fatalf("request %d: %d messages want %d", i, got, want)
		}
	}
	clear()
	if deletes != 1 {
		t.Fatal("uploaded file not cleaned", deletes)
	}
}
