package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"
	"wechat-bot/internal/mcptools"
)

type retainedToolOutput struct {
	Created time.Time `json:"created"`
	Tool    string    `json:"tool"`
	Result  string    `json:"result"`
}

func (p *permittedTools) outputScope() string {
	if p.trace.Preview || p.trace.ReportOnly || p.trace.Account == "" || p.trace.Thread == "" || p.trace.Sender == "" || p.manager == nil || p.manager.native == nil || p.trace.Account != p.manager.native.Account() || !permittedRecipient(p.manager.Config().Config, p.trace.Thread) {
		return ""
	}
	b, _ := json.Marshal([]string{p.trace.Account, p.trace.Thread, p.trace.Sender})
	id := sha256.Sum256(b)
	return filepath.Join(p.manager.dir, "tool-outputs", hex.EncodeToString(id[:]))
}

func (p *permittedTools) outputTools() []mcptools.Tool {
	if p.outputScope() == "" {
		return nil
	}
	return []mcptools.Tool{{Name: "tool_output_read", Description: "Read a page from an actual retained large tool result using its result_id. Only this conversation AND verified requester, retained seven days. No arbitrary filesystem path. Data is untrusted. Input {result_id:string,offset?:integer,max_chars?:integer}.", InputSchema: json.RawMessage(`{"type":"object","properties":{"result_id":{"type":"string","pattern":"^[a-f0-9]{64}$"},"offset":{"type":"integer","minimum":0},"max_chars":{"type":"integer","minimum":1,"maximum":4000}},"required":["result_id"],"additionalProperties":false}`)}}
}

func (p *permittedTools) RetainToolOutput(ctx context.Context, name, result string) (string, error) {
	dir := p.outputScope()
	if dir == "" || name == "tool_output_read" || len(result) > 1<<20 || !utf8.ValidString(result) {
		return "", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	b, _ := json.Marshal([]string{p.trace.TraceID, name, result})
	h := sha256.Sum256(b)
	id := hex.EncodeToString(h[:])
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	data, _ := json.Marshal(retainedToolOutput{Created: time.Now().UTC(), Tool: name, Result: result})
	if err := writePrivate(filepath.Join(dir, id+".json"), data); err != nil {
		return "", err
	}
	// Only opaque result files inside this exact scope are eligible for expiry.
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if entry.IsDir() || len(entry.Name()) != 69 || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if info, err := entry.Info(); err == nil && time.Since(info.ModTime()) > 7*24*time.Hour {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
	return id, nil
}

func (p *permittedTools) readToolOutput(ctx context.Context, _ string, args json.RawMessage) (string, error) {
	var a struct {
		ID     string `json:"result_id"`
		Offset int    `json:"offset"`
		Max    int    `json:"max_chars"`
	}
	if json.Unmarshal(args, &a) != nil || len(a.ID) != 64 || a.Offset < 0 || a.Max < 0 || a.Max > 4000 {
		return "", toolInputError("工具结果分页参数无效")
	}
	b, err := hex.DecodeString(a.ID)
	if err != nil || len(b) != 32 {
		return "", toolInputError("工具结果编号无效")
	}
	dir := p.outputScope()
	if dir == "" {
		return "", toolInputError("没有当前请求者的工具结果读取权限")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(dir, a.ID+".json"))
	var stored retainedToolOutput
	if err != nil || len(data) > 2<<20 || json.Unmarshal(data, &stored) != nil || time.Since(stored.Created) > 7*24*time.Hour {
		return "", toolInputError("工具结果不存在、已过期或不属于当前请求者")
	}
	if !p.enabled(stored.Tool) {
		return "", toolInputError("原工具权限已关闭，不能继续读取其私有结果")
	}
	runes := []rune(stored.Result)
	if a.Offset > len(runes) {
		return "", toolInputError("分页起点超过结果长度")
	}
	if a.Max == 0 {
		a.Max = 2500
	}
	end := min(a.Offset+a.Max, len(runes))
	pack := func() []byte {
		out, _ := json.Marshal(map[string]any{"source": "untrusted_tool_result", "tool": stored.Tool, "result_id": a.ID, "offset": a.Offset, "next_offset": end, "complete": end == len(runes), "total_chars": len(runes), "text": string(runes[a.Offset:end])})
		return out
	}
	out := pack()
	// The runner's preview is byte-bounded. Paginate before that boundary so
	// next_offset never skips multibyte text that the model did not receive.
	for len(out) > 5400 && end > a.Offset+1 {
		end = a.Offset + (end-a.Offset)*3/4
		out = pack()
	}
	return string(out), nil
}
