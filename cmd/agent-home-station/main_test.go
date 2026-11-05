package main

import (
	"path/filepath"
	"testing"
)

func TestDefaultPathsUseExecutableLocation(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "bin", "Agent-home-station.exe")
	cfg, err := parseConfig(nil, executable)
	if err != nil {
		t.Fatalf("parseConfig() error = %v", err)
	}
	wantData := filepath.Join(filepath.Dir(executable), "data", "station")
	if cfg.dataDir != wantData {
		t.Fatalf("dataDir = %q, want %q", cfg.dataDir, wantData)
	}
	wantWorkspace := filepath.Join(wantData, "managed-workspaces")
	if cfg.workspaceRoot != wantWorkspace {
		t.Fatalf("workspaceRoot = %q, want %q", cfg.workspaceRoot, wantWorkspace)
	}
}

func TestExplicitPathsMustBeAbsolute(t *testing.T) {
	_, err := parseConfig([]string{"-data", "relative"}, `C:\\Program Files\\Agent Home Station\\Agent-home-station.exe`)
	if err == nil {
		t.Fatal("relative data path was accepted")
	}
	_, err = parseConfig([]string{"-workspace", "relative"}, `C:\\Program Files\\Agent Home Station\\Agent-home-station.exe`)
	if err == nil {
		t.Fatal("relative workspace path was accepted")
	}
}

func TestLocalListenAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8082", "localhost:8082", "[::1]:8082", "127.0.0.1:0"} {
		if _, err := localListenAddress(address); err != nil {
			t.Errorf("localListenAddress(%q) unexpected error: %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8082", "[::]:8082", "192.168.1.5:8082", "example.com:8082", "localhost", ":8082", "127.0.0.1:bad"} {
		if _, err := localListenAddress(address); err == nil {
			t.Errorf("localListenAddress(%q) unexpectedly succeeded", address)
		}
	}
}
