// Package adbserver owns the local ADB server used by the native device
// broker. It deliberately lives outside individual MCP session jobs: the ADB
// server is machine shared, while a bridge session is not.
package adbserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	loopbackHost = "127.0.0.1"
	defaultPort  = uint16(5037)
	// approvedServerVersion is ADB_SERVER_VERSION for the trusted native
	// bridge/client contract. AOSP clients kill and restart a server on a
	// version mismatch, so accepting another version is unsafe.
	approvedServerVersion = uint32(41)
)

var (
	ErrUnavailable        = errors.New("adb_server_unavailable")
	ErrNoListener         = errors.New("adb_server_no_listener")
	ErrIncompatibleServer = errors.New("adb_server_incompatible")
	ErrUntrustedListener  = errors.New("adb_server_untrusted_listener")
	ErrUnapprovedClient   = errors.New("adb_client_unapproved")
)

// Endpoint is intentionally limited to the numeric IPv4 loopback address.
// AOSP treats hostname "localhost" as local and will auto-start a missing
// server; it treats 127.0.0.1 as remote and therefore fails closed instead.
type Endpoint struct {
	Host string
	Port uint16
}

func DefaultEndpoint() Endpoint { return Endpoint{Host: loopbackHost, Port: defaultPort} }

// Process is the small, testable host-owned foreground server process. Close
// must terminate only this exact process tree, never issue adb kill-server.
type Process interface {
	Close() error
}

// Starter starts the fixed foreground `adb -L tcp:localhost:5037 server
// nodaemon` outside an MCP session job. It returns a handle only for the
// process it started. Clients still use numeric 127.0.0.1 to fail closed.
type Starter func(context.Context, string, Endpoint) (Process, error)

// Probe connects to the fixed server endpoint. It must not invoke adb or have
// any daemon-start side effect.
type Probe func(context.Context, Endpoint) error

// VersionReader verifies a client without connecting it to the ADB server.
// It is injectable so tests never execute a local platform-tools binary.
type VersionReader func(context.Context, string) error

// Manager reuses one endpoint in this process. It never takes ownership of a
// server it did not start, and only closes a foreground process handle it owns.
type Manager struct {
	mu        sync.Mutex
	endpoint  Endpoint
	probe     Probe
	starter   Starter
	version   VersionReader
	instances map[string]*instance
}

type instance struct {
	refs      int
	owned     Process
	binding   string
	closing   bool
	cleanupID uint64
}

// Lease is held by a devicebroker for its lifetime. Its Close releases only
// this caller's reference; the final release closes an owned foreground
// process, while an externally running server is left untouched.
type Lease interface {
	Endpoint() Endpoint
	Binding() string
	Close() error
}

type lease struct {
	m        *Manager
	key      string
	ep       Endpoint
	binding  string
	mu       sync.Mutex
	released bool
}

var (
	defaultOnce       sync.Once
	defaultManager    *Manager
	defaultManagerErr error
)

var quarantined = struct {
	sync.Mutex
	next    uint64
	retries map[uint64]func() error
}{retries: map[uint64]func() error{}}

// CleanupQuarantined retries only termination of an already-owned foreground
// process. It never starts a server or replays a bridge/device operation.
func CleanupQuarantined() error {
	quarantined.Lock()
	items := make(map[uint64]func() error, len(quarantined.retries))
	for id, retry := range quarantined.retries {
		items[id] = retry
	}
	quarantined.Unlock()
	var first error
	for id, retry := range items {
		if err := retry(); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		quarantined.Lock()
		delete(quarantined.retries, id)
		quarantined.Unlock()
	}
	return first
}

func retainCleanup(retry func() error) uint64 {
	id := atomic.AddUint64(&quarantined.next, 1)
	quarantined.Lock()
	quarantined.retries[id] = retry
	quarantined.Unlock()
	return id
}
func releaseCleanup(id uint64) {
	if id != 0 {
		quarantined.Lock()
		delete(quarantined.retries, id)
		quarantined.Unlock()
	}
}

// Acquire uses the process-wide host manager. It is intentionally separate
// from MCP sessions so separate account brokers share the same owned server.
func Acquire(ctx context.Context, adbPath string) (Lease, error) {
	defaultOnce.Do(func() { defaultManager, defaultManagerErr = New(DefaultEndpoint(), tcpProbe, defaultStarter) })
	if defaultManagerErr != nil {
		return nil, defaultManagerErr
	}
	return defaultManager.Acquire(ctx, adbPath)
}

func (l *lease) Endpoint() Endpoint { return l.ep }
func (l *lease) Binding() string    { return l.binding }
func (l *lease) Close() error {
	l.mu.Lock()
	decrement := !l.released
	if decrement {
		l.released = true
	}
	l.mu.Unlock()
	return l.m.release(l.key, decrement)
}

// New creates a host-side manager. No server process is started until Acquire.
func New(endpoint Endpoint, probe Probe, starter Starter) (*Manager, error) {
	return newManager(endpoint, probe, starter, readADBVersion)
}

func newManager(endpoint Endpoint, probe Probe, starter Starter, version VersionReader) (*Manager, error) {
	if endpoint.Host == "" && endpoint.Port == 0 {
		endpoint = DefaultEndpoint()
	}
	if endpoint.Host != loopbackHost || endpoint.Port != defaultPort || probe == nil || starter == nil || version == nil {
		return nil, ErrUnavailable
	}
	return &Manager{endpoint: endpoint, probe: probe, starter: starter, version: version, instances: map[string]*instance{}}, nil
}

// Acquire first reuses a live server. If none exists, it starts an explicitly
// owned *foreground* server outside MCP jobs and waits only for that endpoint.
// A bind race is safe: if another owner wins and the endpoint becomes live,
// the unsuccessful child is closed and the external server is merely reused.
func (m *Manager) Acquire(ctx context.Context, adbPath string) (Lease, error) {
	if ctx == nil || ctx.Err() != nil || !filepath.IsAbs(adbPath) {
		return nil, ErrUnavailable
	}
	adbPath = filepath.Clean(adbPath)
	if err := m.version(ctx, adbPath); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnapprovedClient, err)
	}
	// The endpoint, not the adb binary, owns the process. Different trusted
	// platform-tools installations still address the same 127.0.0.1:5037
	// server; keying by executable would let one broker stop it under another.
	key := m.endpoint.Host + "\x00" + strconv.Itoa(int(m.endpoint.Port))
	m.mu.Lock()
	defer m.mu.Unlock()
	if current := m.instances[key]; current != nil {
		if current.closing {
			return nil, ErrUnavailable
		}
		// Existing leases bind this server. If it is gone, fail closed rather
		// than starting a replacement beneath unknown in-flight work.
		if err := m.probe(ctx, m.endpoint); err != nil {
			return nil, err
		}
		current.refs++
		return &lease{m: m, key: key, ep: m.endpoint, binding: current.binding}, nil
	}
	if err := m.probe(ctx, m.endpoint); err == nil {
		binding := makeBinding(adbPath, m.endpoint, false)
		m.instances[key] = &instance{refs: 1, binding: binding}
		return &lease{m: m, key: key, ep: m.endpoint, binding: binding}, nil
	} else if !errors.Is(err, ErrNoListener) {
		// A reachable endpoint with a malformed or incompatible ADB handshake
		// must never be replaced: an AOSP client could kill it while trying to
		// reconnect. Only a verified connection refusal may start our server.
		return nil, err
	}
	owned, err := m.starter(ctx, adbPath, m.endpoint)
	if err != nil || owned == nil {
		return nil, ErrUnavailable
	}
	if err := waitForEndpoint(ctx, m.probe, m.endpoint); err != nil {
		// This only closes our exact failed child; it never uses kill-server.
		if closeErr := owned.Close(); closeErr != nil {
			// Open has no lease to retain this process. Keep an endpoint-local
			// closing tombstone as well as a process-wide retry reference: if the
			// server becomes reachable after this failed cleanup, it is still our
			// uncertain process, not an external service that may receive leases.
			current := &instance{owned: owned, binding: makeBinding(adbPath, m.endpoint, true), closing: true}
			m.instances[key] = current
			current.cleanupID = retainCleanup(func() error { return m.release(key, false) })
		}
		return nil, ErrUnavailable
	}
	binding := makeBinding(adbPath, m.endpoint, true)
	m.instances[key] = &instance{refs: 1, owned: owned, binding: binding}
	return &lease{m: m, key: key, ep: m.endpoint, binding: binding}, nil
}

func (m *Manager) release(key string, decrement bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.instances[key]
	if current == nil {
		return nil
	}
	if decrement {
		if current.refs <= 0 {
			return ErrUnavailable
		}
		current.refs--
	}
	if current.refs > 0 {
		return nil
	}
	current.closing = true
	if current.owned != nil {
		if err := current.owned.Close(); err != nil {
			// Keep the exact process handle and block new acquisitions until a
			// later Close confirms cleanup. Never reinterpret it as external.
			if current.cleanupID == 0 {
				current.cleanupID = retainCleanup(func() error { return m.release(key, false) })
			}
			return err
		}
	}
	delete(m.instances, key)
	releaseCleanup(current.cleanupID)
	return nil
}

func waitForEndpoint(ctx context.Context, probe Probe, endpoint Endpoint) error {
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := probe(ctx, endpoint); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return ErrUnavailable
		case <-tick.C:
		}
	}
}

func makeBinding(adbPath string, endpoint Endpoint, owned bool) string {
	sum := sha256.Sum256([]byte(adbPath + "\x00" + endpoint.Host + "\x00" + strconv.Itoa(int(endpoint.Port)) + "\x00" + strconv.FormatUint(uint64(approvedServerVersion), 10) + "\x00" + map[bool]string{true: "owned", false: "external"}[owned]))
	return hex.EncodeToString(sum[:])
}

func tcpProbe(ctx context.Context, endpoint Endpoint) error {
	dialer := net.Dialer{Timeout: 250 * time.Millisecond}
	conn, err := dialer.DialContext(ctx, "tcp4", net.JoinHostPort(endpoint.Host, strconv.Itoa(int(endpoint.Port))))
	if err != nil {
		// Windows reports Winsock WSAECONNREFUSED (10061), whereas Unix uses
		// ECONNREFUSED. Do not treat timeouts, resets, or arbitrary dial errors
		// as absence: any reachable/ambiguous endpoint remains fail-closed.
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.Errno(10061)) {
			return ErrNoListener
		}
		return fmt.Errorf("%w: dial failed", ErrUntrustedListener)
	}
	defer conn.Close()
	deadline := time.Now().Add(250 * time.Millisecond)
	if callerDeadline, ok := ctx.Deadline(); ok && callerDeadline.Before(deadline) {
		deadline = callerDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("%w: deadline", ErrUntrustedListener)
	}
	// A bare TCP listener is not an ADB server. Verify the bounded smart-socket
	// host:version handshake before reusing or treating an endpoint as ready.
	if _, err := io.WriteString(conn, "000chost:version"); err != nil {
		return fmt.Errorf("%w: write handshake", ErrUntrustedListener)
	}
	var status [4]byte
	if _, err := io.ReadFull(conn, status[:]); err != nil {
		return fmt.Errorf("%w: read status", ErrUntrustedListener)
	}
	if string(status[:]) != "OKAY" {
		return ErrUntrustedListener
	}
	var length [4]byte
	if _, err := io.ReadFull(conn, length[:]); err != nil {
		return fmt.Errorf("%w: read length", ErrUntrustedListener)
	}
	n, err := strconv.ParseUint(string(length[:]), 16, 16)
	if err != nil || n == 0 || n > 64 {
		return ErrUntrustedListener
	}
	value := make([]byte, int(n))
	if _, err := io.ReadFull(conn, value); err != nil {
		return fmt.Errorf("%w: read version", ErrUntrustedListener)
	}
	version, err := strconv.ParseUint(string(value), 16, 32)
	if err != nil {
		return fmt.Errorf("%w: invalid version", ErrUntrustedListener)
	}
	if uint32(version) != approvedServerVersion {
		return fmt.Errorf("%w: got %d", ErrIncompatibleServer, version)
	}
	return nil
}

func readADBVersion(ctx context.Context, adbPath string) error {
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bounded, adbPath, "version")
	cmd.Env = controlledServerEnv()
	configureVersionCommand(cmd)
	output := &limitedOutput{limit: 16 << 10}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return ErrUnapprovedClient
	}
	for _, line := range strings.Split(output.String(), "\n") {
		if strings.TrimSpace(line) == "Android Debug Bridge version 1.0.41" {
			return nil
		}
	}
	return ErrUnapprovedClient
}

// limitedOutput bounds memory consumed by the trusted client's version banner.
// Excess output is discarded rather than retained; the command's context still
// bounds execution time.
type limitedOutput struct {
	bytes.Buffer
	limit int
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	written := len(p)
	if remaining := w.limit - w.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = w.Buffer.Write(p)
	}
	return written, nil
}
