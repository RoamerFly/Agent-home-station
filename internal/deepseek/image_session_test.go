package deepseek

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDirectImagesAreActualBytesAndRunScoped(t *testing.T) {
	var raw bytes.Buffer
	png.Encode(&raw, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	var seen []map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		json.NewDecoder(r.Body).Decode(&v)
		seen = append(seen, v)
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	c, err := NewWithAPIKey(Config{BaseURL: server.URL}, "synthetic-image-key")
	if err != nil {
		t.Fatal(err)
	}
	c.http = server.Client()
	ctx, clear := WithImageSession(t.Context())
	if err = AttachImage(ctx, "synthetic source", Image{Data: raw.Bytes()}); err != nil {
		t.Fatal(err)
	}
	AttachImage(ctx, "duplicate", Image{Data: raw.Bytes()})
	turns := []map[string]string{{"role": "user", "content": "compare"}}
	if _, err = c.ChatFunctions(ctx, "policy", turns, nil); err != nil {
		t.Fatal(err)
	}
	messages := seen[0]["messages"].([]any)
	parts := messages[len(messages)-1].(map[string]any)["content"].([]any)
	if len(parts) != 3 {
		t.Fatal("duplicate image or missing actual image", len(parts))
	}
	url := parts[2].(map[string]any)["image_url"].(map[string]any)["url"]
	if url != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(raw.Bytes()) {
		t.Fatal("image bytes were not sent")
	}
	for _, other := range []context.Context{t.Context(), ctx} {
		clear()
		if _, err = c.ChatFunctions(other, "policy", turns, nil); err != nil {
			t.Fatal(err)
		}
		if len(seen[len(seen)-1]["messages"].([]any)) != 2 {
			t.Fatal("image leaked after clear or into another run")
		}
	}
	if err = AttachImage(t.Context(), "no session", Image{Data: raw.Bytes()}); err == nil {
		t.Fatal("no-session attach accepted")
	}
	if err = AttachImage(ctx, "not an image", Image{Data: []byte("text")}); err == nil {
		t.Fatal("non-image accepted")
	}
	c.model = "deepseek-v4-pro"
	AttachImage(ctx, "source", Image{Data: raw.Bytes()})
	if _, err = c.ChatFunctions(ctx, "policy", turns, nil); err == nil {
		t.Fatal("unsupported model silently discarded image")
	}
}
