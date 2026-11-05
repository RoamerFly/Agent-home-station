package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMusicCatalogUsesInstalledDescriptionsAndResolvedNativeSchema(t *testing.T) {
	f, access, _ := localMusicPluginFixture(t, "http://127.0.0.1:8765")
	resources, err := f.manager.musicCatalogResources(t.Context(), musicToolCatalogFile, musicNativeSamplingFile)
	if err != nil {
		t.Fatal(err)
	}
	var declared struct {
		Version int                                  `json:"version"`
		Tools   []struct{ Name, Description string } `json:"tools"`
	}
	if json.Unmarshal(resources[musicToolCatalogFile], &declared) != nil {
		t.Fatal("invalid installed catalogue")
	}
	tools := access.yueTools()
	if len(tools) != 8 || len(tools) != len(declared.Tools) {
		t.Fatal("installed tool catalogue not discovered", len(tools))
	}
	found := false
	for index, tool := range tools {
		if tool.Name != declared.Tools[index].Name || tool.Description != declared.Tools[index].Description {
			t.Fatal("tool description did not come from installed package", tool.Name)
		}
		if strings.Contains(string(tool.InputSchema), "$ref") {
			t.Fatal("unresolved external schema leaked into discovery", tool.Name)
		}
		if tool.Name == "music_local_start" {
			found = strings.Contains(string(tool.InputSchema), "sampler_name") && strings.Contains(string(tool.InputSchema), "planning_mode")
		}
	}
	if !found {
		t.Fatal("native sampling declaration was not attached")
	}
	valid := musicSampling{"semantic": {"temperature": float64(0)}, "render": {"steps": float64(77), "sampler_name": "a_backend_enum_chosen_from_live_options"}}
	if err = access.validateMusicSampling(t.Context(), valid, "melody"); err != nil {
		t.Fatal("explicit zero/selected values were changed or rejected", err)
	}
	for _, invalid := range []musicSampling{{"unknown": {}}, {"render": {"unknown_key": float64(2)}}, {"semantic": {"temperature": true}}, {"render": {"sampler_name": ""}}, {"score": nil}} {
		if err = access.validateMusicSampling(t.Context(), invalid, ""); err == nil || !strings.Contains(err.Error(), "music_sampling_options") {
			t.Fatal("installed JSON shape protection lost", invalid, err)
		}
	}
	if err = access.validateMusicSampling(t.Context(), nil, "invented-planning-mode"); err == nil {
		t.Fatal("installed enum not validated")
	}
}

func TestMusicCatalogMissingDisabledOrModifiedPackageHasNoBuiltinFallback(t *testing.T) {
	for _, mode := range []string{"missing", "disabled", "modified_other_file"} {
		t.Run(mode, func(t *testing.T) {
			f, access, store := localMusicPluginFixture(t, "http://127.0.0.1:8765")
			i, err := store.Installed(t.Context(), "music")
			if err != nil {
				t.Fatal(err)
			}
			dir, err := store.PackageDirectory(i.Digest)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing":
				err = os.Remove(filepath.Join(dir, filepath.FromSlash(musicToolCatalogFile)))
			case "disabled":
				_, err = store.SetEnabled(t.Context(), "music", i.Revision, false)
			case "modified_other_file":
				err = os.WriteFile(filepath.Join(dir, "runtime", "cloud_music.py"), []byte("modified synthetic package\n"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if tools, err := f.manager.musicToolCatalog(t.Context()); err == nil || len(tools) != 0 {
				t.Fatal("invalid package returned built-in catalogue", mode, err)
			}
			if tools := access.yueTools(); len(tools) != 0 {
				t.Fatal("invalid package retained hardcoded discovery", mode)
			}
			if err = access.validateMusicSampling(t.Context(), musicSampling{"semantic": {"temperature": float64(0)}}, ""); err == nil {
				t.Fatal("invalid package retained hardcoded native option validation", mode)
			}
		})
	}
}
