package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/comfyui"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/workspacefs"
)

// The application journal owns message provenance and delivery. The shared
// ComfyUI operation receives only its request and this opaque job ID.
type stationWechatComfyJob struct {
	ID, Account, Thread, Sender, Trace, Fish, RequestHash string
	Source                                                companion.SourceRef
	MediaGrant                                            time.Time
	Submission                                            publicComfySubmission
	State                                                 string
	Products                                              []stationWechatComfyProduct
}

type stationWechatComfyProduct struct {
	Name, Kind, MIME, SHA256 string
	Bytes                    int64
}

func (j stationWechatComfyJob) expectedID() string {
	return localCheckpointKey("wechat-comfy", j.Account, j.Thread, j.Sender, j.Trace, j.Source, j.MediaGrant, j.RequestHash)
}

func (t *stationAttachmentTools) comfyJobAllowed(in companion.Input, j stationWechatComfyJob) bool {
	if t.projectOwnerAllowed == nil || !t.projectOwnerAllowed(in) || !t.replyAllowed(in) || t.store == nil ||
		j.ID != j.expectedID() || j.Account != in.Account || j.Thread != in.Thread || j.Sender != in.Sender ||
		!j.MediaGrant.Equal(t.replies[in.Thread]) {
		return false
	}
	original := companion.Input{Channel: "wechat", Account: j.Account, Thread: j.Thread, Sender: j.Sender, TraceID: j.Trace, Source: &j.Source}
	current, ok := t.store.SourceForInput(original)
	return ok && *current == j.Source
}

func (t *stationAttachmentTools) comfyJobRoot(id string) (*os.Root, error) {
	if !validLocalRVCHex(id, 64) {
		return nil, errors.New("ComfyUI 任务编号无效")
	}
	return workspacefs.OpenPath(filepath.Join(t.dir, "comfy-jobs", id))
}

func (t *stationAttachmentTools) readComfyJob(id string) (stationWechatComfyJob, error) {
	var j stationWechatComfyJob
	root, err := t.comfyJobRoot(id)
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
	raw, err := io.ReadAll(io.LimitReader(file, 32<<10))
	if err != nil || len(raw) >= 32<<10 || json.Unmarshal(raw, &j) != nil || j.ID != id || j.expectedID() != id {
		return stationWechatComfyJob{}, errors.New("ComfyUI 应用任务记录无效")
	}
	return j, nil
}

func (t *stationAttachmentTools) writeComfyJob(j stationWechatComfyJob) error {
	root, err := workspacefs.EnsurePath(filepath.Join(t.dir, "comfy-jobs", j.ID))
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

func (t *stationAttachmentTools) comfyJobView(j stationWechatComfyJob) string {
	view := map[string]any{"job_id": j.ID, "state": j.State}
	if j.State == "completed" {
		media := make([]companion.GeneratedMedia, 0, len(j.Products))
		for i := range j.Products {
			item := j.media(i)
			t.preparedSpeech(item)
			media = append(media, item)
		}
		view["media"] = media
	}
	result, _ := localProcessJSON(view)
	return result
}

func (j stationWechatComfyJob) media(index int) companion.GeneratedMedia {
	asset := "comfy-" + j.ID + "-" + fmtComfyIndex(index)
	return companion.GeneratedMedia{ID: "reply-" + asset, AssetID: asset, Kind: j.Products[index].Kind,
		Title: j.Products[index].Name, CachedMIME: j.Products[index].MIME}
}

func fmtComfyIndex(index int) string {
	if index < 10 {
		return "0" + strconv.Itoa(index)
	}
	return strconv.Itoa(index)
}

func (t *stationAttachmentTools) comfyMediaAllowed(in companion.Input, asset string) (stationWechatComfyJob, int, bool) {
	var empty stationWechatComfyJob
	value, ok := strings.CutPrefix(asset, "comfy-")
	if !ok || len(value) != 67 || value[64] != '-' || !validLocalRVCHex(value[:64], 64) {
		return empty, 0, false
	}
	index, err := strconv.Atoi(value[65:])
	if err != nil {
		return empty, 0, false
	}
	j, err := t.readComfyJob(value[:64])
	return j, index, err == nil && j.State == "completed" && index >= 0 && index < len(j.Products) && t.comfyJobAllowed(in, j)
}

func (t *stationAttachmentTools) callPublicComfyRun(ctx context.Context, raw json.RawMessage) (string, error) {
	if t.comfyMu == nil {
		return "", errors.New("微信 ComfyUI 任务暂不可用")
	}
	t.comfyMu.Lock()
	defer t.comfyMu.Unlock()
	if t.store == nil || !t.replyAllowed(t.trace) || len(raw) > 80<<10 || !t.comfyRunAvailable(ctx) {
		return "", wechatapp.ErrAttachmentDenied
	}
	var in struct {
		Workflow string                     `json:"workflow_id"`
		Inputs   map[string]json.RawMessage `json:"inputs"`
		Title    string                     `json:"title"`
	}
	if strictLocalMemoryDecode(raw, &in) != nil || in.Workflow == "" || !safeRVCDisplay(strings.TrimSpace(in.Title), 100) && in.Title != "" {
		return "", mcptools.ArgumentError("$", "请提供有效的工作流编号、参数和标题")
	}
	source, ok := t.store.SourceForInput(t.trace)
	if !ok {
		return "", errors.New("微信原消息来源已变化")
	}
	record, err := t.projects.home.identity.Read()
	if err != nil || record.LocalOwner == nil {
		return "", errors.New("工作站身份不可用")
	}
	hash := sha256.Sum256(raw)
	j := stationWechatComfyJob{Account: t.trace.Account, Thread: t.trace.Thread, Sender: t.trace.Sender,
		Trace: t.trace.TraceID, Fish: record.FishID, Source: *source, MediaGrant: t.replies[t.trace.Thread],
		RequestHash: hex.EncodeToString(hash[:]), State: "submitting"}
	j.ID = j.expectedID()
	if previous, err := t.readComfyJob(j.ID); err == nil {
		if !t.comfyJobAllowed(t.trace, previous) {
			return "", wechatapp.ErrAttachmentDenied
		}
		if previous.State == "submitting" {
			previous.State = "needs_review"
		}
		return t.comfyJobView(previous), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := t.writeComfyJob(j); err != nil {
		return "", err
	}
	submission, submitErr := t.projects.submitPublicComfy(ctx, j.Fish, j.ID, in.Workflow, in.Inputs)
	j.Submission = submission
	switch {
	case submitErr == nil:
		j.State = "queued"
	case errors.Is(submitErr, comfyui.ErrSubmissionUnknown):
		j.State = "needs_review"
	default:
		j.State = "failed"
	}
	if err := t.writeComfyJob(j); err != nil {
		return "", err
	}
	if !t.comfyJobAllowed(t.trace, j) {
		return "", wechatapp.ErrAttachmentDenied
	}
	return t.comfyJobView(j), nil
}

func (t *stationAttachmentTools) comfyRunAvailable(ctx context.Context) bool {
	for _, tool := range t.publicComfyTools(ctx) {
		if tool.Name == localComfyStartTool {
			return true
		}
	}
	return false
}

func (t *stationAttachmentTools) comfyJobAvailable(ctx context.Context) bool {
	for _, tool := range t.publicComfyTools(ctx) {
		if tool.Name == localComfyJobTool {
			return true
		}
	}
	return false
}

func (t *stationAttachmentTools) callPublicComfyJob(ctx context.Context, raw json.RawMessage) (string, error) {
	if t.comfyMu == nil {
		return "", errors.New("微信 ComfyUI 任务暂不可用")
	}
	t.comfyMu.Lock()
	defer t.comfyMu.Unlock()
	var in struct {
		ID     string `json:"job_id"`
		Action string `json:"action"`
	}
	if strictLocalMemoryDecode(raw, &in) != nil || !validLocalRVCHex(in.ID, 64) || in.Action != "status" && in.Action != "cancel" {
		return "", mcptools.ArgumentError("$", "请提供 ComfyUI 任务编号和 status 或 cancel")
	}
	j, err := t.readComfyJob(in.ID)
	if err != nil || !t.comfyJobAllowed(t.trace, j) || !t.comfyJobAvailable(ctx) {
		return "", wechatapp.ErrAttachmentDenied
	}
	if j.State == "completed" || j.State == "failed" || j.State == "cancelled" {
		return t.comfyJobView(j), nil
	}
	if in.Action == "cancel" {
		if err := t.projects.cancelPublicComfy(ctx, j.Fish, j.ID, j.Submission); err != nil {
			return "", err
		}
		j.State = "cancelled"
	} else if j.Submission.Binding != "" {
		state, product, err := t.projects.publicComfyStatus(ctx, j.Fish, j.ID, j.Submission)
		if err != nil {
			return "", err
		}
		switch state {
		case comfyui.Completed:
			if !t.comfyJobAllowed(t.trace, j) {
				return "", wechatapp.ErrAttachmentDenied
			}
			if err := t.saveComfyProducts(ctx, &j, product); err != nil {
				return "", err
			}
			j.State = "completed"
		case comfyui.Failed, comfyui.Cancelled:
			j.State = string(state)
		case comfyui.Unknown:
			j.State = "needs_review"
		default:
			j.State = string(state)
		}
	} else {
		j.State = "needs_review"
	}
	if err := t.writeComfyJob(j); err != nil {
		return "", err
	}
	if j.State == "completed" {
		_ = t.projects.settlePublicComfy(ctx, j.Fish, j.ID, j.Submission)
	}
	return t.comfyJobView(j), nil
}
