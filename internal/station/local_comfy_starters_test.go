package station

import (
	"path/filepath"
	"testing"
)

func TestComfyStarterWorkflowsLoadFromNestedHomeDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workflows", "comfyui")
	if err := localComfyInstallStarters(root); err != nil {
		t.Fatal(err)
	}
	workflows, err := loadLocalComfyWorkflows(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(workflows) != 8 {
		t.Fatalf("got %d workflows", len(workflows))
	}
	if err := localComfyInstallStarters(root); err != nil {
		t.Fatal(err)
	}
	if localComfyGraphName("../escape.json") || localComfyGraphName("image/../escape.json") || !localComfyGraphName("image/workflow.json") {
		t.Fatal("graph path validation")
	}
}
