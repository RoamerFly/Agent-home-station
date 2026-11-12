package wechatui

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Canvas schemas and validation retain the 2026-11-04 editor contract. These
// documents describe a diagram; they never grant a tool or change the bot.
type CanvasNode struct {
	ID    string  `json:"id"`
	Kind  string  `json:"kind"`
	Title string  `json:"title"`
	Note  string  `json:"note"`
	Tone  string  `json:"tone"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
}
type CanvasEdge struct {
	ID    string `json:"id"`
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
}
type CanvasDocument struct {
	Version int          `json:"version"`
	Name    string       `json:"name"`
	Nodes   []CanvasNode `json:"nodes"`
	Edges   []CanvasEdge `json:"edges"`
}
type CanvasView struct {
	Document CanvasDocument `json:"document"`
	Revision string         `json:"revision"`
	Saved    bool           `json:"saved"`
}

var canvasID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func DefaultCanvas() ([]byte, error) { return assets.ReadFile("static/flow-default.json") }
func canvasText(value string, limit int) bool {
	return utf8.ValidString(value) && len(value) <= limit && !strings.ContainsRune(value, 0)
}
func ValidateCanvas(doc CanvasDocument) error {
	if doc.Version != 1 || strings.TrimSpace(doc.Name) == "" || !canvasText(doc.Name, 120) || doc.Nodes == nil || doc.Edges == nil {
		return errors.New("请选择有效的画布名称与版本")
	}
	if len(doc.Nodes) > 200 || len(doc.Edges) > 600 {
		return errors.New("单画布最多容纳200个节点和600条连线，请拆分设计")
	}
	data, err := DefaultCanvas()
	var original CanvasDocument
	if err != nil || json.Unmarshal(data, &original) != nil {
		return errors.New("默认流程不可用")
	}
	kinds := map[string]bool{"note": true, "condition": true, "action": true}
	for _, n := range original.Nodes {
		kinds[n.Kind] = true
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
