//go:build windows

package station

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"wechat-bot/internal/nativeclient"
)

func TestDouyinLiveDiscoverySelectsOnlyVerifiedMainExecutables(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "13.2.4.5", douyinCompanionExecutable)
	for _, path := range []string{main, filepath.Join(root, douyinCompanionExecutable), filepath.Join(root, "extra", douyinCompanionExecutable)} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("synthetic discovery fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	const publisher = "Douyin Vision Co., Ltd."
	var paths []string
	inspect := func(_ context.Context, path string) (nativeclient.Info, error) {
		paths = append(paths, path)
		info := nativeclient.Info{Architecture: "x64", ProductName: "直播伴侣", FileDescription: "直播伴侣"}
		if path == main {
			info.Signature = nativeclient.Signature{State: "trusted", Publisher: publisher, Organization: publisher}
		}
		return info, nil
	}
	got, err := discoverDouyinLiveCompanion(t.Context(), []string{root, root, `\\synthetic\share`}, inspect)
	if err != nil || len(got) != 1 || got[0].path != main || !slices.Equal(paths, []string{filepath.Join(root, douyinCompanionExecutable), main}) {
		t.Fatalf("discovery used an unverified or unrelated file: %v %v %v", got, paths, err)
	}
}

func TestDouyinLiveProcessObservationUsesExactExecutablePath(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if state := douyinLiveProcessState(t.Context(), executable); state != "observed" {
		t.Fatalf("current process was not observed: %s", state)
	}
	other := filepath.Join(t.TempDir(), filepath.Base(executable))
	if state := douyinLiveProcessState(t.Context(), other); state != "not_observed" {
		t.Fatalf("same filename at a different path was observed: %s", state)
	}
}

func TestDouyinLiveDiscoveryInstalledClientReadOnly(t *testing.T) {
	root := os.Getenv("AHS_TEST_DOUYIN_LIVE_INSTALL_ROOT")
	if root == "" {
		t.Skip("set the installed companion root for a read-only discovery check")
	}
	if !slices.ContainsFunc(douyinInstalledRoots(), func(path string) bool { return strings.EqualFold(path, filepath.Clean(root)) }) {
		t.Fatal("registered installation root was not discovered")
	}
	f, _ := newLocalDouyinWebFixture(t)
	initializeDouyinWeb(t, f)
	in := map[string]bool{"discover": true}
	if code, _ := f.request("POST", douyinLiveClientInspectRoute, in, false); code != 403 {
		t.Fatalf("missing CSRF accepted: %d", code)
	}
	code, view := f.request("POST", douyinLiveClientInspectRoute, in, true)
	if code != 200 || view["state"] != "inspected" || view["client_identity"] != "main" || view["control_available"] != false || view["process_state"] != "not_observed" && view["process_state"] != "observed" {
		t.Fatalf("unexpected installed client status: %d, %v", code, view)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(filepath.Base(root))) {
		t.Fatal("inspection response exposed the local installation path")
	}
	t.Logf("installed main signature verified, process state %s", view["process_state"])
}
