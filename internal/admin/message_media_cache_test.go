package admin

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMessageMediaCacheStoresOneOriginalAndReusesContent(t *testing.T) {
	f := setup(t)
	data := []byte("hello from a synthetic received file")
	first, digest, err := f.manager.storeMessageMediaBytes("files", ".txt", data)
	if err != nil {
		t.Fatal(err)
	}
	second, again, err := f.manager.storeMessageMediaBytes("files", ".txt", data)
	if err != nil || first != second || digest != again {
		t.Fatalf("same bytes were duplicated: %q %q %v", first, second, err)
	}
	if !strings.Contains(first, filepath.Join("messages", "files")) {
		t.Fatal("original not in WeChat messages directory", first)
	}
	if err := f.manager.saveMessageObservation("files", digest, "完整文本", string(data)); err != nil {
		t.Fatal(err)
	}
	got, ok := f.manager.readMessageObservation("files", digest)
	if !ok || got.Text != string(data) {
		t.Fatal("cached reading unavailable")
	}
	if err := os.WriteFile(first, []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.manager.storeMessageMediaBytes("files", ".txt", data); err == nil {
		t.Fatal("modified original silently replaced")
	}
}

func TestMessageInspectionCacheKeysContentAndReadParameters(t *testing.T) {
	f := setup(t)
	digest := strings.Repeat("a", 64)
	packageDigest := strings.Repeat("b", 64)
	first := json.RawMessage(`{"artifact":{"artifact_id":"asset-one"},"pages":[1,2],"text_limit":1000}`)
	second := json.RawMessage(`{"artifact":{"artifact_id":"asset-two"},"text_limit":1000,"pages":[1,2]}`)
	key, ok := messageInspectionKey(digest, "application/pdf", "source.pdf", packageDigest, first)
	other, same := messageInspectionKey(digest, "application/pdf", "copy.pdf", packageDigest, second)
	if !ok || !same || key != other {
		t.Fatal("identical file and parameters did not share an inspection key")
	}
	changed, _ := messageInspectionKey(digest, "application/pdf", "copy.pdf", packageDigest, json.RawMessage(`{"pages":[3]}`))
	if key == changed {
		t.Fatal("different pages reused an inspection")
	}
	result := json.RawMessage(`{"text":"verified synthetic page"}`)
	if err := f.manager.saveMessageInspection(digest, key, result); err != nil {
		t.Fatal(err)
	}
	cached, found := f.manager.readMessageInspection(digest, other)
	if !found || !bytes.Equal(cached, result) {
		t.Fatal("same-hash document inspection unavailable")
	}
}

func TestMessageTextFileCachesOnlyActualSmallText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "message.txt")
	data := []byte("hello\nworld")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	coverage, text, ok := messageTextFile(path, "text/plain", int64(len(data)))
	if !ok || coverage != "完整文本" || text != string(data) {
		t.Fatal(coverage, text, ok)
	}
	if _, _, ok := messageTextFile(path, "application/pdf", int64(len(data))); ok {
		t.Fatal("binary file treated as extracted text")
	}
	if _, _, ok := messageTextFile(path, "text/plain", int64(len(data)+1)); ok {
		t.Fatal("size mismatch accepted")
	}
	if bytes.Contains([]byte(text), []byte{0}) {
		t.Fatal("invalid text")
	}
}
