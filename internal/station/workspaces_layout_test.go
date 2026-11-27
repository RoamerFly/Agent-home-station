package station

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStructuredWorkspaceUsesMigratedRootWhenPresent(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(filepath.Join(data, "main_setting"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(data, "applications"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(data, "workspaces"), 0700); err != nil {
		t.Fatal(err)
	}
	if got := stationWorkspaceDir(data); got != filepath.Join(data, "workspaces") {
		t.Fatal(got)
	}
}

func TestWorkspaceRootKeepsExistingLegacyInstallation(t *testing.T) {
	for _, tc := range []struct {
		name, legacy string
		structured   bool
	}{
		{"structured", "workspace", true},
		{"unstructured", "managed-workspaces", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := filepath.Join(t.TempDir(), "data")
			if tc.structured {
				if err := os.MkdirAll(filepath.Join(data, "applications"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			legacy := filepath.Join(data, tc.legacy)
			if err := os.MkdirAll(legacy, 0700); err != nil {
				t.Fatal(err)
			}
			if got := DefaultWorkspaceRoot(data); got != legacy {
				t.Fatalf("workspace root = %q, want %q", got, legacy)
			}
		})
	}
}

func TestWorkspaceRootPrefersModernWhenBothExist(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	for _, name := range []string{"applications", "workspace", "workspaces"} {
		if err := os.MkdirAll(filepath.Join(data, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := DefaultWorkspaceRoot(data), filepath.Join(data, "workspaces"); got != want {
		t.Fatalf("workspace root = %q, want %q", got, want)
	}
}

func TestPersonaOnlyModernRootKeepsLegacyHome(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	legacy := filepath.Join(data, "workspace")
	if err := os.MkdirAll(filepath.Join(legacy, "home", "projects"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(data, "applications"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := ensureSharedPersonaDocument(data); err != nil {
		t.Fatal(err)
	}
	if got := DefaultWorkspaceRoot(data); got != legacy {
		t.Fatalf("persona-only root displaced legacy home: %q", got)
	}
}
