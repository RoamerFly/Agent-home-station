package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Canvas documents contain only a diagram. Operational settings stay in their
// existing APIs; moving/removing a node cannot change the running Agent.
type canvasNode struct {
	ID    string  `json:"id"`
	Kind  string  `json:"kind"`
	Title string  `json:"title"`
	Note  string  `json:"note"`
	Tone  string  `json:"tone"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
}
type canvasEdge struct {
	ID    string `json:"id"`
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
}
type canvasDocument struct {
	Version int          `json:"version"`
	Name    string       `json:"name"`
	Nodes   []canvasNode `json:"nodes"`
	Edges   []canvasEdge `json:"edges"`
}
type canvasView struct {
	Document canvasDocument `json:"document"`
	Revision string         `json:"revision"`
	Saved    bool           `json:"saved"`
}

var canvasID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func canvasText(value string, max int) bool {
	return utf8.ValidString(value) && len(value) <= max && !strings.ContainsRune(value, '\x00')
}
func validateCanvas(doc canvasDocument) error {
	if doc.Version != 1 || strings.TrimSpace(doc.Name) == "" || !canvasText(doc.Name, 120) || doc.Nodes == nil || doc.Edges == nil {
		return errors.New("请选择有效的画布名称与版本")
	}
	if len(doc.Nodes) > 200 || len(doc.Edges) > 600 {
		return errors.New("单画布最多容纳200个节点和600条连线，请拆分设计")
	}
	defaults, _ := assets.ReadFile("static/flow-default.json")
	var original canvasDocument
	if json.Unmarshal(defaults, &original) != nil {
		return errors.New("默认流程不可用")
	}
	kinds := map[string]bool{"note": true, "condition": true, "action": true}
	for _, node := range original.Nodes {
		kinds[node.Kind] = true
	}
	tones := map[string]bool{"blue": true, "teal": true, "violet": true, "amber": true, "rose": true, "slate": true}
	nodes := map[string]bool{}
	for _, n := range doc.Nodes {
		if !canvasID.MatchString(n.ID) || nodes[n.ID] || !kinds[n.Kind] || !tones[n.Tone] || strings.TrimSpace(n.Title) == "" || !canvasText(n.Title, 120) || !canvasText(n.Note, 8192) || math.IsNaN(n.X) || math.IsNaN(n.Y) || math.IsInf(n.X, 0) || math.IsInf(n.Y, 0) || math.Abs(n.X) > 100000 || math.Abs(n.Y) > 100000 {
			return errors.New("节点类型、名称、说明或位置无效，或节点编号重复")
		}
		nodes[n.ID] = true
	}
	edges := map[string]bool{}
	for _, e := range doc.Edges {
		if !canvasID.MatchString(e.ID) || edges[e.ID] || !nodes[e.From] || !nodes[e.To] || !canvasText(e.Label, 240) {
			return errors.New("连线无效：请检查端点、重复编号和标签")
		}
		edges[e.ID] = true
	}
	encoded, err := json.Marshal(doc)
	if err != nil || len(encoded) > 120<<10 {
		return errors.New("画布超过120KiB，请精简说明或拆分设计")
	}
	return nil
}
func (m *Manager) readCanvas() (canvasView, error) {
	data, err := os.ReadFile(filepath.Join(m.dir, "flow-canvas.json"))
	saved := true
	if errors.Is(err, os.ErrNotExist) {
		data, err = assets.ReadFile("static/flow-default.json")
		saved = false
	}
	if err != nil || len(data) > 128<<10 {
		return canvasView{}, errors.New("画布文件不可读，原文件已保留")
	}
	var doc canvasDocument
	if json.Unmarshal(data, &doc) != nil || validateCanvas(doc) != nil {
		return canvasView{}, errors.New("画布文件格式无效，原文件已保留")
	}
	canonical, _ := json.Marshal(doc)
	sum := sha256.Sum256(canonical)
	return canvasView{Document: doc, Revision: hex.EncodeToString(sum[:]), Saved: saved}, nil
}
func (s *Server) canvasRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/canvas", s.protected(func(w http.ResponseWriter, r *http.Request) {
		s.manager.canvasMu.Lock()
		defer s.manager.canvasMu.Unlock()
		view, err := s.manager.readCanvas()
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		respond(w, 200, view)
	}))
	mux.HandleFunc("POST /api/canvas", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			BaseRevision string         `json:"base_revision"`
			Document     canvasDocument `json:"document"`
		}
		if !decode(w, r, &input) {
			return
		}
		if err := validateCanvas(input.Document); err != nil {
			fail(w, 400, err.Error())
			return
		}
		s.manager.canvasMu.Lock()
		defer s.manager.canvasMu.Unlock()
		current, err := s.manager.readCanvas()
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		if input.BaseRevision != current.Revision {
			fail(w, 409, "画布已在其他页面更新，当前草稿已保留。请导出草稿或载入服务器版本。")
			return
		}
		data, _ := json.Marshal(input.Document)
		if err = writePrivate(filepath.Join(s.manager.dir, "flow-canvas.json"), data); err != nil {
			fail(w, 500, "画布保存失败，当前草稿已保留")
			return
		}
		view, err := s.manager.readCanvas()
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		respond(w, 200, view)
	}))
}
