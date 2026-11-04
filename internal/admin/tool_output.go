package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"
	"unicode/utf8"
	"wechat-bot/internal/mcptools"
)

type retainedToolOutput struct {
	MemoryEpoch         string    `json:"memory_epoch,omitempty"`
	MemoryAuthorization string    `json:"memory_authorization,omitempty"`
	Created             time.Time `json:"created"`
	Tool                string    `json:"tool"`
	Result              string    `json:"result"`
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
	stored := retainedToolOutput{Created: time.Now().UTC(), Tool: name, Result: result}
	if err := p.validateServiceOutput(ctx, name, result); err != nil {
		return "", err
	}
	if memoryOutputTool(name) {
		// The query, not retention, owns the snapshot stamp. Never relabel an old
		// result with a newer epoch after a concurrent correction or forget.
		var envelope struct {
			Epoch         string `json:"_memory_epoch"`
			Authorization string `json:"_memory_authorization"`
		}
		if json.Unmarshal([]byte(result), &envelope) != nil || envelope.Epoch == "" {
			return "", toolInputError("memory_result_unversioned: 请重新查询记忆后读取分页")
		}
		epoch, err := p.manager.memory.DataEpoch(ctx)
		if err != nil {
			return "", err
		}
		if envelope.Epoch != epoch {
			return "", toolInputError("memory_result_invalidated: 记忆或来源已更新，请重新查询记忆")
		}
		if !p.MemoryResultAuthorized(envelope.Authorization) {
			return "", toolInputError("memory_authorization_changed: 记忆读取权限已变化，请重新查询")
		}
		stored.MemoryEpoch = envelope.Epoch
		stored.MemoryAuthorization = envelope.Authorization
	}
	data, _ := json.Marshal(stored)
	if err := writePrivate(filepath.Join(dir, id+".json"), data); err != nil {
		return "", err
	}
	if err := p.validateServiceOutput(ctx, name, result); err != nil {
		_ = os.Remove(filepath.Join(dir, id+".json"))
		return "", err
	}
	if memoryOutputTool(name) && !p.MemoryResultAuthorized(stored.MemoryAuthorization) {
		_ = os.Remove(filepath.Join(dir, id+".json"))
		return "", toolInputError("memory_authorization_changed: 保存期间记忆权限已变化，请重新查询")
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
	if err := p.validateServiceOutput(ctx, stored.Tool, stored.Result); err != nil {
		return "", err
	}
	if memoryOutputTool(stored.Tool) {
		if !p.MemoryResultAuthorized(stored.MemoryAuthorization) {
			return "", toolInputError("memory_authorization_changed: 原记忆读取权限已变化，不能继续读取旧结果")
		}
		epoch, e := p.manager.memory.DataEpoch(ctx)
		if e != nil || stored.MemoryEpoch == "" || stored.MemoryEpoch != epoch {
			return "", toolInputError("memory_result_invalidated: 记忆或来源已更新，请重新查询记忆；原执行任务未重做")
		}
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
		out, _ := json.Marshal(map[string]any{"source": "untrusted_tool_result", "tool": stored.Tool, "result_id": a.ID, "memory_epoch": stored.MemoryEpoch, "memory_authorization": stored.MemoryAuthorization, "offset": a.Offset, "next_offset": end, "complete": end == len(runes), "total_chars": len(runes), "text": string(runes[a.Offset:end])})
		return out
	}
	out := pack()
	// The runner's preview is byte-bounded. Paginate before that boundary so
	// next_offset never skips multibyte text that the model did not receive.
	for len(out) > 5400 && end > a.Offset+1 {
		end = a.Offset + (end-a.Offset)*3/4
		out = pack()
	}
	if memoryOutputTool(stored.Tool) && !p.MemoryResultAuthorized(stored.MemoryAuthorization) {
		return "", toolInputError("memory_authorization_changed: 读取期间记忆权限已变化")
	}
	if err := p.validateServiceOutput(ctx, stored.Tool, stored.Result); err != nil {
		return "", err
	}
	return string(out), nil
}

// Capture effective visibility before querying, rather than granting old data
// whatever privileges happen to be current when it is retained or paged.
func (p *permittedTools) memoryQueryAuthorization() string {
	if p == nil || p.manager == nil || p.manager.native == nil || p.manager.agent == nil {
		return ""
	}
	c := p.manager.Config()
	a := p.manager.agent.config()
	account := p.manager.native.Account()
	allowed := permittedRecipient(c.Config, p.trace.Thread)
	if p.trace.Preview || p.trace.ReportOnly || p.trace.Account != account || p.trace.Account == "" || p.trace.Thread == "" || p.trace.Sender == "" || !allowed || !c.Companion.Permissions.Remember {
		return ""
	}
	privileged := a.Enabled && !p.trace.Surf && contains(a.OwnerIDs, p.trace.Sender) && a.allowsPrivilegedThread(p.trace.Thread)
	b, _ := json.Marshal(struct {
		Version                                                                                  int
		Account, Channel, Thread, Requester                                                      string
		Privileged, CrossConversation, Remember, ObserveGroups, ReadConversations, Enabled, Surf bool
	}{1, account, p.trace.Channel, p.trace.Thread, p.trace.Sender, privileged, a.CrossConversation, c.Companion.Permissions.Remember, c.Companion.Permissions.ObserveGroups, a.ReadConversations, a.Enabled, p.trace.Surf})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// surfGenerationAuthorization binds autonomous browsing context to an actual
// configured proactive target without granting the no-sender worker access to
// memory_read/search. It is only used as generation evidence; the ordinary
// memory query stamp above remains byte-for-byte unchanged for user turns.
func (p *permittedTools) surfGenerationAuthorization() string {
	if p == nil || p.manager == nil || p.manager.native == nil || p.manager.agent == nil || !p.trace.Surf || p.privateSurfOrigin != nil || p.trace.Sender != "" {
		return ""
	}
	interest := normalizeSurfInterest(p.trace.SurfTopic)
	if interest == "" {
		return "" // Older queued turns never received cross-session excerpts.
	}
	// Use the same web grant as the original tool invocation. serviceAuthorization
	// reads the member query stamp directly, so a surf generation stamp can
	// never grant memory-tool access or change an existing web job's signature.
	grant := p.serviceAuthorization()
	if grant == "" {
		return ""
	}
	p.manager.mu.Lock()
	remember := p.manager.config.Companion.Permissions.Remember
	revision := p.manager.surf.SettingsRevision
	p.manager.mu.Unlock()
	if !remember {
		return ""
	}
	return agentID("surf-generation-v1", grant, strconv.Itoa(revision), interest)
}

// MemoryResultAuthorized is also used before reinjecting retained memory into
// a model continuation. A missing stamp is legacy data, never a fresh grant.
func (p *permittedTools) MemoryResultAuthorized(stamp string) bool {
	return stamp != "" && (stamp == p.memoryQueryAuthorization() || stamp == p.surfGenerationAuthorization())
}

// MemoryResultAuthorization lets the reply engine bind the same opaque,
// effective-visibility snapshot to its final delivery evidence. It does not
// relabel existing query results and is not exposed as a callable agent tool.
func (p *permittedTools) MemoryResultAuthorization() string {
	if stamp := p.memoryQueryAuthorization(); stamp != "" {
		return stamp
	}
	return p.surfGenerationAuthorization()
}

func memoryOutputTool(name string) bool {
	return name == "memory_read" || name == "memory_search" || name == "agent_global_search"
}

// stampMemoryQuery fails closed if any source changed while a read was being
// assembled. The stamp travels with previews and retained pages all the way to
// the next model invocation, where it is checked again.
func (p *permittedTools) stampMemoryQuery(ctx context.Context, epoch, authorization string, value any) (string, error) {
	if !p.MemoryResultAuthorized(authorization) {
		return "", toolInputError("memory_authorization_changed: 查询期间记忆权限已变化，请重新查询")
	}
	current, err := p.manager.memory.DataEpoch(ctx)
	if err != nil {
		return "", err
	}
	if epoch == "" || current != epoch {
		return "", toolInputError("memory_result_invalidated: 查询期间来源已更新，请从当前游标重新查询")
	}
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var envelope map[string]json.RawMessage
	if err = json.Unmarshal(b, &envelope); err != nil || envelope == nil {
		return "", toolInputError("memory_result_invalid: 查询结果缺少版本封套")
	}
	envelope["_memory_epoch"], _ = json.Marshal(epoch)
	envelope["_memory_authorization"], _ = json.Marshal(authorization)
	b, err = json.Marshal(envelope)
	if !p.MemoryResultAuthorized(authorization) {
		return "", toolInputError("memory_authorization_changed: 查询期间记忆权限已变化，请重新查询")
	}
	return string(b), err
}
