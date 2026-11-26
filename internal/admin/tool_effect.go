package admin

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// Keep only small, explicitly useful scalar parameters. Full arguments may
// contain message text, commands or credentials and must stay out of logs.
func toolArgsSummary(raw json.RawMessage) string {
	var input map[string]any
	if json.Unmarshal(raw, &input) != nil {
		return ""
	}
	if nested, ok := input["arguments"].(map[string]any); ok {
		for key, value := range nested {
			if _, exists := input[key]; !exists {
				input[key] = value
			}
		}
	}
	keys := []string{"plugin", "capability", "action", "path", "output_path", "model", "model_name", "width", "height", "steps", "seed", "format", "limit", "query"}
	parts := make([]string, 0, 6)
	for _, key := range keys {
		value, ok := input[key]
		if !ok {
			continue
		}
		var text string
		switch value.(type) {
		case string, float64, bool:
			text = fmt.Sprint(value)
		default:
			continue
		}
		text = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(text)
		if runes := []rune(text); len(runes) > 100 {
			text = string(runes[:100]) + "…"
		}
		if text != "" {
			parts = append(parts, key+"="+text)
		}
		if len(parts) == 6 {
			break
		}
	}
	return strings.Join(parts, " · ")
}

// toolEffect records only a verified, scoped effect of a successful call.
// Tool output and command text are never copied into the activity journal.
func (p *permittedTools) toolEffect(name string, args json.RawMessage, result string) string {
	var input, output map[string]any
	_ = json.Unmarshal(args, &input)
	_ = json.Unmarshal([]byte(result), &output)
	path := ""
	verb := ""
	switch name {
	case "music_local_start", "music_rvc_start":
		if id, ok := output["job_id"].(string); ok && validID(id) {
			if output["existing_job"] == true {
				return "复用歌曲任务：job_id=" + id
			}
			return "已提交歌曲任务：job_id=" + id
		}
	case "server_write":
		path, _ = input["path"].(string)
		verb = "已创建文件"
	case "server_edit":
		path, _ = input["path"].(string)
		verb = "已修改文件"
	case "speech_synthesize":
		path, _ = output["output_path"].(string)
		verb = "已生成音频"
	case "workspace_write":
		if document, ok := input["name"].(string); ok && document != "" {
			return "已保存工作区笔记：" + document
		}
	}
	if path == "" || verb == "" || !filepath.IsAbs(path) {
		return ""
	}
	x := p.executionContext()
	for _, root := range []struct{ label, path string }{
		{"会话根目录", x.Conversation}, {"公共根目录", x.Public}, {"账号根目录", x.Private},
	} {
		rel, err := filepath.Rel(root.path, path)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return verb + "：" + root.label + "/" + filepath.ToSlash(rel)
		}
	}
	return ""
}
