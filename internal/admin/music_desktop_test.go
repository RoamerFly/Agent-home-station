package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"wechat-bot/internal/pluginruntime"
)

func TestWindowsMusicPolicyScopeAndForeignPortRefused(t *testing.T) {
	f := setup(t)
	if _, err := f.manager.startWindowsMusic(t.Context()); err == nil || !strings.Contains(err.Error(), "not_installed_or_authorized") {
		t.Fatal("unregistered process admitted", err)
	}
	public := filepath.Join(f.manager.managedRoot(), "public_workspace")
	python := filepath.Join(public, "runtimes", "python", "python.exe")
	if err := os.MkdirAll(filepath.Dir(python), 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(python, []byte("synthetic executable"), 0600)
	control := t.TempDir()
	entry := filepath.Join(control, "managed_music_service.py")
	os.WriteFile(entry, []byte("synthetic control"), 0600)
	config := windowsMusicConfig{Enabled: true, Policy: windowsMusicPolicy, Python: python, Entry: entry, Media: control, IPC: control}
	save := func() {
		data, _ := json.Marshal(config)
		if err := os.WriteFile(filepath.Join(f.manager.dir, "windows-music.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	config.Policy = "unrestricted"
	save()
	if _, err := f.manager.startWindowsMusic(t.Context()); err == nil {
		t.Fatal("unknown policy admitted")
	}
	config.Policy = windowsMusicPolicy
	config.Entry = filepath.Join(public, "managed_music_service.py")
	os.WriteFile(config.Entry, []byte("agent visible control"), 0600)
	save()
	if _, err := f.manager.startWindowsMusic(t.Context()); err == nil || !strings.Contains(err.Error(), "control_inside") {
		t.Fatal("control plane mounted", err)
	}
	config.Entry = entry
	save()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	f.manager.speech.YuEEndpoint = "http://" + listener.Addr().String()
	f.manager.speech.YuEToken = "synthetic-local-token"
	installLegacyLocalMusicPlugin(t, f.manager)
	if _, err := f.manager.startWindowsMusic(t.Context()); err == nil || !strings.Contains(err.Error(), "port_occupied") {
		t.Fatal("foreign listener adopted", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.manager.startWindowsMusic(ctx); err != context.Canceled {
		t.Fatal("cancelled launch admitted", err)
	}
	listener.Close()
	revoked := errors.New("synthetic source revoked during launch preparation")
	ctx = context.WithValue(t.Context(), musicServiceControlCheckKey{}, func(context.Context) error {
		// Simulate authority changing after the private credential has been
		// prepared, before the final process admission. The fake executable
		// must never be invoked, and no process may be adopted.
		if _, err := os.Stat(filepath.Join(f.manager.dir, "windows-music-token")); err == nil {
			return revoked
		}
		return nil
	})
	if _, err := f.manager.startWindowsMusic(ctx); !errors.Is(err, revoked) || f.manager.windowsMusicStatus().Running {
		t.Fatal("launch preparation bypassed current source check", err)
	}
}

func TestManagedServiceRegistryInitializationDoesNotAcquireManagerLock(t *testing.T) {
	m := &Manager{}
	m.mu.Lock()
	initialized := make(chan *pluginruntime.ManagedServices, 1)
	go func() { initialized <- m.managedServiceRegistry() }()
	var registry *pluginruntime.ManagedServices
	select {
	case registry = <-initialized:
		m.mu.Unlock()
	case <-time.After(2 * time.Second):
		m.mu.Unlock()
		t.Fatal("generic registry initialization waits on Manager.mu")
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if m.managedServiceRegistry() != registry {
				t.Error("multiple registry instances")
			}
		}()
	}
	wg.Wait()
}
