package station

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"wechat-bot/internal/companion"
)

func uploadChatFixtureFile(t *testing.T, f *localHomeFixture, name string, data []byte, csrf bool) (int, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, f.server.URL+"/api/local/chat/attachments", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", f.server.URL)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if csrf {
		req.Header.Set("X-CSRF-Token", f.csrf)
	}
	response, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(response.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, out
}

func TestLocalChatAttachmentUploadSendHistoryAndIdempotency(t *testing.T) {
	f := localChatFixture(t)
	var pixel bytes.Buffer
	im := image.NewRGBA(image.Rect(0, 0, 2, 2))
	im.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&pixel, im); err != nil {
		t.Fatal(err)
	}
	if code, _ := uploadChatFixtureFile(t, f, "test.png", pixel.Bytes(), false); code != http.StatusForbidden {
		t.Fatalf("upload bypassed owner CSRF: %d", code)
	}
	code, uploaded := uploadChatFixtureFile(t, f, "test.png", pixel.Bytes(), true)
	if code != http.StatusOK {
		t.Fatalf("upload: %d %#v", code, uploaded)
	}
	attachment := uploaded["attachment"].(map[string]any)
	id := attachment["id"].(string)
	if !localAttachmentID(id) || attachment["mime"] != "image/png" || attachment["preview_url"] == "" {
		t.Fatalf("uploaded metadata: %#v", attachment)
	}
	if code, again := uploadChatFixtureFile(t, f, "other-name.png", pixel.Bytes(), true); code != http.StatusOK || again["attachment"].(map[string]any)["id"] != id {
		t.Fatalf("content dedup: %d %#v", code, again)
	}
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
			if !strings.Contains(turns[len(turns)-1]["content"], id) {
				t.Error("attachment identity absent from model context")
			}
			return `{"action":"reply","messages":["看到了附件。"],"reason":"reply"}`, nil
		}), nil
	}
	request := map[string]any{"request_id": "attachment-only", "text": "", "attachments": []string{id}}
	if code, result := f.request("POST", "/api/local/chat", request, true); code != 200 || result["status"] != "completed" {
		t.Fatalf("attachment-only send: %d %#v", code, result)
	}
	if code, result := f.request("POST", "/api/local/chat", request, true); code != 200 || result["reused"] != true {
		t.Fatalf("same request replay: %d %#v", code, result)
	}
	if code, _ := f.request("POST", "/api/local/chat", map[string]any{"request_id": "attachment-only", "text": ""}, true); code != 400 {
		t.Fatalf("empty changed replay: %d", code)
	}
	_, history := f.request("GET", "/api/local/chat", nil, true)
	items := history["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("history length: %#v", history)
	}
	user := items[0].(map[string]any)
	got := user["attachments"].([]any)
	if user["text"] != "" || got[0].(map[string]any)["id"] != id {
		t.Fatalf("history attachment: %#v", user)
	}
	req, err := http.NewRequest(http.MethodGet, f.server.URL+attachment["preview_url"].(string), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || !bytes.Equal(preview, pixel.Bytes()) {
		t.Fatalf("preview: %d %v", response.StatusCode, err)
	}
}

func TestLocalChatAttachmentTextReadAndSelectionBinding(t *testing.T) {
	f := localChatFixture(t)
	code, uploaded := uploadChatFixtureFile(t, f, "notes.txt", []byte("hello attached text"), true)
	if code != 200 {
		t.Fatalf("upload: %d %#v", code, uploaded)
	}
	id := uploaded["attachment"].(map[string]any)["id"].(string)
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(context.Context, string, []map[string]string) (string, error) {
			return `{"action":"reply","messages":["收到"],"reason":"reply"}`, nil
		}), nil
	}
	request := map[string]any{"request_id": "attached-text", "text": "read it", "attachments": []string{id}, "model_selection": map[string]any{"model": "deepseek-flash"}}
	if code, result := f.request("POST", "/api/local/chat", request, true); code != 200 {
		t.Fatalf("send: %d %#v", code, result)
	}
	changed := map[string]any{"request_id": "attached-text", "text": "read it", "attachments": []string{id}, "model_selection": map[string]any{"model": "deepseek-reasoner"}}
	if code, _ := f.request("POST", "/api/local/chat", changed, true); code != 409 {
		t.Fatalf("model swap accepted: %d", code)
	}
	_, in, err := f.handler.chat.conversation()
	if err != nil {
		t.Fatal(err)
	}
	_, grant, err := f.handler.chat.attachmentAccess(in)
	if err != nil {
		t.Fatal(err)
	}
	d, file, err := readAttachment(grant, id)
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(file)
	file.Close()
	if err != nil || d.Name != "notes.txt" || string(content) != "hello attached text" {
		t.Fatalf("stored source: %#v %q %v", d, content, err)
	}
}

func TestLocalChatAttachmentToolReadsUploadedText(t *testing.T) {
	f := localChatFixture(t)
	code, uploaded := uploadChatFixtureFile(t, f, "notes.txt", []byte("hello attached text"), true)
	if code != http.StatusOK {
		t.Fatalf("upload: %d %#v", code, uploaded)
	}
	id := uploaded["attachment"].(map[string]any)["id"].(string)
	var calls atomic.Int32
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
			if calls.Add(1) == 1 {
				return localToolCall(localChatAttachmentTool, map[string]any{"id": id}), nil
			}
			var content string
			for _, turn := range turns {
				content += turn["content"]
			}
			if !strings.Contains(content, "hello attached text") {
				t.Error("uploaded text was not returned to the model")
			}
			return `{"action":"reply","messages":["读到了。"],"reason":"reply"}`, nil
		}), nil
	}
	if code, result := f.request("POST", "/api/local/chat", map[string]any{"request_id": "read-upload", "text": "读一下这个文件", "attachments": []string{id}}, true); code != http.StatusOK || result["status"] != "completed" {
		t.Fatalf("send: %d %#v", code, result)
	}
	if calls.Load() != 2 {
		t.Fatalf("model calls = %d", calls.Load())
	}
}
