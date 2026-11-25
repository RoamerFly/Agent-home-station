package station

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStationAuxiliaryDirectoriesRespectDataLayout(t *testing.T) {
	for _, structured := range []bool{false, true} {
		name := "legacy"
		if structured {
			name = "structured"
		}
		t.Run(name, func(t *testing.T) {
			data := filepath.Join(t.TempDir(), "data")
			if err := os.MkdirAll(data, 0700); err != nil {
				t.Fatal(err)
			}
			base := data
			if structured {
				if err := os.MkdirAll(filepath.Join(data, "applications"), 0700); err != nil {
					t.Fatal(err)
				}
				base = filepath.Join(data, "main_setting")
			}
			wantTasks := filepath.Join(base, "project-tool-tasks")
			wantStage := filepath.Join(base, "comfyui-output-stage")
			if structured {
				wantTasks = filepath.Join(base, "tmp", "project-tool-tasks")
				wantStage = filepath.Join(base, "tmp", "comfyui-output-stage")
			}
			wantUsage := filepath.Join(base, "local-vision-usage")
			if got := stationProjectToolTasksDir(data); got != wantTasks {
				t.Fatalf("task directory = %s, want %s", got, wantTasks)
			}
			if got := stationComfyOutputStageDir(data); got != wantStage {
				t.Fatalf("ComfyUI stage = %s, want %s", got, wantStage)
			}
			if got := stationVisionUsageDir(data); got != wantUsage {
				t.Fatalf("vision usage = %s, want %s", got, wantUsage)
			}
			tools := &localProjectTools{home: &localHomeService{dataDir: data}}
			_, err := tools.localVisionUsage("synthetic-fish")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(wantUsage, "synthetic-fish")); err != nil {
				t.Fatal("usage ledger not created under selected layout", err)
			}
		})
	}
}

func TestStructuredVisionUsageReadsOldLedgerUntilNewDirectoryExists(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	oldRoot := filepath.Join(data, "local-vision-usage")
	newRoot := filepath.Join(data, "main_setting", "local-vision-usage")
	oldFish := filepath.Join(oldRoot, "synthetic-fish")
	for _, path := range []string{filepath.Join(data, "applications"), oldFish} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	ledger := []byte(`{"version":1,"started_at":"2026-11-25T00:00:00Z","total":{"requests":7},"days":{},"models":{},"prices":{}}`)
	if err := os.WriteFile(filepath.Join(oldFish, "usage.json"), ledger, 0600); err != nil {
		t.Fatal(err)
	}
	if got := stationVisionUsageDir(data); got != oldRoot {
		t.Fatalf("old usage directory not selected: %s", got)
	}
	tools := &localProjectTools{home: &localHomeService{dataDir: data}}
	usage, err := tools.localVisionUsage("synthetic-fish")
	if err != nil || usage.Totals().Requests != 7 {
		t.Fatal("old ledger not read", usage, err)
	}
	if err := os.MkdirAll(newRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if got := stationVisionUsageDir(data); got != newRoot {
		t.Fatalf("new usage directory did not take priority: %s", got)
	}
}
