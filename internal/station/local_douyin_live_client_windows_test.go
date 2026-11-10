//go:build windows

package station

import (
	"debug/pe"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wechat-bot/internal/nativeclient"
)

func TestLocalDouyinLiveClientInspectionIsOwnerOnlyAndDoesNotConnect(t *testing.T) {
	f, _ := newLocalDouyinWebFixture(t)
	initializeDouyinWeb(t, f)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"executable": exe}
	if code, _ := f.request("POST", douyinLiveClientInspectRoute, input, false); code != http.StatusForbidden {
		t.Fatalf("missing CSRF accepted: %d", code)
	}
	_, before := f.request("GET", localDouyinWebRoute, nil, false)
	code, view := f.request("POST", douyinLiveClientInspectRoute, input, true)
	if code != http.StatusOK || view["state"] != "metadata_missing" || view["control_available"] != false {
		t.Fatalf("Go test executable must stay an unverified file, not a usable client: %d %v", code, view)
	}
	file, ok := view["file"].(map[string]any)
	if !ok || len(file["sha256"].(string)) != 64 || file["architecture"] != "x64" || file["bytes"].(float64) <= 0 {
		t.Fatal("missing actual file identity", view)
	}
	signature, ok := file["signature"].(map[string]any)
	if !ok || signature["state"] != "unsigned" || signature["publisher"] != nil || view["client_identity"] != "unverified" {
		t.Fatal("unsigned synthetic executable unexpectedly had publisher evidence", view)
	}
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), filepath.Base(exe)) || strings.Contains(string(raw), filepath.Dir(exe)) {
		t.Fatal("response exposed local path", string(raw))
	}
	_, after := f.request("GET", localDouyinWebRoute, nil, false)
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatal("inspection changed browser login or grants")
	}
}

func TestLocalDouyinLiveClientInspectionErrorsStaySpecificAndPrivate(t *testing.T) {
	f, _ := newLocalDouyinWebFixture(t)
	initializeDouyinWeb(t, f)
	bad := filepath.Join(t.TempDir(), "synthetic-client.exe")
	if err := os.WriteFile(bad, []byte("not an executable"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		code int
	}{
		{"", http.StatusBadRequest},
		{`\\server\private\client.exe`, http.StatusBadRequest},
		{bad + ":private", http.StatusBadRequest},
		{bad, http.StatusUnprocessableEntity},
		{filepath.Join(t.TempDir(), "missing-private-client.exe"), http.StatusServiceUnavailable},
	} {
		code, out := f.request("POST", douyinLiveClientInspectRoute, map[string]any{"executable": tc.path}, true)
		if code != tc.code {
			t.Fatalf("got %d, want %d: %v", code, tc.code, out)
		}
		raw, _ := json.Marshal(out)
		if strings.Contains(string(raw), "private-client") || strings.Contains(string(raw), "synthetic-client") {
			t.Fatal("private path leaked", string(raw))
		}
	}
}

func TestLocalDouyinLiveClientClassifiesOnlyTrustedPublisher(t *testing.T) {
	const publisher = "Douyin Vision Co., Ltd."
	info := nativeclient.Info{ProductName: "直播伴侣", FileDescription: "直播伴侣", CompanyName: publisher}
	if role, _ := douyinLiveClientIdentity(info); role != "unverified" {
		t.Fatal("self-declared metadata established the publisher", role)
	}
	info.Signature = nativeclient.Signature{State: "trusted", Publisher: publisher, Organization: "different organization"}
	if role, _ := douyinLiveClientIdentity(info); role != "other_publisher" {
		t.Fatal("a certificate with a mismatched subject was accepted", role)
	}
	info.Signature.Organization = publisher
	info.FileDescription = "直播伴侣 Launcher"
	if role, _ := douyinLiveClientIdentity(info); role != "launcher" {
		t.Fatal("launcher was mistaken for the main process", role)
	}
	info.FileDescription, info.Architecture = "直播伴侣", "x86"
	if role, _ := douyinLiveClientIdentity(info); role != "unrecognized_binary" {
		t.Fatal("32-bit executable was classified as the main process", role)
	}
	info.FileDescription = "another signed program"
	if role, _ := douyinLiveClientIdentity(info); role != "unrecognized_binary" {
		t.Fatal("an unrelated binary from the publisher was accepted", role)
	}
}

// Optional local acceptance reads only explicitly selected installed binaries.
// Paths and binaries stay on the user's machine and are never executed.
func TestLocalDouyinLiveClientInstalledSignatures(t *testing.T) {
	main, launcher := os.Getenv("AHS_TEST_DOUYIN_LIVE_MAIN"), os.Getenv("AHS_TEST_DOUYIN_LIVE_LAUNCHER")
	if main == "" || launcher == "" {
		t.Skip("set both installed live companion paths for read-only validation")
	}
	f, _ := newLocalDouyinWebFixture(t)
	initializeDouyinWeb(t, f)
	for _, file := range []struct{ path, role string }{{main, "main"}, {launcher, "launcher"}} {
		code, view := f.request("POST", douyinLiveClientInspectRoute, map[string]any{"executable": file.path}, true)
		if code != 200 || view["client_identity"] != file.role || view["control_available"] != false {
			t.Fatalf("signed %s did not return the expected bounded evidence: %d %#v", file.role, code, view)
		}
	}
	original, err := pe.Open(main)
	if err != nil {
		t.Fatal(err)
	}
	codeSection := original.Section(".text")
	original.Close()
	tampered, err := os.ReadFile(main)
	if err != nil || codeSection == nil || int(codeSection.Offset) >= len(tampered) {
		t.Fatalf("cannot construct a tampered test copy: %v", err)
	}
	tampered[codeSection.Offset] ^= 1
	copyPath := filepath.Join(t.TempDir(), "tampered.exe")
	if err := os.WriteFile(copyPath, tampered, 0600); err != nil {
		t.Fatal(err)
	}
	code, view := f.request("POST", douyinLiveClientInspectRoute, map[string]any{"executable": copyPath}, true)
	if code != 200 || view["client_identity"] != "unverified" || view["control_available"] != false ||
		view["file"].(map[string]any)["signature"].(map[string]any)["state"] != "untrusted" {
		t.Fatalf("modified signed bytes retained publisher trust: %d %#v", code, view)
	}
}
