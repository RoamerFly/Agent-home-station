package station

import (
	"encoding/json"
	"os"
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
	if len(workflows) != 9 {
		t.Fatalf("got %d workflows", len(workflows))
	}
	graph, err := os.ReadFile(filepath.Join(root, "video", "h3-image-to-video.json"))
	if err != nil {
		t.Fatal(err)
	}
	var nodes map[string]struct {
		Class  string         `json:"class_type"`
		Inputs map[string]any `json:"inputs"`
	}
	if err := json.Unmarshal(graph, &nodes); err != nil {
		t.Fatal(err)
	}
	if nodes["5"].Class != "MiniMaxH3ImageToVideo" || nodes["5"].Inputs["first_frame"] == nil || nodes["5"].Inputs["ref_images.ref_image_0"] != nil {
		t.Fatal("image-to-video starter must anchor its first frame")
	}
	if nodes["1"].Inputs["unet_name"] != "minimax_h3_fl2va_pruned_int8_convrot-视频扩散-视频生成.safetensors" {
		t.Fatal("image-to-video starter selected the wrong model")
	}
	if _, err := os.Stat(filepath.Join(root, "video", "h3-image-reference-to-video.json")); err != nil {
		t.Fatal("reference-to-video starter missing:", err)
	}
	if err := localComfyInstallStarters(root); err != nil {
		t.Fatal(err)
	}
	if localComfyGraphName("../escape.json") || localComfyGraphName("image/../escape.json") || !localComfyGraphName("image/workflow.json") {
		t.Fatal("graph path validation")
	}
}

func TestComfyStarterMigratesOnlyUnmodifiedOldImageVideoGraph(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workflows", "comfyui")
	if err := os.MkdirAll(filepath.Join(root, "video"), 0700); err != nil {
		t.Fatal(err)
	}
	var registry localComfyWorkflowRegistry
	raw, _ := localComfyStarters.ReadFile("comfy_starters/registry.json")
	if err := json.Unmarshal(raw, &registry); err != nil {
		t.Fatal(err)
	}
	var imageVideo localComfyWorkflowRecord
	for _, item := range registry.Workflows {
		if item.ID == "h3-image-to-video" {
			imageVideo = item
			break
		}
	}
	imageVideo.SHA256 = oldH3ImageToVideoSHA
	registry.Workflows = []localComfyWorkflowRecord{imageVideo}
	oldGraph, _ := localComfyStarters.ReadFile("comfy_starters/video/h3-image-reference-to-video.json")
	if localComfyDigest(oldGraph) != oldH3ImageToVideoSHA {
		t.Fatal("old graph fixture does not match shipped release")
	}
	graphPath := filepath.Join(root, "video", "h3-image-to-video.json")
	if err := os.WriteFile(graphPath, oldGraph, 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(registry)
	if err := os.WriteFile(filepath.Join(root, localComfyWorkflowRegistryFile), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := localComfyInstallStarters(root); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(graphPath)
	if err != nil || localComfyDigest(updated) == oldH3ImageToVideoSHA {
		t.Fatal("unmodified old graph was not migrated", err)
	}
	workflows, err := loadLocalComfyWorkflows(root)
	if err != nil || len(workflows) != 9 {
		t.Fatalf("migrated workflows: %d, %v", len(workflows), err)
	}
}
