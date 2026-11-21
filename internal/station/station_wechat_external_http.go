package station

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/plugins"
	"wechat-bot/internal/strictjson"
	"wechat-bot/internal/workspacefs"
)

// The WeChat application owns source binding and the durable submit-once
// journal. The workstation backend receives no WeChat account or message.
type stationWechatExternalHTTPJob struct {
	ID, Account, Thread, Sender, Trace, Fish string
	Source                                   companion.SourceRef
	Grant                                    time.Time
	Registration, RequestHash, Tool          string
	State, Text                              string
}

func (j stationWechatExternalHTTPJob) expectedID() string {
	return localCheckpointKey("wechat-external-http", j.Account, j.Thread, j.Sender, j.Trace, j.Source, j.Grant, j.Registration, j.RequestHash)
}

func (t *stationAttachmentTools) externalHTTPAllowed(in companion.Input, j stationWechatExternalHTTPJob) bool {
	if t.projectOwnerAllowed == nil || !t.projectOwnerAllowed(in) || !t.allowed() || t.store == nil ||
		j.ID != j.expectedID() || j.Account != in.Account || j.Thread != in.Thread || j.Sender != in.Sender ||
		j.Trace != in.TraceID || !j.Grant.Equal(t.grants[in.Thread]) {
		return false
	}
	current, ok := t.store.SourceForInput(in)
	return ok && *current == j.Source
}

func (t *stationAttachmentTools) externalHTTPRoot(id string) (*os.Root, error) {
	if !validLocalRVCHex(id, 64) {
		return nil, errors.New("外部工具应用任务编号无效")
	}
	return workspacefs.OpenPath(filepath.Join(t.dir, "external-http-jobs", id))
}

func (t *stationAttachmentTools) readExternalHTTPJob(id string) (stationWechatExternalHTTPJob, error) {
	var j stationWechatExternalHTTPJob
	root, err := t.externalHTTPRoot(id)
	if err != nil {
		return j, err
	}
	defer root.Close()
	file, err := workspacefs.OpenRegular(root, "record.json")
	if os.IsNotExist(err) {
		file, err = workspacefs.OpenRegular(root, "record.previous.json")
	}
	if err != nil {
		return j, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 8<<20))
	if err != nil || len(raw) >= 8<<20 || json.Unmarshal(raw, &j) != nil || j.ID != id || j.expectedID() != id {
		return stationWechatExternalHTTPJob{}, errors.New("外部工具应用任务记录无效")
	}
	return j, nil
}

func (t *stationAttachmentTools) writeExternalHTTPJob(j stationWechatExternalHTTPJob) error {
	root, err := workspacefs.EnsurePath(filepath.Join(t.dir, "external-http-jobs", j.ID))
	if err != nil {
		return err
	}
	defer root.Close()
	raw, err := json.Marshal(j)
	if err != nil {
		return err
	}
	name := ".record-" + identity.RandomToken()
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(raw)
	if err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		_ = root.Remove(name)
		return err
	}
	if err = root.Rename(name, "record.json"); os.IsExist(err) {
		_ = root.Remove("record.previous.json")
		if err = root.Rename("record.json", "record.previous.json"); err == nil {
			err = root.Rename(name, "record.json")
			if err != nil {
				_ = root.Rename("record.previous.json", "record.json")
			}
		}
	}
	if err == nil {
		_ = root.Remove("record.previous.json")
	} else {
		_ = root.Remove(name)
	}
	return err
}

func (t *stationAttachmentTools) externalHTTPView(j stationWechatExternalHTTPJob, offset, limit int, reused bool) (string, error) {
	view := map[string]any{"tool": j.Tool, "task_id": j.ID, "state": j.State, "reused": reused}
	if j.State == "completed" {
		raw, _ := json.Marshal(map[string]string{"text": j.Text})
		page, err := localExternalTextPage(raw, offset, limit)
		if err != nil {
			return "", err
		}
		view["output"] = page
		view["untrusted_external_content"] = true
	} else {
		view["instruction"] = "请求结果尚未确认；请核对原服务，不要重新提交同一操作。"
	}
	return localProcessJSON(view)
}

func (t *stationAttachmentTools) callPublicExternalHTTP(ctx context.Context, raw json.RawMessage) (string, error) {
	if t.externalMu == nil {
		return "", errors.New("微信外部工具任务暂不可用")
	}
	t.externalMu.Lock()
	defer t.externalMu.Unlock()
	if len(raw) > 64<<10 || t.store == nil || t.projects == nil || t.dir == "" {
		return "", wechatapp.ErrAttachmentDenied
	}
	var input struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
		Offset    int             `json:"text_offset"`
		Limit     int             `json:"text_limit"`
	}
	if _, err := strictjson.Decode(raw); err != nil || strictLocalMemoryDecode(raw, &input) != nil ||
		input.Offset < 0 || input.Limit < 0 {
		return "", mcptools.ArgumentError("$", "请提供目录中的同步工具名及参数对象")
	}
	if input.Limit == 0 {
		input.Limit = 1200
	}
	args, err := strictjson.Decode(input.Arguments)
	object, ok := args.(map[string]any)
	if err != nil || !ok {
		return "", mcptools.ArgumentError("$.arguments", "请提供 JSON 参数对象")
	}
	arguments, err := json.Marshal(object)
	if err != nil {
		return "", err
	}
	if t.projectOwnerAllowed == nil || !t.projectOwnerAllowed(t.trace) || !t.allowed() {
		return "", wechatapp.ErrAttachmentDenied
	}
	owner, err := t.projects.home.identity.Read()
	if err != nil || owner.LocalOwner == nil {
		return "", errors.New("工作站身份不可用")
	}
	tools, _, _, err := t.projects.publicExternalCatalog(ctx, owner.FishID)
	if err != nil {
		return "", err
	}
	index := slices.IndexFunc(tools, func(tool plugins.ExternalProtocolTool) bool { return tool.Name == input.Tool })
	if index < 0 {
		return "", mcptools.ArgumentError("$.tool", "该工具未在工作站启用")
	}
	if tools[index].TextPath == "" || tools[index].MediaURLPath != "" || tools[index].MediaKind != "" {
		return "", errors.New("该工具包含媒体结果，当前微信适配器仅支持文本结果")
	}
	registration, err := t.projects.externalRegistration()
	if err != nil {
		return "", errors.New("工作站外部工具登记不可读")
	}
	source, ok := t.store.SourceForInput(t.trace)
	if !ok {
		return "", errors.New("微信原消息来源已变化")
	}
	j := stationWechatExternalHTTPJob{Account: t.trace.Account, Thread: t.trace.Thread, Sender: t.trace.Sender,
		Trace: t.trace.TraceID, Fish: owner.FishID, Source: *source, Grant: t.grants[t.trace.Thread],
		Registration: externalDigest(registration), Tool: input.Tool,
		RequestHash: localCheckpointKey(input.Tool, arguments), State: "submitting"}
	j.ID = j.expectedID()
	if previous, err := t.readExternalHTTPJob(j.ID); err == nil {
		if !t.externalHTTPAllowed(t.trace, previous) || previous.Tool != input.Tool || previous.Fish != owner.FishID {
			return "", wechatapp.ErrAttachmentDenied
		}
		if previous.State == "submitting" {
			previous.State = "needs_review"
		}
		return t.externalHTTPView(previous, input.Offset, input.Limit, true)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if input.Offset != 0 {
		return "", mcptools.ArgumentError("$.text_offset", "首次提交请从正文起点读取")
	}
	if !t.externalHTTPAllowed(t.trace, j) {
		return "", wechatapp.ErrAttachmentDenied
	}
	if err := t.writeExternalHTTPJob(j); err != nil {
		return "", err
	}
	bound, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-bound.Done():
				return
			case <-ticker.C:
				if !t.externalHTTPAllowed(t.trace, j) {
					cancel()
					return
				}
			}
		}
	}()
	text, callErr := t.projects.publicExternalHTTP(bound, owner.FishID, j.Registration, input.Tool, arguments)
	cancel()
	<-done
	if callErr != nil {
		j.State = "needs_review"
	} else {
		j.State, j.Text = "completed", text
	}
	if err := t.writeExternalHTTPJob(j); err != nil {
		return "", err
	}
	if !t.externalHTTPAllowed(t.trace, j) {
		return "", wechatapp.ErrAttachmentDenied
	}
	return t.externalHTTPView(j, 0, input.Limit, false)
}
