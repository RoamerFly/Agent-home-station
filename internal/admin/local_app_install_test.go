package admin

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	appcatalog "wechat-bot/connected-apps"
	"wechat-bot/internal/pluginhost"
)

func localAppInstallFixture(t *testing.T) (*localHomeFixture, map[string]any) {
	t.Helper()
	base := t.TempDir()
	f := newLocalHomeFixture(t, filepath.Join(base, "host"), filepath.Join(base, "workspace"))
	catalog, err := filepath.Abs("../../connected-apps")
	if err != nil {
		t.Fatal(err)
	}
	f.handler.home.appCatalog = catalog
	f.login()
	code, home := f.request("POST", "/api/local/home", map[string]string{"action": "initialize"}, true)
	if code != http.StatusOK {
		t.Fatal(code, home)
	}
	return f, home
}

func TestLocalAppSourceInstallPreservesEditsAndDoesNotConnect(t *testing.T) {
	f, home := localAppInstallFixture(t)
	code, catalog := f.request("GET", "/api/local/apps/catalog", nil, false)
	if code != 200 {
		t.Fatal(code, catalog)
	}
	row := catalog["applications"].([]any)[0].(map[string]any)
	if row["source_available"] != true || row["state"] != "source_only" {
		t.Fatal(row)
	}
	in := map[string]any{"digest": row["digest"], "revision": home["revision"]}
	if code, _ := f.request("POST", "/api/local/apps/wechat/install", in, false); code != 403 {
		t.Fatal("install bypassed CSRF", code)
	}
	code, installed := f.request("POST", "/api/local/apps/wechat/install", in, true)
	want := filepath.Join(home["directory"].(string), "Connected Apps", "wechat")
	if code != 200 || installed["result"] != "created" || installed["directory"] != want || installed["connected"] != false {
		t.Fatal(code, installed)
	}
	if _, err := pluginhost.VerifyDescriptor(want, "app.json"); err != nil {
		t.Fatal("installed bundle invalid", err)
	}
	marker := filepath.Join(want, "workspace", "my-tool.txt")
	if err := os.WriteFile(marker, []byte("persistent owner edits"), 0600); err != nil {
		t.Fatal(err)
	}
	if code, out := f.request("POST", "/api/local/apps/wechat/install", in, true); code != 200 || out["result"] != "existing" {
		t.Fatal(code, out)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "persistent owner edits" {
		t.Fatal("retry overwrote owner edits", err)
	}
	if code, out := f.request("GET", "/api/apps", nil, false); code != 200 || len(out["items"].([]any)) != 0 {
		t.Fatal("source install invented a connection", code, out)
	}
	// The same home and installation survive a host restart with zero accounts.
	f2 := newLocalHomeFixture(t, f.dir, f.root)
	f2.handler.home.appCatalog = f.handler.home.appCatalog
	f2.login()
	if code, out := f2.request("POST", "/api/local/apps/wechat/install", in, true); code != 200 || out["result"] != "existing" {
		t.Fatal("source installation did not survive restart", code, out)
	}
	if err := os.WriteFile(filepath.Join(want, "app.json"), []byte("different owner version"), 0600); err != nil {
		t.Fatal(err)
	}
	if code, out := f2.request("POST", "/api/local/apps/wechat/install", in, true); code != 409 {
		t.Fatal("different working version was overwritten", code, out)
	}
}

func TestLocalAppSourceInstallRejectsChangedSourceAndGrant(t *testing.T) {
	f, home := localAppInstallFixture(t)
	contract, err := appcatalog.Lookup("wechat")
	if err != nil {
		t.Fatal(err)
	}
	in := map[string]any{"digest": contract.Digest, "revision": home["revision"]}
	// Copy official files, then give the otherwise valid descriptor a different
	// version. Hash consistency alone must not establish publisher trust.
	official := f.handler.home.appCatalog
	replacement := t.TempDir()
	for name := range contract.Package.Files {
		data, err := os.ReadFile(filepath.Join(official, "wechat", filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(replacement, "wechat", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	alternative := contract.Package
	alternative.Version = "0.2.0"
	data, err := json.Marshal(alternative)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replacement, "wechat", "app.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	f.handler.home.appCatalog = replacement
	if code, out := f.request("POST", "/api/local/apps/wechat/install", in, true); code != 409 {
		t.Fatal("replacement descriptor became official", code, out)
	}
	f.handler.home.appCatalog = official
	for _, mutate := range []map[string]any{
		{"digest": "wrong", "revision": home["revision"]},
		{"digest": contract.Digest, "revision": 0},
	} {
		if code, out := f.request("POST", "/api/local/apps/wechat/install", mutate, true); code != 409 {
			t.Fatal(code, out)
		}
	}
	if code, out := f.request("POST", "/api/local/apps/unknown/install", in, true); code != 409 {
		t.Fatal(code, out)
	}
	// Missing source cannot create a visible installation.
	f.handler.home.appCatalog = t.TempDir()
	if code, out := f.request("POST", "/api/local/apps/wechat/install", in, true); code != 409 {
		t.Fatal("missing source accepted", code, out)
	}
	if code, out := f.request("POST", "/api/local/home", map[string]any{"action": "pause", "revision": home["revision"]}, true); code != 200 {
		t.Fatal(code, out)
	}
	if code, out := f.request("POST", "/api/local/apps/wechat/install", in, true); code != 409 {
		t.Fatal("paused home accepted install", code, out)
	}
	if _, err := os.Stat(filepath.Join(home["directory"].(string), "Connected Apps", "wechat")); !os.IsNotExist(err) {
		t.Fatal("failed install left visible source", err)
	}
}
