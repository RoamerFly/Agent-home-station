package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func getCanvasFixture(t *testing.T, f *webFixture) canvasView {
	t.Helper()
	code, body, _ := f.request("GET", "/api/canvas", nil, false, false)
	var view canvasView
	if code != 200 || json.Unmarshal(body, &view) != nil {
		t.Fatal("canvas unavailable", code, string(body))
	}
	return view
}
func TestCanvasPersistsDesignWithoutChangingOperationalConfig(t *testing.T) {
	f := setup(t)
	if code, _, _ := f.request("GET", "/api/canvas", nil, false, false); code != 401 {
		t.Fatal(code)
	}
	f.login()
	before := f.manager.Config().Config
	view := getCanvasFixture(t, f)
	if view.Saved || len(view.Document.Nodes) != 24 || len(view.Document.Edges) < 30 {
		t.Fatal("incomplete default flow", view.Saved, len(view.Document.Nodes))
	}
	view.Document.Nodes = append(view.Document.Nodes, canvasNode{ID: "custom-note", Kind: "note", Title: "合成便签", Note: "规划说明，不是运行指令", Tone: "teal", X: -123.5, Y: 99.25})
	view.Document.Edges = append(view.Document.Edges, canvasEdge{ID: "custom-edge", From: "agent", To: "custom-note", Label: "计划"})
	input := map[string]any{"base_revision": view.Revision, "document": view.Document}
	if code, _, _ := f.request("POST", "/api/canvas", input, true, false); code != 403 {
		t.Fatal("CSRF missing", code)
	}
	code, body, _ := f.request("POST", "/api/canvas", input, true, true)
	var saved canvasView
	if code != 200 || json.Unmarshal(body, &saved) != nil || saved.Revision == view.Revision || !saved.Saved {
		t.Fatal(code, string(body))
	}
	if !reflect.DeepEqual(before, f.manager.Config().Config) || f.native.loginCalls.Load() != 0 {
		t.Fatal("diagram changed operational configuration")
	}
	loaded, err := f.manager.readCanvas()
	if err != nil || !reflect.DeepEqual(saved.Document, loaded.Document) {
		t.Fatal("positions or notes lost", err)
	}
	info, err := os.Stat(filepath.Join(f.dir, "flow-canvas.json"))
	if err != nil || info.Size() == 0 {
		t.Fatal(err)
	}
	// An older tab cannot silently overwrite a newer design.
	input["document"] = view.Document
	if code, _, _ := f.request("POST", "/api/canvas", input, true, true); code != 409 {
		t.Fatal("stale revision overwritten", code)
	}
	if now := getCanvasFixture(t, f); now.Revision != saved.Revision {
		t.Fatal("conflict mutated design")
	}
}

func TestCanvasRejectsInvalidGraphAndKeepsLastSavedDesign(t *testing.T) {
	f := setup(t)
	f.login()
	view := getCanvasFixture(t, f)
	for _, change := range []func(*canvasDocument){
		func(d *canvasDocument) { d.Nodes[0].Kind = "server_execute" },
		func(d *canvasDocument) { d.Nodes[0].Tone = "url(javascript:bad)" },
		func(d *canvasDocument) { d.Nodes[1].ID = d.Nodes[0].ID },
		func(d *canvasDocument) { d.Edges[0].To = "missing" },
		func(d *canvasDocument) { d.Nodes[0].X = 100001 },
	} {
		raw, _ := json.Marshal(view.Document)
		var doc canvasDocument
		_ = json.Unmarshal(raw, &doc)
		change(&doc)
		if code, _, _ := f.request("POST", "/api/canvas", map[string]any{"base_revision": view.Revision, "document": doc}, true, true); code != 400 {
			t.Fatal("invalid graph accepted", code)
		}
		if now := getCanvasFixture(t, f); now.Revision != view.Revision {
			t.Fatal("invalid request overwrote graph")
		}
	}
	// Removing everything is a valid, editable design, not a worker shutdown.
	empty := canvasDocument{Version: 1, Name: "空画布", Nodes: []canvasNode{}, Edges: []canvasEdge{}}
	if code, _, _ := f.request("POST", "/api/canvas", map[string]any{"base_revision": view.Revision, "document": empty}, true, true); code != 200 {
		t.Fatal("empty design rejected", code)
	}
}

func TestCanvasCorruptFileIsNotAutomaticallyReset(t *testing.T) {
	f := setup(t)
	f.login()
	path := filepath.Join(f.dir, "flow-canvas.json")
	original := []byte("retained-invalid-synthetic-file")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.request("GET", "/api/canvas", nil, false, false); code != 500 {
		t.Fatal(code)
	}
	if data, _ := os.ReadFile(path); string(data) != string(original) {
		t.Fatal("original file destroyed")
	}
}
