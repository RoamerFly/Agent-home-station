package admin

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// toolEffect records only a verified, scoped effect of a successful call.
// Tool output and command text are never copied into the activity journal.
func (p *permittedTools) toolEffect(name string, args json.RawMessage, result string) string {
	var input, output map[string]any
	_ = json.Unmarshal(args, &input)
	_ = json.Unmarshal([]byte(result), &output)
	path := ""
	verb := ""
	switch name {
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
