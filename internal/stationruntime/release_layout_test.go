package stationruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNestedServiceUsesReleaseEnvironment(t *testing.T) {
	release := t.TempDir()
	env := filepath.Join(release, "env")
	if err := os.Mkdir(env, 0700); err != nil {
		t.Fatal(err)
	}
	service := filepath.Join(release, "data", "main_setting", "runtime")
	options := PackagedToolOptionsAt(service)
	if options.Python != filepath.Join(env, "windows-python-cli", "python.exe") {
		t.Fatalf("wrong runtime: %s", options.Python)
	}
}
