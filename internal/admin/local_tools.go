package admin

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

type localToolSet struct {
	memory       companion.ToolAccess
	files        companion.ToolAccess
	process      companion.ToolAccess
	processError error
	access       *localWorkspaceAccess
	python       string
}

// Local owner actions already pass the actual tool's grant checks. Reuse the
// runner's existing autonomous loop instead of a zero/default tool-call quota.
func (*localToolSet) AutonomousActions() bool { return true }

func (h *localChatService) tools(store *companion.Store, in companion.Input) companion.ToolAccess {
	access := &localWorkspaceAccess{home: h.home}
	h.mu.Lock()
	var err error
	if h.processes == nil && !h.closed {
		h.processes, err = newLocalProcessTools(access, h.home.dataDir, h.windows)
	}
	process := h.processes
	python := ""
	if h.windows != nil {
		python = filepath.Join(filepath.Dir(h.windows.Pythonw), "python.exe")
	}
	h.mu.Unlock()
	set := &localToolSet{memory: newLocalMemoryTools(store, in), files: newLocalFileTools(access), access: access, processError: err, python: python}
	if process != nil {
		set.process = process
	}
	return set
}

func (t *localToolSet) List(ctx context.Context) ([]mcptools.Tool, error) {
	all := []mcptools.Tool{}
	var failures error
	for _, access := range []companion.ToolAccess{t.memory, t.files, t.process} {
		if access == nil {
			continue
		}
		tools, err := access.List(ctx)
		all = append(all, tools...)
		failures = errors.Join(failures, err)
	}
	return all, errors.Join(failures, t.processError)
}

func (t *localToolSet) Call(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	if strings.HasPrefix(name, "memory_") {
		return t.memory.Call(ctx, name, raw)
	}
	if name == "server_windows_process" {
		if t.process == nil {
			return "", errors.New("本机进程记录不可用，程序未启动；请保留记录并检查私有目录")
		}
		return t.process.Call(ctx, name, raw)
	}
	result, err := t.files.Call(ctx, name, raw)
	if err == nil && name == "server_status" {
		var state map[string]any
		if json.Unmarshal([]byte(result), &state) == nil {
			state["windows_hidden_executor"] = t.python != "" && t.processError == nil
			state["execution_tool"] = "server_windows_process"
			data, _ := json.Marshal(state)
			result = string(data)
		}
	}
	return result, err
}

func (t *localToolSet) AgentContext(ctx context.Context) string {
	if ctx.Err() != nil {
		return ""
	}
	grant, err := t.access.resolve()
	if err != nil {
		return `{"kind":"local_workspace","authorized":false,"note":"家目录操作已暂停或尚未授权；文字聊天和本机对话读取仍可用。"}`
	}
	data, _ := json.Marshal(map[string]any{"kind": "local_workspace", "authorized": true, "home": grant.Directory, "python_executable": t.python, "tool_directory": "Project Tools", "note": "家目录内可保存、修改、读取自己的项目与工具。先按需发现准确工具schema；只有实际进程结果能证明程序已运行成功。指定Python仅在运行器已配置时可用；无应用接入，不代表具有平台外发能力。"})
	return string(data)
}
