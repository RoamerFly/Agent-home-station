package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"wechat-bot/internal/companion"
)

const localProcessTool = "server_windows_process"

// Hash only typed, normalized arguments. No command or file content is copied
// into the journal. The key is also usable as a Jobs lookup key after a crash.
func localCheckpointKey(values ...any) string {
	data, _ := json.Marshal(values)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (t *localToolSet) writeCheckpointed(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	var in localWorkspaceFileInput
	if err := strictLocalMemoryDecode(raw, &in); err != nil {
		return "", errors.New("本机家目录文件参数无效")
	}
	grant, err := t.access.resolve()
	if err != nil {
		return "", err
	}
	in.Path, err = localWorkspacePath(grant, in.Path)
	if err != nil {
		return "", err
	}
	target, _ := filepath.Rel(grant.Directory, in.Path)
	// Ignore fields that do not affect the write/edit.
	var key string
	if name == "server_write" {
		key = localCheckpointKey(name, in.Path, in.Content)
	} else {
		key = localCheckpointKey(name, in.Path, in.Expected, in.Old, in.New)
	}
	op := companion.LocalToolCheckpoint{Key: key, Tool: name, Grant: localCheckpointKey(grant), Target: target}
	return t.dispatchCheckpointed(ctx, op, grant, func() (string, string, error) {
		result, err := t.files.call(ctx, name, in, grant)
		return result, result, err
	})
}

func (t *localToolSet) startCheckpointed(ctx context.Context, in localProcessInput) (string, error) {
	if in.Path == "" || len(in.Argv) == 0 {
		return "", errors.New("启动程序需要path和argv")
	}
	grant, err := t.access.resolve()
	if err != nil {
		return "", err
	}
	scope := localProcessScope(grant.FishID, grant.OwnerID)
	op := companion.LocalToolCheckpoint{
		Key:  localCheckpointKey(localProcessTool, scope, t.traceID, in.Path, in.Argv, in.Seconds),
		Tool: localProcessTool, Grant: scope,
	}
	in.Key = "local-" + op.Key
	return t.dispatchCheckpointed(ctx, op, grant, func() (string, string, error) {
		result, err := t.process.start(ctx, in, grant)
		if err != nil {
			return "", "", err
		}
		var receipt struct{ ID string }
		if err := json.Unmarshal([]byte(result), &receipt); err != nil {
			return "", "", err
		}
		// The registry already owns the process ID, state and output. Only a
		// known not-started response is kept in the request journal.
		if receipt.ID != "" {
			return result, "registered", nil
		}
		return result, result, nil
	})
}

func (t *localToolSet) dispatchCheckpointed(ctx context.Context, op companion.LocalToolCheckpoint, grant localWorkspaceGrant, call func() (result, checkpoint string, err error)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	for _, previous := range t.run.Operations {
		if previous.Key == op.Key {
			if err := t.run.CheckSource(ctx); err != nil {
				return "", errors.New("原请求来源已变化，不能返回工具结果")
			}
			return localProcessJSON(t.checkpointView(previous, grant))
		}
	}
	if t.inspectOnly {
		return "", errors.New("续办只能核对已有操作或取消原进程；新增写入和启动需要主人发起新请求")
	}
	if err := t.run.Reserve(ctx, op); err != nil {
		return "", errors.New("操作检查点未能保存，未执行")
	}
	t.run.Operations = append(t.run.Operations, op)
	result, checkpoint, err := call()
	if err != nil {
		t.inspectOnly = true
		return "", errors.Join(err, errors.New("已登记操作，但结果未核实。请先读取文件或查原进程，勿重做"))
	}
	// A tool may have completed while the model request was being cancelled.
	// Give its outcome a bounded chance to commit before reporting the result.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := t.run.RecordResult(saveCtx, op.Key, checkpoint); err != nil {
		t.inspectOnly = true
		return "", errors.New("操作已登记，但结果未能保存。请先核对，勿重做")
	}
	t.run.Operations[len(t.run.Operations)-1].Result = checkpoint
	return result, nil
}

func (t *localToolSet) checkpointView(op companion.LocalToolCheckpoint, grant localWorkspaceGrant) map[string]any {
	view := map[string]any{"tool": op.Tool, "state": "needs_review", "recovered": true}
	if op.Tool == localProcessTool {
		if t.process == nil {
			return view
		}
		scope, err := t.process.ownerScope()
		if err != nil || scope != op.Grant {
			return view
		}
		if p, ok := t.process.jobs.FindByKey(scope, "local-"+op.Key); ok {
			view["id"], view["state"], view["reason"] = p.ID, p.State, p.Reason
			return view
		}
		if op.Result != "" && op.Result != "registered" {
			view["state"], view["previous_result"] = "not_started", json.RawMessage(op.Result)
		}
		return view
	}
	if grant.Directory == "" || op.Grant != localCheckpointKey(grant) {
		view["state"] = "grant_changed"
		return view
	}
	if op.Tool == localRVCStartTool || op.Tool == localRVCJobTool {
		return t.rvcCheckpointView(op, grant)
	}
	if projectOperation(op.Tool).capability != "" {
		return t.projectCheckpointView(op, grant)
	}
	view["path"] = op.Target
	if op.Result != "" {
		view["state"], view["previous_result"] = "recorded", json.RawMessage(op.Result)
	}
	return view
}
