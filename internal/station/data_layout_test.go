package station

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStationToolDataDirUsesLegacyUntilToolsDirectoryExists(t *testing.T) {
	data := t.TempDir()
	for _, name := range []string{"applications", "publictools", "main_setting"} {
		if err := makeDir(filepath.Join(data, name)); err != nil {
			t.Fatal(err)
		}
	}

	legacy := filepath.Join(data, "publictools")
	if got := stationToolRoot(data, "rvc"); got != filepath.Join(legacy, "rvc") {
		t.Fatalf("legacy tool root = %q", got)
	}
	if got := stationToolRegistrationPath(data, "station-rvc.json"); got != filepath.Join(legacy, "registrations", "station-rvc.json") {
		t.Fatalf("registration path moved with tool data = %q", got)
	}

	if err := makeDir(filepath.Join(data, "tools")); err != nil {
		t.Fatal(err)
	}
	if got := stationToolRoot(data, "rvc"); got != filepath.Join(data, "tools", "rvc") {
		t.Fatalf("migrated tool root = %q", got)
	}
	if got := stationToolPrivateDir(data, "rvc"); got != filepath.Join(data, "tools", "rvc", "private") {
		t.Fatalf("migrated private dir = %q", got)
	}
}

func makeDir(path string) error {
	return os.MkdirAll(path, 0o755)
}
