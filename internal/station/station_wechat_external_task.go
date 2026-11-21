package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"os"
	"path/filepath"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/plugins"
	"wechat-bot/internal/strictjson"
	"wechat-bot/internal/workspacefs"
)

type stationWechatExternalTaskJob struct {
	ID, Account, Thread, Sender, Trace, Fish string
	Source                                   companion.SourceRef
	Grant, MediaGrant                        time.Time
	Registration, RequestHash, Tool          string
	Arguments                                json.RawMessage
	Ticket                                   plugins.JobTicket
	Initial                                  plugins.ExternalResponse
	Started, Updated                         time.Time
	Interval, AttemptLimit, Deadline         int
	Attempts                                 int
	CanCancel, IsJob                         bool
	State, Pending, Text                     string
	SHA256, MIME, DeclaredKind               string
	Bytes                                    int64
}

func (j stationWechatExternalTaskJob) expectedID() string {
	return localCheckpointKey("wechat-external-task", j.Account, j.Thread, j.Sender, j.Trace, j.Source, j.RequestHash)
}

func (j stationWechatExternalTaskJob) media() companion.GeneratedMedia {
	return companion.GeneratedMedia{ID: "reply-external-task-" + j.ID, AssetID: "external-task-" + j.ID,
		Kind: "file", Title: "工作站外部任务结果.bin", CachedMIME: j.MIME}
}

func (t *stationAttachmentTools) externalTaskAllowed(in companion.Input, j stationWechatExternalTaskJob) bool {
	if t.projectOwnerAllowed == nil || !t.projectOwnerAllowed(in) || !t.scopeAllowed(in, t.grants) || t.store == nil ||
		j.ID != j.expectedID() || j.Account != in.Account || j.Thread != in.Thread || j.Sender != in.Sender ||
		j.Trace != in.TraceID || !j.Grant.Equal(t.grants[in.Thread]) {
		return false
	}
	current, ok := t.store.SourceForInput(in)
	return ok && *current == j.Source
}

func (t *stationAttachmentTools) externalTaskRoot(id string) (*os.Root, error) {
	if !validLocalRVCHex(id, 64) {
		return nil, errors.New("外部任务编号无效")
	}
	return workspacefs.OpenPath(filepath.Join(t.dir, "external-task-jobs", id))
}

func (t *stationAttachmentTools) readExternalTaskJob(id string) (stationWechatExternalTaskJob, error) {
	var j stationWechatExternalTaskJob
	root, err := t.externalTaskRoot(id)
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
	raw, err := io.ReadAll(io.LimitReader(file, 16<<20))
	if err != nil || len(raw) >= 16<<20 || json.Unmarshal(raw, &j) != nil || j.ID != id || j.ID != j.expectedID() ||
		!validLocalRVCHex(j.Registration, 64) || !validLocalRVCHex(j.Ticket.Fingerprint, 64) {
		return stationWechatExternalTaskJob{}, errors.New("外部任务记录无效")
	}
	return j, nil
}

func (t *stationAttachmentTools) writeExternalTaskJob(j stationWechatExternalTaskJob, binary []byte) error {
	root, err := workspacefs.EnsurePath(filepath.Join(t.dir, "external-task-jobs", j.ID))
	if err != nil {
		return err
	}
	defer root.Close()
	if len(binary) != 0 {
		if err := writeComfyProduct(root, "output.bin", binary, j.SHA256); err != nil {
			return err
		}
	}
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

func (t *stationAttachmentTools) externalTaskView(j stationWechatExternalTaskJob, offset, limit int, reused bool) (string, error) {
	state := j.State
	if j.Pending != "" {
		state = "needs_review"
	}
	view := map[string]any{"tool": j.Tool, "task_id": j.ID, "state": state, "reused": reused,
		"attempts": j.Attempts, "attempt_limit": j.AttemptLimit}
	switch state {
	case "completed":
		if j.Bytes != 0 {
			if !t.replyAllowed(t.trace) || !j.MediaGrant.Equal(t.replies[t.trace.Thread]) {
				return "", wechatapp.ErrAttachmentDenied
			}
			media := j.media()
			t.preparedSpeech(media)
			view["media"] = []companion.GeneratedMedia{media}
			view["declared_kind"], view["delivery_kind"] = j.DeclaredKind, "file"
		} else {
			raw, _ := json.Marshal(map[string]string{"text": j.Text})
			page, err := localExternalTextPage(raw, offset, limit)
			if err != nil {
				return "", err
			}
			view["output"] = page
		}
		view["untrusted_external_content"] = true
	case "waiting":
		view["next_check_at"] = minTime(j.Updated.Add(time.Duration(j.Interval)*time.Second), j.Started.Add(time.Duration(j.Deadline)*time.Second))
	case "media_pending":
		view["instruction"] = "外部任务返回了媒体 URL，尚未完成安全领取，不能作为可投递成品。"
		if j.Text != "" {
			raw, _ := json.Marshal(map[string]string{"text": j.Text})
			page, err := localExternalTextPage(raw, offset, limit)
			if err != nil {
				return "", err
			}
			view["output"], view["untrusted_external_content"] = page, true
		}
	case "needs_review":
		view["instruction"] = "外部任务阶段回执不明；请核对原服务，不要重新提交。"
	case "cancelled":
		view["cancel_scope"] = "local_polling"
	case "cancel_acknowledged":
		view["cancel_scope"] = "remote_request"
	}
	return localProcessJSON(view)
}

func (t *stationAttachmentTools) callPublicExternalTask(ctx context.Context, raw json.RawMessage) (string, error) {
	if t.externalMu == nil {
		return "", errors.New("微信外部任务暂不可用")
	}
	t.externalMu.Lock()
	defer t.externalMu.Unlock()
	if len(raw) > 64<<10 || t.store == nil || t.projects == nil || t.dir == "" {
		return "", wechatapp.ErrAttachmentDenied
	}
	var input localExternalTaskArgs
	if _, err := strictjson.Decode(raw); err != nil || strictjson.DecodeKnownFields(raw, &input) != nil || input.Offset < 0 || input.Limit < 0 {
		return "", mcptools.ArgumentError("$", "外部任务参数或分页范围无效")
	}
	if input.Limit == 0 {
		input.Limit = 1200
	}
	if t.projectOwnerAllowed == nil || !t.projectOwnerAllowed(t.trace) || !t.allowed() {
		return "", wechatapp.ErrAttachmentDenied
	}
	owner, err := t.projects.home.identity.Read()
	if err != nil || owner.LocalOwner == nil {
		return "", errors.New("工作站身份不可用")
	}
	registration, err := t.projects.externalRegistration()
	if err != nil {
		return "", errors.New("工作站外部任务登记不可读")
	}
	if input.Action == "start" {
		return t.startPublicExternalTask(ctx, input, owner.FishID, externalDigest(registration))
	}
	if input.Action != "status" && input.Action != "cancel" || !validLocalRVCHex(input.ID, 64) || input.Tool != "" || len(input.Args) != 0 ||
		input.Action == "cancel" && input.Offset != 0 {
		return "", mcptools.ArgumentError("$", "查询或取消请使用原任务编号")
	}
	j, err := t.readExternalTaskJob(input.ID)
	if err != nil || !t.externalTaskAllowed(t.trace, j) || j.Fish != owner.FishID {
		return "", wechatapp.ErrAttachmentDenied
	}
	def, err := t.projects.publicExternalTaskDefinition(ctx, owner.FishID, j.Tool)
	if err != nil || j.Registration != externalDigest(registration) || def.Protocol.Tool.Fingerprint != j.Ticket.Fingerprint {
		return localProcessJSON(map[string]any{"tool": j.Tool, "task_id": j.ID, "state": "needs_review",
			"instruction": "工作站登记或授权已变化，请核对原任务。"})
	}
	if j.Pending != "" || j.State != "waiting" || input.Action == "status" && input.Offset != 0 {
		return t.externalTaskView(j, input.Offset, input.Limit, true)
	}
	if input.Action == "cancel" {
		if !j.CanCancel || !plugins.ValidExternalRemoteID(j.Ticket.RemoteID) {
			j.State, j.Updated = "cancelled", time.Now().UTC()
			if err := t.writeExternalTaskJob(j, nil); err != nil {
				return "", err
			}
			return t.externalTaskView(j, 0, input.Limit, false)
		}
		return t.runPublicExternalTaskPhase(ctx, &j, "cancel", input.Limit)
	}
	now := time.Now().UTC()
	if j.Attempts >= j.AttemptLimit || !now.Before(j.Started.Add(time.Duration(j.Deadline)*time.Second)) {
		j.State = "polling_limit_reached"
		if err := t.writeExternalTaskJob(j, nil); err != nil {
			return "", err
		}
		return t.externalTaskView(j, 0, input.Limit, false)
	}
	if now.Before(j.Updated.Add(time.Duration(j.Interval) * time.Second)) {
		return t.externalTaskView(j, 0, input.Limit, true)
	}
	j.Attempts++
	phase := "query"
	if j.IsJob {
		phase = "poll"
	}
	return t.runPublicExternalTaskPhase(ctx, &j, phase, input.Limit)
}

func (t *stationAttachmentTools) startPublicExternalTask(ctx context.Context, input localExternalTaskArgs, fish, registration string) (string, error) {
	if input.ID != "" || input.Offset != 0 {
		return "", mcptools.ArgumentError("$", "提交只接受工具名和参数")
	}
	value, err := strictjson.Decode(input.Args)
	object, ok := value.(map[string]any)
	if err != nil || !ok {
		return "", mcptools.ArgumentError("$.arguments", "请提供任务参数对象")
	}
	arguments, err := json.Marshal(object)
	if err != nil {
		return "", err
	}
	if len(arguments) > 8<<10 {
		return "", mcptools.ArgumentError("$.arguments", "外部任务参数超过协议的 8 KiB 限制")
	}
	def, err := t.projects.publicExternalTaskDefinition(ctx, fish, input.Tool)
	if err != nil {
		return "", mcptools.ArgumentError("$.tool", "该异步工具未在工作站启用")
	}
	if def.Protocol.Tool.MediaKind != "" && !t.replyAllowed(t.trace) {
		return "", wechatapp.ErrAttachmentDenied
	}
	source, ok := t.store.SourceForInput(t.trace)
	if !ok {
		return "", errors.New("微信原消息来源已变化")
	}
	now := time.Now().UTC()
	j := stationWechatExternalTaskJob{Account: t.trace.Account, Thread: t.trace.Thread, Sender: t.trace.Sender,
		Trace: t.trace.TraceID, Fish: fish, Source: *source, Grant: t.grants[t.trace.Thread], MediaGrant: t.replies[t.trace.Thread],
		Registration: registration, RequestHash: localCheckpointKey(input.Tool, arguments), Tool: input.Tool, Arguments: arguments,
		Ticket:  plugins.JobTicket{Tool: input.Tool, Fingerprint: def.Protocol.Tool.Fingerprint, State: "submitting"},
		Started: now, Updated: now, Interval: def.Interval, AttemptLimit: def.AttemptMax, Deadline: def.Deadline,
		CanCancel: def.CanCancel, IsJob: def.Protocol.Tool.Job != nil, State: "submitting"}
	j.ID = j.expectedID()
	if previous, err := t.readExternalTaskJob(j.ID); err == nil {
		if !t.externalTaskAllowed(t.trace, previous) || previous.Fish != fish {
			return "", wechatapp.ErrAttachmentDenied
		}
		if previous.Registration != registration {
			return localProcessJSON(map[string]any{"tool": input.Tool, "task_id": previous.ID, "state": "needs_review",
				"instruction": "工作站登记已变化，请核对原请求，不要再次提交。"})
		}
		return t.externalTaskView(previous, 0, input.Limit, true)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if !t.externalTaskAllowed(t.trace, j) {
		return "", wechatapp.ErrAttachmentDenied
	}
	return t.runPublicExternalTaskPhase(ctx, &j, "start", input.Limit)
}

func (t *stationAttachmentTools) runPublicExternalTaskPhase(ctx context.Context, j *stationWechatExternalTaskJob, phase string, limit int) (string, error) {
	j.Pending = phase
	j.Updated = time.Now().UTC()
	if err := t.writeExternalTaskJob(*j, nil); err != nil {
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
				if !t.externalTaskAllowed(t.trace, *j) {
					cancel()
					return
				}
			}
		}
	}()
	result, callErr := t.projects.publicExternalTaskPhase(bound, j.Fish, j.Registration, j.Tool, phase, j.Arguments, j.Ticket, j.Initial)
	cancel()
	<-done
	j.Pending = ""
	j.Updated = time.Now().UTC()
	var binary []byte
	if callErr != nil {
		j.State = "needs_review"
	} else if result.CancelAck {
		j.State = "cancel_acknowledged"
	} else {
		j.Ticket = result.Ticket
		j.Text = result.Text
		j.State = result.Ticket.State
		if result.MediaPending {
			j.State = "media_pending"
		}
		if result.Initial.BodyBase64 != "" {
			j.Initial = result.Initial
		}
		if len(result.Binary) != 0 {
			binary = result.Binary
			j.Bytes, j.SHA256, j.DeclaredKind = int64(len(binary)), externalDigest(binary), result.DeclaredKind
			j.MIME, _, _ = mime.ParseMediaType(result.MIME)
			if j.MIME == "" {
				j.MIME = "application/octet-stream"
			}
		}
	}
	if err := t.writeExternalTaskJob(*j, binary); err != nil {
		return "", err
	}
	if !t.externalTaskAllowed(t.trace, *j) {
		return "", wechatapp.ErrAttachmentDenied
	}
	return t.externalTaskView(*j, 0, limit, false)
}

func (t *stationAttachmentTools) externalTaskMediaAllowed(in companion.Input, asset string) (stationWechatExternalTaskJob, bool) {
	var empty stationWechatExternalTaskJob
	const prefix = "external-task-"
	if len(asset) != len(prefix)+64 || asset[:len(prefix)] != prefix || !validLocalRVCHex(asset[len(prefix):], 64) {
		return empty, false
	}
	j, err := t.readExternalTaskJob(asset[len(prefix):])
	return j, err == nil && j.State == "completed" && j.Bytes > 0 && j.SHA256 != "" &&
		j.MediaGrant.Equal(t.replies[in.Thread]) && t.replyAllowed(in) && t.externalTaskAllowed(in, j)
}

func (t *stationAttachmentTools) stageExternalTask(ctx context.Context, in companion.Input, asset string) (string, error) {
	j, ok := t.externalTaskMediaAllowed(in, asset)
	if !ok {
		return "", wechatapp.ErrAttachmentDenied
	}
	root, err := t.externalTaskRoot(j.ID)
	if err != nil {
		return "", err
	}
	defer root.Close()
	file, err := workspacefs.OpenRegular(root, "output.bin")
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, j.Bytes+1))
	if err != nil || n != j.Bytes || hex.EncodeToString(hash.Sum(nil)) != j.SHA256 {
		return "", errors.New("外部任务成品内容已变化")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	upload, err := wechatapp.StageMediaUpload(ctx, t.native, wechatapp.MediaUpload{
		Account: in.Account, Filename: j.media().Title, MIME: j.MIME, Bytes: j.Bytes, SHA256: j.SHA256,
	}, file, func() bool { _, allowed := t.externalTaskMediaAllowed(in, asset); return allowed })
	if err != nil {
		return "", err
	}
	if _, ok := t.externalTaskMediaAllowed(in, asset); !ok {
		return "", wechatapp.ErrAttachmentDenied
	}
	return upload, nil
}
