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
