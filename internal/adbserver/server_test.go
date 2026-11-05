package adbserver

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeProcess struct {
	mu     sync.Mutex
	closes int
	err    error
}

func (p *fakeProcess) Close() error { p.mu.Lock(); defer p.mu.Unlock(); p.closes++; return p.err }

func newTestManager(endpoint Endpoint, probe Probe, starter Starter) (*Manager, error) {
	return newManager(endpoint, probe, starter, func(context.Context, string) error { return nil })
}

func TestAcquireReusesExternalServerWithoutOwningIt(t *testing.T) {
	var starts int
	m, err := newTestManager(DefaultEndpoint(), func(context.Context, Endpoint) error { return nil }, func(context.Context, string, Endpoint) (Process, error) { starts++; return &fakeProcess{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "adb.exe")
	first, err := m.Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if starts != 0 || first.Binding() != second.Binding() {
		t.Fatalf("external server startup/binding: starts=%d", starts)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireOwnsForegroundServerAndClosesOnlyAfterLastLease(t *testing.T) {
	var live bool
	process := &fakeProcess{}
	var argv []string
	m, err := newTestManager(DefaultEndpoint(), func(context.Context, Endpoint) error {
		if live {
			return nil
		}
		return ErrNoListener
	}, func(_ context.Context, path string, endpoint Endpoint) (Process, error) {
		argv = []string{path, endpoint.Host}
		live = true
		return process, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "adb.exe")
	first, err := m.Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 2 || argv[1] != "127.0.0.1" {
		t.Fatalf("foreground starter arguments not fixed: %#v", argv)
	}
	if first.Binding() != second.Binding() {
		t.Fatal("shared owner must have stable binding")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if process.closes != 0 {
		t.Fatal("closed owned server before final lease")
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if process.closes != 1 {
		t.Fatalf("close count=%d", process.closes)
	}
}

func TestDifferentTrustedADBPathsShareOneOwnedEndpointLease(t *testing.T) {
	var live bool
	process := &fakeProcess{}
	m, err := newTestManager(DefaultEndpoint(), func(context.Context, Endpoint) error {
		if live {
			return nil
		}
		return ErrNoListener
	}, func(_ context.Context, _ string, _ Endpoint) (Process, error) { live = true; return process, nil })
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	first, err := m.Acquire(context.Background(), filepath.Join(root, "one", "adb.exe"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Acquire(context.Background(), filepath.Join(root, "two", "adb.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if process.closes != 0 {
		t.Fatal("first binary path released shared server")
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if process.closes != 1 {
		t.Fatalf("final shared release closes=%d", process.closes)
	}
}

func TestAcquireBindRaceDoesNotKillExternalServer(t *testing.T) {
	var probes int
	process := &fakeProcess{}
	m, err := newTestManager(DefaultEndpoint(), func(context.Context, Endpoint) error {
		probes++
		if probes >= 2 {
			return nil
		}
		return ErrNoListener
	}, func(context.Context, string, Endpoint) (Process, error) { return process, nil })
	if err != nil {
		t.Fatal(err)
	}
	l, err := m.Acquire(context.Background(), filepath.Join(t.TempDir(), "adb.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if process.closes != 0 {
		t.Fatal("winner endpoint was reachable; manager must not kill a potentially external service")
	}
	_ = l.Close()
	if process.closes != 1 {
		t.Fatal("owned candidate must be reaped on final release")
	}
}

func TestEndpointIsStrictLoopback(t *testing.T) {
	for _, ep := range []Endpoint{{Host: "localhost", Port: 5037}, {Host: "127.0.0.1", Port: 5038}, {Host: "::1", Port: 5037}} {
		if _, err := newTestManager(ep, func(context.Context, Endpoint) error { return nil }, func(context.Context, string, Endpoint) (Process, error) { return &fakeProcess{}, nil }); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("accepted %#v", ep)
		}
	}
}

func TestForegroundServerArgsAreFixedAndNeverStartServer(t *testing.T) {
	got := serverArgs(DefaultEndpoint())
	want := []string{"-L", "tcp:localhost:5037", "server", "nodaemon"}
	if len(got) != len(want) {
		t.Fatalf("args=%#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args=%#v", got)
		}
	}
}

func TestControlledServerEnvironmentDoesNotInheritHostSecretsOrSelectors(t *testing.T) {
	t.Setenv("ADB_SERVER_SOCKET", "synthetic-selector")
	t.Setenv("ADB_SERVER_TEST_SECRET", "synthetic-secret")
	t.Setenv("PATH", "synthetic-path")
	t.Setenv("TEMP", "synthetic-temp")
	env := controlledServerEnv()
	joined := "\x00" + strings.Join(env, "\x00") + "\x00"
	if strings.Contains(joined, "ADB_SERVER_SOCKET=") || strings.Contains(joined, "ADB_SERVER_TEST_SECRET=") || strings.Contains(joined, "PATH=") {
		t.Fatalf("unsafe environment inherited")
	}
	if !strings.Contains(joined, "TEMP=synthetic-temp") {
		t.Fatal("required OS temp environment was removed")
	}
}

func TestDefaultStarterDoesNotPassHostSecretToChild(t *testing.T) {
	for _, argument := range os.Args {
		if argument == "adbserver-env-child" {
			if _, present := os.LookupEnv("ADB_SERVER_TEST_SECRET"); present {
				os.Exit(19)
			}
			return
		}
	}
	t.Setenv("ADB_SERVER_TEST_SECRET", "synthetic-secret")
	previous := command
	command = func(string, ...string) *exec.Cmd {
		return exec.Command(os.Args[0], "-test.run=^TestDefaultStarterDoesNotPassHostSecretToChild$", "--", "adbserver-env-child")
	}
	t.Cleanup(func() { command = previous })
	process, err := defaultStarter(context.Background(), "ignored-by-test", DefaultEndpoint())
	if err != nil {
		t.Fatal(err)
	}
	reaper := process.(reapedProcess)
	deadline := time.Now().Add(2 * time.Second)
	for !reaper.reaped() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !reaper.reaped() {
		t.Fatal("synthetic child was not reaped")
	}
	if err := reaper.waitError(); err != nil {
		t.Fatalf("host secret reached child: %v", err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformChildReapedExactlyOnceAfterNaturalExit(t *testing.T) {
	for _, argument := range os.Args {
		if argument == "adbserver-child" {
			return // child exits normally without a real ADB/device.
		}
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPlatformChildReapedExactlyOnceAfterNaturalExit$", "--", "adbserver-child")
	process, err := platformStart(cmd)
	if err != nil {
		t.Fatal(err)
	}
	reaper, ok := process.(reapedProcess)
	if !ok {
		t.Fatal("platform process does not expose test-only reaper state")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !reaper.reaped() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !reaper.reaped() {
		t.Fatal("naturally exited host child was not reaped")
	}
	if err := reaper.waitError(); err != nil {
		t.Fatalf("synthetic child failed: %v", err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	if err := process.Close(); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
}

func TestCanceledFirstAcquireContextCannotStopSharedServer(t *testing.T) {
	live := false
	process := &fakeProcess{}
	m, err := newTestManager(DefaultEndpoint(), func(context.Context, Endpoint) error {
		if live {
			return nil
		}
		return ErrNoListener
	}, func(context.Context, string, Endpoint) (Process, error) { live = true; return process, nil })
	if err != nil {
		t.Fatal(err)
	}
	firstContext, cancelFirst := context.WithCancel(context.Background())
	first, err := m.Acquire(firstContext, filepath.Join(t.TempDir(), "adb.exe"))
	if err != nil {
		t.Fatal(err)
	}
	cancelFirst()
	second, err := m.Acquire(context.Background(), filepath.Join(t.TempDir(), "adb.exe"))
	if err != nil {
		t.Fatalf("canceled first request poisoned shared server: %v", err)
	}
	if process.closes != 0 {
		t.Fatal("server followed first request context")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if process.closes != 0 {
		t.Fatal("first lease released shared server")
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if process.closes != 1 {
		t.Fatalf("final lease did not stop owned server: %d", process.closes)
	}
}

func TestCachedEndpointLossFailsClosed(t *testing.T) {
	live := false
	process := &fakeProcess{}
	m, err := newTestManager(DefaultEndpoint(), func(context.Context, Endpoint) error {
		if live {
			return nil
		}
		return ErrNoListener
	}, func(context.Context, string, Endpoint) (Process, error) { live = true; return process, nil })
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.Acquire(context.Background(), filepath.Join(t.TempDir(), "adb.exe"))
	if err != nil {
		t.Fatal(err)
	}
	live = false
	if _, err := m.Acquire(context.Background(), filepath.Join(t.TempDir(), "adb.exe")); !errors.Is(err, ErrNoListener) {
		t.Fatalf("stale endpoint accepted: %v", err)
	}
	if process.closes != 0 {
		t.Fatal("endpoint loss restarted or tore down while a lease exists")
	}
	_ = first.Close()
}

func TestFailedOwnedCloseBlocksNewAcquireUntilRetryConfirmsCleanup(t *testing.T) {
	live := false
	process := &flakyProcess{first: errors.New("still running")}
	m, err := newTestManager(DefaultEndpoint(), func(context.Context, Endpoint) error {
		if live {
			return nil
		}
		return ErrNoListener
	}, func(context.Context, string, Endpoint) (Process, error) { live = true; return process, nil })
	if err != nil {
		t.Fatal(err)
	}
	l, err := m.Acquire(context.Background(), filepath.Join(t.TempDir(), "adb.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err == nil {
		t.Fatal("first cleanup unexpectedly succeeded")
	}
	if _, err := m.Acquire(context.Background(), filepath.Join(t.TempDir(), "adb.exe")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("new acquire passed during uncertain cleanup: %v", err)
	}
	if err := CleanupQuarantined(); err != nil {
		t.Fatalf("cleanup registry retry: %v", err)
	}
	if process.closes != 2 {
		t.Fatalf("close attempts=%d", process.closes)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("same lease retry after registry cleanup: %v", err)
	}
}

func TestFailedStartupCleanupIsRetainedForProcessWideRetry(t *testing.T) {
	process := &flakyProcess{first: errors.New("still running")}
	m, err := newTestManager(DefaultEndpoint(), func(context.Context, Endpoint) error { return ErrNoListener }, func(context.Context, string, Endpoint) (Process, error) { return process, nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := m.Acquire(ctx, filepath.Join(t.TempDir(), "adb.exe")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("startup failure=%v", err)
	}
	if process.closes != 1 {
		t.Fatalf("startup cleanup calls=%d", process.closes)
	}
	if err := CleanupQuarantined(); err != nil {
		t.Fatalf("startup cleanup retry: %v", err)
	}
	if process.closes != 2 {
		t.Fatalf("startup retry calls=%d", process.closes)
	}
}

func TestFailedStartupCleanupBlocksReachableEndpointUntilOwnedProcessIsReaped(t *testing.T) {
	ready := false
	process := &flakyProcess{first: errors.New("still running")}
	m, err := newTestManager(DefaultEndpoint(), func(context.Context, Endpoint) error {
		if ready {
			return nil
		}
		return ErrNoListener
	}, func(context.Context, string, Endpoint) (Process, error) { return process, nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := m.Acquire(ctx, filepath.Join(t.TempDir(), "adb.exe")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("startup failure=%v", err)
	}
	ready = true // A server becomes reachable after the failed owned cleanup.
	if _, err := m.Acquire(context.Background(), filepath.Join(t.TempDir(), "adb.exe")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("reachable endpoint bypassed pending owned cleanup: %v", err)
	}
	if err := CleanupQuarantined(); err != nil {
		t.Fatalf("startup cleanup retry: %v", err)
	}
	lease, err := m.Acquire(context.Background(), filepath.Join(t.TempDir(), "adb.exe"))
	if err != nil {
		t.Fatalf("endpoint remained blocked after confirmed cleanup: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTCPProbeRequiresADBVersionHandshake(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		request := make([]byte, len("000chost:version"))
		_, _ = io.ReadFull(conn, request)
		_, _ = conn.Write([]byte("OKAY00040029"))
	}()
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	if err := tcpProbe(context.Background(), Endpoint{Host: "127.0.0.1", Port: port}); err != nil {
		t.Fatalf("valid adb handshake: %v", err)
	}
	<-done

	wrong, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	go func() {
		conn, err := wrong.Accept()
		if err == nil {
			defer conn.Close()
			_, _ = io.Copy(io.Discard, conn)
		}
	}()
	if err := tcpProbe(context.Background(), Endpoint{Host: "127.0.0.1", Port: uint16(wrong.Addr().(*net.TCPAddr).Port)}); err == nil {
		t.Fatal("accepted bare TCP listener as adb")
	}
}

func TestTCPProbeRejectsIncompatibleOrMalformedListener(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response string
		want     error
	}{
		{name: "different ADB protocol", response: "OKAY00040028", want: ErrIncompatibleServer},
		{name: "bad response", response: "FAIL", want: ErrUntrustedListener},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			go func() {
				conn, acceptErr := listener.Accept()
				if acceptErr != nil {
					return
				}
				defer conn.Close()
				request := make([]byte, len("000chost:version"))
				_, _ = io.ReadFull(conn, request)
				_, _ = conn.Write([]byte(tc.response))
			}()
			err = tcpProbe(context.Background(), Endpoint{Host: loopbackHost, Port: uint16(listener.Addr().(*net.TCPAddr).Port)})
			if !errors.Is(err, tc.want) {
				t.Fatalf("probe error=%v, want %v", err, tc.want)
			}
		})
	}
}

func TestTCPProbeReportsOnlyConnectionRefusalAsNoListener(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := Endpoint{Host: loopbackHost, Port: uint16(listener.Addr().(*net.TCPAddr).Port)}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tcpProbe(context.Background(), endpoint); !errors.Is(err, ErrNoListener) {
		t.Fatalf("closed endpoint error=%v, want %v", err, ErrNoListener)
	}
}

func TestAcquireOnlyStartsAfterVerifiedNoListener(t *testing.T) {
	for _, tc := range []struct {
		name  string
		probe error
		want  error
		start bool
	}{
		{name: "connection refused", probe: ErrNoListener, start: true},
		{name: "wrong ADB version", probe: ErrIncompatibleServer, want: ErrIncompatibleServer},
		{name: "malformed listener", probe: ErrUntrustedListener, want: ErrUntrustedListener},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			live := false
			m, err := newTestManager(DefaultEndpoint(), func(context.Context, Endpoint) error {
				if live {
					return nil
				}
				return tc.probe
			}, func(context.Context, string, Endpoint) (Process, error) {
				calls++
				live = true
				return &fakeProcess{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			lease, err := m.Acquire(context.Background(), filepath.Join(t.TempDir(), "adb.exe"))
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Fatalf("Acquire error=%v, want %v", err, tc.want)
				}
				if calls != 0 {
					t.Fatalf("starter calls=%d", calls)
				}
				return
			}
			if err != nil || lease == nil || !tc.start || calls != 1 {
				t.Fatalf("lease=%v error=%v starts=%d", lease, err, calls)
			}
			_ = lease.Close()
		})
	}
}

func TestAcquireRejectsUnapprovedClientBeforeProbeOrStart(t *testing.T) {
	var probes, starts int
	m, err := newManager(DefaultEndpoint(), func(context.Context, Endpoint) error {
		probes++
		return ErrNoListener
	}, func(context.Context, string, Endpoint) (Process, error) {
		starts++
		return &fakeProcess{}, nil
	}, func(context.Context, string) error { return errors.New("expected 1.0.41") })
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Acquire(context.Background(), filepath.Join(t.TempDir(), "adb.exe"))
	if !errors.Is(err, ErrUnapprovedClient) {
		t.Fatalf("Acquire error=%v", err)
	}
	if probes != 0 || starts != 0 {
		t.Fatalf("unapproved client probed=%d started=%d", probes, starts)
	}
}

func TestLimitedVersionOutputCapsRetainedBytes(t *testing.T) {
	output := &limitedOutput{limit: 4}
	n, err := output.Write([]byte("abcdef"))
	if err != nil || n != 6 || output.String() != "abcd" {
		t.Fatalf("write=%d err=%v retained=%q", n, err, output.String())
	}
}

type flakyProcess struct {
	closes int
	first  error
}

func (p *flakyProcess) Close() error {
	p.closes++
	if p.closes == 1 {
		return p.first
	}
	return nil
}
