package devicebroker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/adbserver"
	"wechat-bot/internal/mcptools"
)

type fakeHub struct {
	calls      int
	closeCalls int
	call       func(context.Context, string, json.RawMessage) (string, error)
	close      func() error
}

func (f *fakeHub) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	f.calls++
	if f.call != nil {
		return f.call(ctx, name, args)
	}
	return `{"ok":true}`, nil
}
func (f *fakeHub) Close() error {
	f.closeCalls++
	if f.close != nil {
		return f.close()
	}
	return nil
}

func TestOpenFiltersLegacyProfileAndPinsFixedRuntime(t *testing.T) {
	options, paths := fixtureOptions(t)
	legacy := profile{ADB: paths.adb, Devices: map[string]profileDevice{
		"allowed": {Serial: "emulator-5554", Enabled: true, Packages: []string{"com.example.browser"}, Actions: []string{"observe", "tap", "capture", "home"}, Browsers: []string{"com.example.browser"}, WebHosts: []string{"example.test"}},
		"other":   {Serial: "private-serial", Enabled: true, Packages: []string{"com.other.app"}, Actions: []string{"observe", "text"}},
	}}
	writeJSON(t, paths.config, legacy)
	options.Legacy = legacyConfig(paths.script, paths.config, paths.storage)
	options.Legacy.Command = "C:/untrusted/ignored.exe"
	var captured mcptools.ServerConfig
	withHub(t, func(config mcptools.ServerConfig) (hub, error) {
		captured = config
		return &fakeHub{}, nil
	})
	bridge, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	if captured.Command != paths.python || len(captured.Args) != 8 || captured.Args[3] != paths.script || captured.Args[4] != "--config" || captured.Args[6] != "--storage" || captured.Args[7] != paths.storage {
		t.Fatalf("fixed command contract lost: %#v", captured)
	}
	if len(captured.Env) != 0 || captured.URL != "" || len(captured.Headers) != 0 {
		t.Fatal("legacy process settings escaped into broker")
	}
	data, err := os.ReadFile(captured.Args[5])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "other") || strings.Contains(string(data), "private-serial") || strings.Contains(string(data), "untrusted") {
		t.Fatalf("unfiltered profile leaked: %s", data)
	}
	var filtered profile
	if json.Unmarshal(data, &filtered) != nil || len(filtered.Devices) != 1 || filtered.Devices["allowed"].Serial != "emulator-5554" {
		t.Fatalf("bad filtered profile: %#v", filtered)
	}
	if got := filtered.Devices["allowed"].Actions; strings.Join(got, ",") != "observe,tap,capture" {
		t.Fatalf("wrong action intersection: %#v", got)
	}
}

func TestOpenRejectsUnsafeLegacyInvocationAndPaths(t *testing.T) {
	options, paths := fixtureOptions(t)
	writeJSON(t, paths.config, validProfile(paths.adb))
	cases := []struct {
		name   string
		mutate func(*Options)
	}{
		{"python module flag", func(o *Options) { o.Legacy = legacyConfig("-m", paths.config, paths.storage) }},
		{"legacy environment", func(o *Options) {
			o.Legacy = legacyConfig(paths.script, paths.config, paths.storage)
			o.Legacy.Env = map[string]string{"SECRET": "no"}
		}},
		{"outside config", func(o *Options) {
			o.Legacy = legacyConfig(paths.script, filepath.Join(t.TempDir(), "profile.json"), paths.storage)
		}},
		{"fresh ledger outside state", func(o *Options) { o.Legacy = legacyConfig(paths.script, paths.config, paths.workspace) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := options
			tc.mutate(&o)
			if _, err := Open(context.Background(), o); !errors.Is(err, ErrNeedsMigration) {
				t.Fatalf("Open error = %v, want needs migration", err)
			}
		})
	}
}

func TestOpenRequiresBoundedTrustedADBRoot(t *testing.T) {
	options, paths := fixtureOptions(t)
	writeJSON(t, paths.config, validProfile(paths.adb))
	options.Legacy = legacyConfig(paths.script, paths.config, paths.storage)
	outside := filepath.Join(paths.workspace, "untrusted", "adb.exe")
	if err := os.MkdirAll(filepath.Dir(outside), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*Options)
	}{
		{"no roots", func(o *Options) { o.ADBRoots = nil }},
		{"workspace is not a runtime root", func(o *Options) { o.ADBRoots = []string{paths.workspace} }},
		{"profile adb outside approved root", func(o *Options) { writeJSON(t, paths.config, validProfile(outside)) }},
		{"non-adb executable rejected", func(o *Options) {
			writeJSON(t, paths.config, validProfile(filepath.Join(paths.adbRoot, "other.exe")))
			_ = os.WriteFile(filepath.Join(paths.adbRoot, "other.exe"), []byte("other"), 0600)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeJSON(t, paths.config, validProfile(paths.adb))
			o := options
			tc.mutate(&o)
			if _, err := Open(context.Background(), o); !errors.Is(err, ErrNeedsMigration) {
				t.Fatalf("Open error = %v, want needs migration", err)
			}
		})
	}
}

func TestCallRevalidatesBeforeAndAfterBridgeInvocation(t *testing.T) {
	options, paths := fixtureOptions(t)
	writeJSON(t, paths.config, validProfile(paths.adb))
	options.Legacy = legacyConfig(paths.script, paths.config, paths.storage)
	fake := &fakeHub{}
	withHub(t, func(mcptools.ServerConfig) (hub, error) { return fake, nil })
	bridge, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	if err := os.WriteFile(paths.script, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = bridge.Call(context.Background(), "observe", json.RawMessage(`{"device_id":"allowed"}`)); !errors.Is(err, errChanged) || fake.calls != 0 {
		t.Fatalf("changed pre-call accepted: err=%v calls=%d", err, fake.calls)
	}

	// Reopen with a stable script, then mutate it from the fake call. The
	// broker reports an unknown outcome to its caller instead of replaying.
	if err := os.WriteFile(paths.script, []byte("bridge"), 0600); err != nil {
		t.Fatal(err)
	}
	bridge, err = Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	fake.call = func(context.Context, string, json.RawMessage) (string, error) {
		return `{"state":"submitted"}`, os.WriteFile(paths.script, []byte("changed-again"), 0600)
	}
	if _, err = bridge.Call(context.Background(), "act", json.RawMessage(`{"device_id":"allowed"}`)); !errors.Is(err, errChanged) || fake.calls != 1 {
		t.Fatalf("changed post-call accepted: err=%v calls=%d", err, fake.calls)
	}
	if _, err = bridge.Call(context.Background(), "shell", json.RawMessage(`{}`)); !errors.Is(err, errToolDenied) {
		t.Fatalf("unexpected tool accepted: %v", err)
	}
}

func TestNativeFailureReleasesManagedADBLeaseAfterHubCleanup(t *testing.T) {
	options, paths := fixtureOptions(t)
	writeJSON(t, paths.config, validProfile(paths.adb))
	options.Legacy = legacyConfig(paths.script, paths.config, paths.storage)
	lease := &trackedADBLease{}
	options.ADBServer = trackedADBServer{lease: lease}
	fake := &fakeHub{call: func(context.Context, string, json.RawMessage) (string, error) {
		return "", errors.New("native failure")
	}}
	withHub(t, func(mcptools.ServerConfig) (hub, error) { return fake, nil })
	bridge, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.Call(context.Background(), "act", json.RawMessage(`{"device_id":"allowed"}`)); err == nil {
		t.Fatal("native failure unexpectedly succeeded")
	}
	if lease.closes != 1 {
		t.Fatalf("adb lease was leaked after native failure: %d", lease.closes)
	}
	if err := bridge.Close(); err != nil {
		t.Fatal(err)
	}
	if lease.closes != 1 {
		t.Fatalf("closed lease twice: %d", lease.closes)
	}
}

func TestFailedADBLeaseCleanupIsQuarantinedForRetry(t *testing.T) {
	options, paths := fixtureOptions(t)
	writeJSON(t, paths.config, validProfile(paths.adb))
	options.Legacy = legacyConfig(paths.script, paths.config, paths.storage)
	lease := &trackedADBLease{err: errors.New("server cleanup pending")}
	options.ADBServer = trackedADBServer{lease: lease}
	fake := &fakeHub{call: func(context.Context, string, json.RawMessage) (string, error) {
		return "", errors.New("native failure")
	}}
	withHub(t, func(mcptools.ServerConfig) (hub, error) { return fake, nil })
	bridge, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.Call(context.Background(), "act", json.RawMessage(`{"device_id":"allowed"}`)); err == nil {
		t.Fatal("native failure unexpectedly succeeded")
	}
	if !bridge.(*broker).quarantined {
		t.Fatal("failed managed cleanup was not quarantined")
	}
	lease.err = nil
	if err := CleanupQuarantined(); err != nil {
		t.Fatalf("quarantined cleanup retry: %v", err)
	}
	if lease.closes < 2 {
		t.Fatalf("lease cleanup was not retried: %d", lease.closes)
	}
}

func TestBindingPinsProfileADBAndDerivedConfigWithoutStartingBridge(t *testing.T) {
	options, paths := fixtureOptions(t)
	base := validProfile(paths.adb)
	writeJSON(t, paths.config, base)
	options.Legacy = legacyConfig(paths.script, paths.config, paths.storage)
	var hubs []*fakeHub
	withHub(t, func(mcptools.ServerConfig) (hub, error) {
		fake := &fakeHub{}
		hubs = append(hubs, fake)
		return fake, nil
	})

	open := func() *broker {
		bridge, err := Open(context.Background(), options)
		if err != nil {
			t.Fatal(err)
		}
		return bridge.(*broker)
	}
	assertChanged := func(label string, bridge *broker) {
		t.Helper()
		if binding, err := bridge.Binding(); !errors.Is(err, errChanged) || binding != "" {
			t.Fatalf("%s Binding = %q, %v; want changed without hash", label, binding, err)
		}
	}

	bridge := open()
	first, err := bridge.Binding()
	if err != nil || first == "" {
		t.Fatalf("initial Binding = %q, %v", first, err)
	}
	second, err := bridge.Binding()
	if err != nil || second != first || hubs[0].calls != 0 {
		t.Fatalf("stable Binding changed or started bridge: %q %v calls=%d", second, err, hubs[0].calls)
	}
	changedProfile := base
	changedProfile.Devices["alias-change"] = profileDevice{Serial: "emulator-5556", Enabled: true, Packages: []string{"com.example.browser"}, Actions: []string{"observe"}}
	writeJSON(t, paths.config, changedProfile)
	assertChanged("profile alias", bridge)

	writeJSON(t, paths.config, base)
	bridge = open()
	if err := os.WriteFile(paths.adb, []byte("changed-adb"), 0600); err != nil {
		t.Fatal(err)
	}
	assertChanged("adb", bridge)
	if err := os.WriteFile(paths.adb, []byte("bridge"), 0600); err != nil {
		t.Fatal(err)
	}

	bridge = open()
	derived := ""
	for _, seal := range bridge.seals {
		if strings.Contains(seal.path, "devicebroker-private") {
			derived = seal.path
			break
		}
	}
	if derived == "" {
		t.Fatal("derived profile seal missing")
	}
	if err := os.WriteFile(derived, []byte(`{"tampered":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	assertChanged("derived profile", bridge)
}

func TestHubSessionDoesNotPassHostSecretToFixedPythonBridge(t *testing.T) {
	python := testPython(t)
	options, paths := fixtureOptions(t)
	options.Python = python
	writeJSON(t, paths.config, validProfile(paths.adb))
	options.Legacy = legacyConfig(paths.script, paths.config, paths.storage)
	// This is a no-op stdio MCP fixture, not the real bridge and not a device
	// call. It observes only whether a non-allowlisted host secret reached it.
	const fixture = `import json, os, sys
for line in sys.stdin:
  request = json.loads(line)
  method = request.get("method")
  if method == "notifications/initialized":
    continue
  if method == "initialize":
    result = {"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"fixture","version":"1"}}
  elif method == "tools/call":
    result = {"content":[{"type":"text","text":json.dumps({"host_secret_present": "DEVICEBROKER_HOST_SECRET" in os.environ})}]}
  else:
    result = {"tools":[]}
  print(json.dumps({"jsonrpc":"2.0","id":request.get("id"),"result":result}), flush=True)
`
	if err := os.WriteFile(paths.script, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVICEBROKER_HOST_SECRET", "must-not-reach-child")
	bridge, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	result, err := bridge.Call(context.Background(), "devices", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result, "true") {
		t.Fatalf("host secret reached fixed bridge: %s", result)
	}
}

func TestPhysicalLeaseIsSharedAcrossAliasesAndAccounts(t *testing.T) {
	options, paths := fixtureOptions(t)
	profile := validProfile(paths.adb)
	profile.Devices["same-alias"] = profileDevice{Serial: "emulator-5554", Enabled: true, Packages: []string{"com.example.browser"}, Actions: []string{"observe"}}
	profile.Devices["other-serial"] = profileDevice{Serial: "emulator-5556", Enabled: true, Packages: []string{"com.example.browser"}, Actions: []string{"observe"}}
	writeJSON(t, paths.config, profile)
	options.Legacy = legacyConfig(paths.script, paths.config, paths.storage)

	state2, storage2 := filepath.Join(filepath.Dir(paths.state), "state-two"), filepath.Join(filepath.Dir(paths.state), "state-two", "ledger")
	if err := os.MkdirAll(storage2, 0700); err != nil {
		t.Fatal(err)
	}
	second := options
	second.Account, second.StateDirectory, second.Devices = "account_2", state2, []string{"same-alias"}
	second.Legacy = legacyConfig(paths.script, paths.config, storage2)
	third := options
	third.Account, third.StateDirectory, third.Devices = "account_3", filepath.Join(filepath.Dir(paths.state), "state-three"), []string{"other-serial"}
	storage3 := filepath.Join(third.StateDirectory, "ledger")
	if err := os.MkdirAll(storage3, 0700); err != nil {
		t.Fatal(err)
	}
	third.Legacy = legacyConfig(paths.script, paths.config, storage3)

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var made int
	withHub(t, func(mcptools.ServerConfig) (hub, error) {
		made++
		index := made
		return &fakeHub{call: func(ctx context.Context, _ string, _ json.RawMessage) (string, error) {
			entered <- struct{}{}
			if index == 1 || index == 3 {
				select {
				case <-release:
					return `{"ok":true}`, nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			return `{"ok":true}`, nil
		}}, nil
	})
	first, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	alias, err := Open(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	defer alias.Close()
	different, err := Open(context.Background(), third)
	if err != nil {
		t.Fatal(err)
	}
	defer different.Close()

	firstDone := make(chan error, 1)
	go func() {
		_, err := first.Call(context.Background(), "observe", json.RawMessage(`{"device_id":"allowed"}`))
		firstDone <- err
	}()
	<-entered
	if _, err := alias.Call(context.Background(), "observe", json.RawMessage(`{"device_id":"same-alias"}`)); !errors.Is(err, ErrDeviceBusy) {
		t.Fatalf("same physical serial was not leased globally: %v", err)
	}
	differentDone := make(chan error, 1)
	go func() {
		_, err := different.Call(context.Background(), "observe", json.RawMessage(`{"device_id":"other-serial"}`))
		differentDone <- err
	}()
	<-entered // a distinct serial may execute concurrently
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-differentDone; err != nil {
		t.Fatal(err)
	}
	if _, err := alias.Call(context.Background(), "observe", json.RawMessage(`{"device_id":"same-alias"}`)); err != nil {
		t.Fatalf("lease did not release after completed call: %v", err)
	}
}

func TestUnknownCallKeepsPhysicalLeaseUntilCleanupConfirmed(t *testing.T) {
	options, paths := fixtureOptions(t)
	profile := validProfile(paths.adb)
	profile.Devices["same-alias"] = profileDevice{Serial: "emulator-5554", Enabled: true, Packages: []string{"com.example.browser"}, Actions: []string{"observe"}}
	writeJSON(t, paths.config, profile)
	options.Legacy = legacyConfig(paths.script, paths.config, paths.storage)

	state2 := filepath.Join(filepath.Dir(paths.state), "state-two")
	storage2 := filepath.Join(state2, "ledger")
	if err := os.MkdirAll(storage2, 0700); err != nil {
		t.Fatal(err)
	}
	second := options
	second.Account, second.StateDirectory, second.Devices = "account_2", state2, []string{"same-alias"}
	second.Legacy = legacyConfig(paths.script, paths.config, storage2)

	cleanupErr := errors.New("native_child_cleanup_unconfirmed")
	firstHub := &fakeHub{call: func(context.Context, string, json.RawMessage) (string, error) {
		return "", errors.New("native_transport_interrupted")
	}}
	firstHub.close = func() error { return cleanupErr }
	secondHub := &fakeHub{}
	opened := 0
	withHub(t, func(mcptools.ServerConfig) (hub, error) {
		opened++
		if opened == 1 {
			return firstHub, nil
		}
		return secondHub, nil
	})
	first, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	other, err := Open(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err = first.Call(context.Background(), "observe", json.RawMessage(`{"device_id":"allowed"}`)); err == nil || firstHub.calls != 1 || firstHub.closeCalls != 1 {
		t.Fatalf("unknown call was not attempted/cleaned once: err=%v calls=%d closes=%d", err, firstHub.calls, firstHub.closeCalls)
	}
	if _, err = other.Call(context.Background(), "observe", json.RawMessage(`{"device_id":"same-alias"}`)); !errors.Is(err, ErrDeviceBusy) {
		t.Fatalf("lease released while child cleanup was unconfirmed: %v", err)
	}
	if _, err = first.Binding(); !errors.Is(err, errClosed) {
		t.Fatalf("quarantined broker still produced binding: %v", err)
	}
	firstHub.close = func() error { return nil }
	if err = first.Close(); err != nil {
		t.Fatalf("confirmed cleanup did not release quarantined lease: %v", err)
	}
	if _, err = other.Call(context.Background(), "observe", json.RawMessage(`{"device_id":"same-alias"}`)); err != nil {
		t.Fatalf("lease remained after confirmed cleanup: %v", err)
	}
	if firstHub.calls != 1 {
		t.Fatalf("unknown operation was replayed: calls=%d", firstHub.calls)
	}
}

func TestQuarantinedBrokerSurvivesGCUntilCleanup(t *testing.T) {
	options, paths := fixtureOptions(t)
	profile := validProfile(paths.adb)
	profile.Devices["same-alias"] = profileDevice{Serial: "emulator-5554", Enabled: true, Packages: []string{"com.example.browser"}, Actions: []string{"observe"}}
	writeJSON(t, paths.config, profile)
	options.Legacy = legacyConfig(paths.script, paths.config, paths.storage)
	state2 := filepath.Join(filepath.Dir(paths.state), "state-two")
	storage2 := filepath.Join(state2, "ledger")
	if err := os.MkdirAll(storage2, 0700); err != nil {
		t.Fatal(err)
	}
	otherOptions := options
	otherOptions.Account, otherOptions.StateDirectory, otherOptions.Devices = "account_2", state2, []string{"same-alias"}
	otherOptions.Legacy = legacyConfig(paths.script, paths.config, storage2)

	cleanupErr := errors.New("cleanup_still_unknown")
	firstHub := &fakeHub{call: func(context.Context, string, json.RawMessage) (string, error) {
		return "", errors.New("transport_interrupted")
	}, close: func() error { return cleanupErr }}
	secondHub := &fakeHub{}
	opened := 0
	withHub(t, func(mcptools.ServerConfig) (hub, error) {
		opened++
		if opened == 1 {
			return firstHub, nil
		}
		return secondHub, nil
	})
	func() {
		bridge, err := Open(context.Background(), options)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = bridge.Call(context.Background(), "observe", json.RawMessage(`{"device_id":"allowed"}`)); err == nil {
			t.Fatal("expected unknown native outcome")
		}
		// Intentionally discard the wrapper: only the quarantine registry may
		// keep the lease alive now.
	}()
	runtime.GC()
	other, err := Open(context.Background(), otherOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err = other.Call(context.Background(), "observe", json.RawMessage(`{"device_id":"same-alias"}`)); !errors.Is(err, ErrDeviceBusy) {
		t.Fatalf("GC released quarantined physical lease: %v", err)
	}
	firstHub.close = func() error { return nil }
	if err = CleanupQuarantined(); err != nil {
		t.Fatalf("CleanupQuarantined = %v", err)
	}
	if _, err = other.Call(context.Background(), "observe", json.RawMessage(`{"device_id":"same-alias"}`)); err != nil {
		t.Fatalf("cleanup did not release physical lease: %v", err)
	}
	if firstHub.calls != 1 {
		t.Fatalf("cleanup replayed unknown operation: calls=%d", firstHub.calls)
	}
}

func TestPhysicalLeaseReleasesAfterCancellationAndProcessExit(t *testing.T) {
	if path := os.Getenv("DEVICEBROKER_LEASE_HELPER"); path != "" {
		lease, err := acquireLease(path)
		if err != nil {
			os.Exit(2)
		}
		defer lease.Close()
		fmt.Println("locked")
		select {}
	}
	path := filepath.Join(t.TempDir(), "physical.lock")
	lease, err := acquireLease(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acquireLease(path); !errors.Is(err, errLeaseBusy) {
		t.Fatalf("same-process contention = %v, want busy", err)
	}
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	lease, err = acquireLease(path)
	if err != nil {
		t.Fatalf("close did not release lease: %v", err)
	}
	_ = lease.Close()

	command := exec.Command(os.Args[0], "-test.run=^TestPhysicalLeaseReleasesAfterCancellationAndProcessExit$")
	command.Env = append(os.Environ(), "DEVICEBROKER_LEASE_HELPER="+path)
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	if line, readErr := bufio.NewReader(output).ReadString('\n'); readErr != nil || line != "locked\n" {
		_ = command.Process.Kill()
		t.Fatalf("lease helper readiness = %q, %v", line, readErr)
	}
	if _, err = acquireLease(path); !errors.Is(err, errLeaseBusy) {
		_ = command.Process.Kill()
		t.Fatalf("child lock contention = %v, want busy", err)
	}
	if err = command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	deadline := time.Now().Add(2 * time.Second)
	for {
		lease, err = acquireLease(path)
		if err == nil {
			_ = lease.Close()
			break
		}
		if !errors.Is(err, errLeaseBusy) || time.Now().After(deadline) {
			t.Fatalf("process exit did not release lease: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOpenBindsManagedADBEndpointAndReleasesLease(t *testing.T) {
	options, paths := fixtureOptions(t)
	writeJSON(t, paths.config, validProfile(paths.adb))
	options.Legacy = legacyConfig(paths.script, paths.config, paths.storage)
	lease := &trackedADBLease{}
	options.ADBServer = trackedADBServer{lease: lease}
	var captured mcptools.ServerConfig
	withHub(t, func(config mcptools.ServerConfig) (hub, error) {
		captured = config
		return &fakeHub{}, nil
	})
	bridge, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	var derived profile
	data, err := os.ReadFile(captured.Args[5])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &derived); err != nil {
		t.Fatal(err)
	}
	if derived.ADBHost != "127.0.0.1" || derived.ADBPort != 5037 {
		t.Fatalf("unbound adb endpoint: %#v", derived)
	}
	if _, err := bridge.Binding(); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Close(); err != nil {
		t.Fatal(err)
	}
	if lease.closes != 1 {
		t.Fatalf("managed adb lease closes=%d", lease.closes)
	}
}

func testPython(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"python", "python3"} {
		path, err := exec.LookPath(candidate)
		if err == nil {
			absolute, err := filepath.Abs(path)
			if err == nil {
				return absolute
			}
		}
	}
	t.Skip("a local Python interpreter is required only for the isolated stdio environment fixture")
	return ""
}

type fixturePaths struct {
	workspace  string
	state      string
	packageDir string
	adbRoot    string
	leaseDir   string
	python     string
	script     string
	config     string
	storage    string
	adb        string
}

func fixtureOptions(t *testing.T) (Options, fixturePaths) {
	t.Helper()
	root := t.TempDir()
	paths := fixturePaths{workspace: filepath.Join(root, "workspace"), state: filepath.Join(root, "state"), packageDir: filepath.Join(root, "package")}
	paths.adbRoot = filepath.Join(paths.workspace, "runtimes")
	paths.leaseDir = filepath.Join(paths.workspace, "device-leases")
	for _, directory := range []string{paths.workspace, paths.state, paths.packageDir, filepath.Join(paths.packageDir, "runtime"), paths.adbRoot, paths.leaseDir} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	paths.python = filepath.Join(paths.workspace, "python.exe")
	paths.script = filepath.Join(paths.packageDir, "runtime", "bridge.py")
	paths.config = filepath.Join(paths.workspace, "legacy-profile.json")
	paths.storage = filepath.Join(paths.state, "ledger")
	paths.adb = filepath.Join(paths.adbRoot, "adb.exe")
	for _, file := range []string{paths.python, paths.script, paths.adb} {
		if err := os.WriteFile(file, []byte("bridge"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(paths.storage, 0700); err != nil {
		t.Fatal(err)
	}
	return Options{Python: paths.python, PackageDirectory: paths.packageDir, WorkspaceDirectory: paths.workspace, StateDirectory: paths.state, GlobalLeaseDirectory: paths.leaseDir, ADBRoots: []string{paths.adbRoot}, Account: "account_1", Devices: []string{"allowed"}, Actions: []string{"tap", "capture"}, ADBServer: fakeADBServer{}}, paths
}

type fakeADBServer struct{}

func (fakeADBServer) Acquire(context.Context, string) (adbserver.Lease, error) {
	return fakeADBLease{}, nil
}

type fakeADBLease struct{}

func (fakeADBLease) Endpoint() adbserver.Endpoint { return adbserver.DefaultEndpoint() }
func (fakeADBLease) Binding() string              { return "synthetic-adb-server" }
func (fakeADBLease) Close() error                 { return nil }

type trackedADBServer struct{ lease *trackedADBLease }

func (s trackedADBServer) Acquire(context.Context, string) (adbserver.Lease, error) {
	return s.lease, nil
}

type trackedADBLease struct {
	closes int
	err    error
}

func (l *trackedADBLease) Endpoint() adbserver.Endpoint { return adbserver.DefaultEndpoint() }
func (l *trackedADBLease) Binding() string              { return "tracked-server" }
func (l *trackedADBLease) Close() error                 { l.closes++; return l.err }

func legacyConfig(script, config, storage string) mcptools.ServerConfig {
	return mcptools.ServerConfig{Transport: "stdio", Args: []string{"-I", "-B", "-u", script, "--config", config, "--storage", storage}}
}

func validProfile(adb string) profile {
	return profile{ADB: adb, Devices: map[string]profileDevice{"allowed": {Serial: "emulator-5554", Enabled: true, Packages: []string{"com.example.browser"}, Actions: []string{"observe", "tap", "capture"}, Browsers: []string{"com.example.browser"}, WebHosts: []string{"example.test"}}}}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func withHub(t *testing.T, replacement func(mcptools.ServerConfig) (hub, error)) {
	t.Helper()
	old := openHub
	openHub = replacement
	t.Cleanup(func() { openHub = old })
}
