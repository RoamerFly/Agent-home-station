package station

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wechat-bot/internal/apphost"
)

type worksTestProvider struct{}

func (worksTestProvider) Connections(context.Context) ([]apphost.Connection, error) { return nil, nil }
func (worksTestProvider) Inspect(context.Context, string) (apphost.ConnectionStatus, error) {
	return apphost.ConnectionStatus{}, apphost.ErrNotFound
}

func worksTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func worksRows(t *testing.T, raw map[string]any) []map[string]any {
	t.Helper()
	values, ok := raw["items"].([]any)
	if !ok {
		t.Fatalf("items absent: %#v", raw)
	}
	items := make([]map[string]any, 0, len(values))
	for _, value := range values {
		item, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("item invalid: %#v", value)
		}
		items = append(items, item)
	}
	return items
}

func TestLocalWorksCatalogAndRecoverableManagement(t *testing.T) {
	base := t.TempDir()
	f := newLocalHomeFixture(t, filepath.Join(base, "data"), filepath.Join(base, "workspaces"))
	code, session := f.request("GET", "/api/session", nil, false)
	if code != http.StatusOK || session["authenticated"] != true {
		t.Fatalf("owner session: %d %#v", code, session)
	}
	f.csrf, _ = session["csrf"].(string)
	code, home := f.request("POST", "/api/local/home", map[string]string{"action": "initialize"}, true)
	if code != http.StatusOK {
		t.Fatalf("home: %d %#v", code, home)
	}
	registry, err := apphost.New(
		apphost.Registration{Application: apphost.Application{ID: "wechat", Name: "微信", InstallationID: "works-wechat", Registration: "synthetic"}, Provider: worksTestProvider{}},
		apphost.Registration{Application: apphost.Application{ID: "douyin", Name: "抖音", InstallationID: "works-douyin", Registration: "synthetic"}, Provider: worksTestProvider{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	f.handler.chat.applications = registry
	worksTestFile(t, f.root, "home/works/a.txt", "finished first")
	worksTestFile(t, f.root, "home/works/b.txt", "finished second")
	worksTestFile(t, f.root, "wechat/music/output/song.flac", "synthetic audio")
	worksTestFile(t, f.root, "douyin/works/clip.mp4", "synthetic video")
	worksTestFile(t, f.root, "wechat/messages/private.mp4", "private message")
	worksTestFile(t, f.root, "douyin/works/config.json", "synthetic secret")
	worksTestFile(t, f.root, "home/projects/design.json", "synthetic design")

	snapshot, err := f.handler.home.artifacts.publish(t.Context(), localArtifactInput{Path: "works/a.txt", Key: "works-catalog-snapshot"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]map[string]any{}
	cursor := ""
	for {
		uri := "/api/local/works?limit=2"
		if cursor != "" {
			uri += "&cursor=" + cursor
		}
		code, page := f.request("GET", uri, nil, false)
		if code != http.StatusOK {
			t.Fatalf("list: %d %#v", code, page)
		}
		for _, item := range worksRows(t, page) {
			key := item["path"].(string) + "|" + item["source"].(string)
			if seen[key] != nil {
				t.Fatalf("duplicate page item: %s", key)
			}
			seen[key] = item
		}
		next, _ := page["next_cursor"].(string)
		if next == "" {
			break
		}
		if cursor == next {
			t.Fatal("cursor did not advance")
		}
		cursor = next
	}
	for _, path := range []string{"home/works/a.txt|workspace", "home/works/b.txt|workspace", "wechat/music/output/song.flac|workspace", "douyin/works/clip.mp4|workspace", "home/projects/design.json|workspace", "已登记成品/a.txt|snapshot"} {
		if seen[path] == nil {
			t.Fatalf("missing %s in %#v", path, seen)
		}
	}
	for _, path := range []string{"wechat/messages/private.mp4|workspace", "douyin/works/config.json|workspace"} {
		if seen[path] != nil {
			t.Fatalf("private file listed: %s", path)
		}
	}
	if len(seen) != 6 {
		t.Fatalf("unexpected catalog entries: %#v", seen)
	}
	registered := seen["已登记成品/a.txt|snapshot"]
	if registered["id"] != "artifact-"+snapshot.ID || registered["can_rename"] != false || registered["can_trash"] != false {
		t.Fatal("snapshot is mutable", registered)
	}
	work := seen["home/works/a.txt|workspace"]
	if work["can_rename"] != true || work["can_trash"] != true {
		t.Fatal("work is not manageable", work)
	}
	if seen["home/projects/design.json|workspace"]["can_rename"] != false {
		t.Fatal("project config was mutable")
	}
	if seen["wechat/music/output/song.flac|workspace"]["can_trash"] != true {
		t.Fatal("completed output was not manageable")
	}
	id, version := work["id"].(string), work["version"].(string)
	if !strings.HasPrefix(work["preview_url"].(string), "/api/local/works/") || !strings.HasPrefix(work["download_url"].(string), "/api/local/works/") {
		t.Fatal("URLs are not same-origin", work)
	}
	request, err := http.NewRequest("GET", f.server.URL+work["preview_url"].(string), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || string(content) != "finished first" || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("preview: %d %q %v", response.StatusCode, content, err)
	}
	forged := workID("file", "wechat", "messages/private.mp4")
	if code, _ := f.request("GET", "/api/local/works/"+forged+"/preview", nil, false); code != 404 {
		t.Fatal("private message previewed", code)
	}
	if code, _ := f.request("PATCH", "/api/local/works/"+id, map[string]string{"name": "renamed.txt", "expected_version": "stale"}, true); code != 409 {
		t.Fatal("stale rename succeeded", code)
	}
	if code, _ := f.request("PATCH", "/api/local/works/"+id, map[string]string{"name": "b.txt", "expected_version": version}, true); code != 409 {
		t.Fatal("rename overwrote an existing work", code)
	}
	if data, err := os.ReadFile(filepath.Join(f.root, "home", "works", "b.txt")); err != nil || string(data) != "finished second" {
		t.Fatal("rename changed the existing target", err)
	}
	code, renamed := f.request("PATCH", "/api/local/works/"+id, map[string]string{"name": "renamed.txt", "expected_version": version}, true)
	if code != 200 {
		t.Fatalf("rename: %d %#v", code, renamed)
	}
	newItem := renamed["item"].(map[string]any)
	if _, err := os.Stat(filepath.Join(f.root, "home", "works", "a.txt")); !os.IsNotExist(err) {
		t.Fatal("old name retained", err)
	}
	if data, err := os.ReadFile(filepath.Join(f.root, "home", "works", "renamed.txt")); err != nil || string(data) != "finished first" {
		t.Fatal("renamed bytes changed", err)
	}
	code, trashed := f.request("POST", "/api/local/works/"+newItem["id"].(string)+"/trash", map[string]string{"expected_version": newItem["version"].(string)}, true)
	if code != 200 || trashed["trashed"] != true {
		t.Fatalf("trash: %d %#v", code, trashed)
	}
	trashItem := trashed["item"].(map[string]any)
	if trashItem["can_restore"] != true || trashItem["original_path"] != "home/works/renamed.txt" {
		t.Fatal("restore metadata missing", trashItem)
	}
	if _, err := os.Stat(filepath.Join(f.root, "home", "works", "renamed.txt")); !os.IsNotExist(err) {
		t.Fatal("trashed file remains active", err)
	}
	code, trashList := f.request("GET", "/api/local/works?status=trash&application=home", nil, false)
	if code != 200 || len(worksRows(t, trashList)) != 1 {
		t.Fatalf("trash list: %d %#v", code, trashList)
	}
	worksTestFile(t, f.root, "home/works/renamed.txt", "new occupied target")
	if code, _ := f.request("POST", trashItem["restore_url"].(string), map[string]any{}, true); code != 409 {
		t.Fatal("restore overwrote an existing work", code)
	}
	if data, err := os.ReadFile(filepath.Join(f.root, "home", "works", "renamed.txt")); err != nil || string(data) != "new occupied target" {
		t.Fatal("restore changed the existing target", err)
	}
	if err := os.Remove(filepath.Join(f.root, "home", "works", "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	code, restored := f.request("POST", trashItem["restore_url"].(string), map[string]any{}, true)
	if code != 200 || restored["restored"] != true {
		t.Fatalf("restore: %d %#v", code, restored)
	}
	if data, err := os.ReadFile(filepath.Join(f.root, "home", "works", "renamed.txt")); err != nil || string(data) != "finished first" {
		t.Fatal("restored bytes changed", err)
	}
	if code, _ := f.request("PATCH", "/api/local/works/artifact-"+snapshot.ID, map[string]string{"name": "bad.txt", "expected_version": "x"}, true); code != 404 {
		t.Fatal("snapshot renamed", code)
	}
	if code, _ := f.request("GET", "/api/local/works?cursor="+base64.RawURLEncoding.EncodeToString([]byte(`{"application":"other"}`)), nil, false); code != 400 {
		t.Fatal("cursor accepted wrong filters", code)
	}
}
