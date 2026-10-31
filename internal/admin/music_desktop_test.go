package admin

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if _, err := f.manager.startWindowsMusic(t.Context()); err == nil || !strings.Contains(err.Error(), "port_occupied") {
		t.Fatal("foreign listener adopted", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.manager.startWindowsMusic(ctx); err != context.Canceled {
		t.Fatal("cancelled launch admitted", err)
	}
}

func TestWindowsMusicBusyIncludesJobsAndCorruption(t *testing.T) {
	f := setup(t)
	if f.manager.windowsMusicBusy() {
		t.Fatal("unstarted music marked busy")
	}
	f.manager.windowsMusic = &windowsMusicProcess{done: make(chan struct{})}
	defer func() { f.manager.windowsMusic = nil }()
	path := filepath.Join(f.manager.privateWorkspaceFor(f.manager.native.Account()), "music/jobs/jobs.json")
	os.MkdirAll(filepath.Dir(path), 0700)
	for _, item := range []struct {
		data string
		busy bool
	}{{`{"a":{"state":"running"}}`, true}, {`{"a":{"state":"queued"}}`, true}, {`{"a":{"state":"completed"}}`, false}, {`invalid`, true}} {
		os.WriteFile(path, []byte(item.data), 0600)
		if f.manager.windowsMusicBusy() != item.busy {
			t.Fatal("wrong drain decision", item.data)
		}
	}
	close(f.manager.windowsMusic.done)
	if f.manager.windowsMusicBusy() {
		t.Fatal("exited service retained a busy lock")
	}
}
