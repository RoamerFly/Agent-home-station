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
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/comfyui"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/stationyue"
	"wechat-bot/internal/workspacefs"
)

// This record belongs to the WeChat application. The YuE tool receives only
// stationyue.Request, an optional WAV and a caller-assigned job ID.
type stationWechatYuEJob struct {
	ID, Account, Thread, Sender, Trace, Fish, RequestHash string
	Source                                                companion.SourceRef
	MediaGrant                                            time.Time
	Binding                                               string
	Submission                                            localYuESubmission
	State, FLACHash                                       string
	Bytes                                                 int64
}

func (j stationWechatYuEJob) expectedID() string {
	return localCheckpointKey("wechat-yue", j.Account, j.Thread, j.Sender, j.Trace, j.Source, j.MediaGrant, j.RequestHash)
}

func (j stationWechatYuEJob) media() companion.GeneratedMedia {
	return companion.GeneratedMedia{ID: "reply-yue-" + j.ID, AssetID: "yue-" + j.ID, Kind: "file", Title: "大肥鱼作品.flac", CachedMIME: "audio/flac"}
}

func (t *stationAttachmentTools) publicYuETools(ctx context.Context) []mcptools.Tool {
	if t.speech == nil || t.speech.yue == nil || t.speechOwnerAllowed == nil || !t.speechOwnerAllowed(t.trace) || !t.replyAllowed(t.trace) {
		return nil
	}
	record, err := t.speech.home.identity.Read()
	if err != nil || record.LocalOwner == nil {
		return nil
	}
	host, err := t.speech.forFish(record.FishID)
	if err != nil {
		return nil
	}
	if _, err = installedLocalProject(ctx, host, localProjectOperation{pkg: localYuEPackage, capability: localYuECapability}); err != nil {
		return nil
	}
	if info, err := t.speech.yue.Info(ctx, record.FishID); err != nil || info.Binding == "" {
		return nil
	}
	caps, err := host.store.Capabilities(ctx, localYuEPackage)
	if err != nil {
		return nil
	}
	var tools []mcptools.Tool
	for _, cap := range caps {
		if !cap.Discoverable {
			continue
		}
		switch cap.ID {
		case localYuECapability:
			tools = append(tools, mcptools.Tool{Name: localYuEStartTool, Description: cap.Description, InputSchema: cap.InputSchema})
		case "yue.job":
			tools = append(tools, mcptools.Tool{Name: localYuEJobTool, Description: cap.Description, InputSchema: cap.InputSchema})
		}
	}
	return tools
}

func (t *stationAttachmentTools) yueJobAllowed(in companion.Input, j stationWechatYuEJob) bool {
	if t.speechOwnerAllowed == nil || !t.speechOwnerAllowed(in) || !t.replyAllowed(in) ||
		j.ID != j.expectedID() || j.Account != in.Account || j.Thread != in.Thread || j.Sender != in.Sender ||
		!j.MediaGrant.Equal(t.replies[in.Thread]) || t.store == nil {
		return false
	}
	original := companion.Input{Channel: "wechat", Account: j.Account, Thread: j.Thread, Sender: j.Sender, TraceID: j.Trace, Source: &j.Source}
	current, ok := t.store.SourceForInput(original)
	return ok && *current == j.Source
}

func (t *stationAttachmentTools) yueRoot(id string) (*os.Root, error) {
	if !validLocalRVCHex(id, 64) {
		return nil, errors.New("YuE 任务编号无效")
	}
	return workspacefs.OpenPath(filepath.Join(t.dir, "yue-jobs", id))
}

func (t *stationAttachmentTools) readYuEJob(id string) (stationWechatYuEJob, error) {
	var j stationWechatYuEJob
	root, err := t.yueRoot(id)
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
		return stationWechatYuEJob{}, errors.New("YuE 应用任务记录无效")
	}
	return j, nil
}

func (t *stationAttachmentTools) writeYuEJob(j stationWechatYuEJob, flac []byte) error {
	root, err := workspacefs.EnsurePath(filepath.Join(t.dir, "yue-jobs", j.ID))
	if err != nil {
		return err
	}
	defer root.Close()
	if len(flac) != 0 {
		output, err := root.OpenFile("output.flac", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if os.IsExist(err) {
			existing, openErr := workspacefs.OpenRegular(root, "output.flac")
			if openErr != nil {
				return openErr
			}
			hash := sha256.New()
			n, readErr := io.Copy(hash, io.LimitReader(existing, int64(len(flac))+1))
			readErr = errors.Join(readErr, existing.Close())
			if readErr != nil || n != int64(len(flac)) || hex.EncodeToString(hash.Sum(nil)) != j.FLACHash {
				return errors.New("YuE 已有成品与任务结果不一致")
			}
		} else if err != nil {
			return err
		} else {
			_, err = output.Write(flac)
			if err == nil {
				err = output.Sync()
			}
			if err = errors.Join(err, output.Close()); err != nil {
				_ = root.Remove("output.flac")
				return err
			}
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

func (t *stationAttachmentTools) callPublicYuEGenerate(ctx context.Context, raw json.RawMessage) (string, error) {
	if t.yueMu == nil {
		return "", errors.New("微信 YuE 任务暂不可用")
	}
	t.yueMu.Lock()
	defer t.yueMu.Unlock()
	if len(t.publicYuETools(ctx)) == 0 || t.store == nil || len(raw) > 64<<10 {
		return "", wechatapp.ErrAttachmentDenied
	}
	var in localYuEGenerateInput
	if localComfyStrictDecode(raw, &in) != nil || in.Style == nil || in.Lyrics == nil || in.Duration <= 0 ||
		(in.Mode != stationyue.ModeOriginal && in.Mode != stationyue.ModeCover) || (in.Mode == stationyue.ModeCover) != (in.Reference != "") {
		return "", mcptools.ArgumentError("$", "请提供有效的 YuE 原创或翻唱配方")
	}
	source, ok := t.store.SourceForInput(t.trace)
	if !ok {
		return "", errors.New("微信原消息来源已变化")
	}
	record, err := t.speech.home.identity.Read()
	if err != nil || record.LocalOwner == nil {
		return "", errors.New("工作站身份不可用")
	}
	hash := sha256.Sum256(raw)
	j := stationWechatYuEJob{Account: t.trace.Account, Thread: t.trace.Thread, Sender: t.trace.Sender,
		Trace: t.trace.TraceID, Fish: record.FishID, Source: *source, MediaGrant: t.replies[t.trace.Thread], RequestHash: hex.EncodeToString(hash[:]), State: "submitting"}
	j.ID = j.expectedID()
	if previous, err := t.readYuEJob(j.ID); err == nil {
		if !t.yueJobAllowed(t.trace, previous) {
			return "", wechatapp.ErrAttachmentDenied
		}
		if previous.State == "submitting" {
			previous.State = "needs_review"
		}
		return t.yueJobView(previous), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	request := stationyue.Request{Mode: in.Mode, Style: *in.Style, Lyrics: *in.Lyrics, DurationSeconds: in.Duration,
		MinDurationSeconds: in.Minimum, TextGuidanceScale: in.Guidance, VocalGender: in.Gender,
		PlanningMode: in.Planning, ReferenceMode: in.RefMode, Sampling: in.Sampling}
	seed, _ := strconv.ParseUint(j.ID[:8], 16, 32)
	request.Seed = int64(seed & 0x7fffffff)
	if in.Seed != nil {
		request.Seed = *in.Seed
	}
	var reference []byte
	if in.Reference != "" {
		access := &localWorkspaceAccess{home: t.speech.home, fishID: record.FishID, ownerID: record.LocalOwner.ID}
		grant, err := access.resolve()
		if err != nil {
			return "", err
		}
		bound, err := t.speech.home.artifacts.bindProjectArtifact(ctx, in.Reference, access, grant)
		if err != nil {
			return "", err
		}
		reference, _, err = readRVCReference(ctx, bound)
		if err != nil {
			return "", err
		}
	}
	if err := t.writeYuEJob(j, nil); err != nil {
		return "", err
	}
	deploy, submission, submitErr := t.speech.submitPublicYuE(ctx, j.Fish, j.ID, request, reference)
	j.Binding, j.Submission = deploy.Binding, submission
	switch {
	case submitErr == nil:
		j.State = "queued"
	case errors.Is(submitErr, comfyui.ErrSubmissionUnknown):
		j.State = "needs_review"
	default:
		j.State = "failed"
	}
	if err := t.writeYuEJob(j, nil); err != nil {
		return "", err
	}
	if !t.yueJobAllowed(t.trace, j) {
		return "", wechatapp.ErrAttachmentDenied
	}
	return t.yueJobView(j), nil
}

func (t *stationAttachmentTools) yueJobView(j stationWechatYuEJob) string {
	view := map[string]any{"job_id": j.ID, "state": j.State}
	if j.State == "completed" {
		media := j.media()
		t.preparedSpeech(media)
		view["media"] = []companion.GeneratedMedia{media}
	}
	result, _ := localProcessJSON(view)
	return result
}

func (t *stationAttachmentTools) callPublicYuEJob(ctx context.Context, raw json.RawMessage) (string, error) {
	if t.yueMu == nil {
		return "", errors.New("微信 YuE 任务暂不可用")
	}
	t.yueMu.Lock()
	defer t.yueMu.Unlock()
	var in struct {
		ID     string `json:"job_id"`
		Action string `json:"action"`
	}
	if strictLocalMemoryDecode(raw, &in) != nil || !validLocalRVCHex(in.ID, 64) || (in.Action != "status" && in.Action != "cancel") {
		return "", mcptools.ArgumentError("$", "请提供 YuE 任务编号和 status 或 cancel")
	}
	j, err := t.readYuEJob(in.ID)
	if err != nil || !t.yueJobAllowed(t.trace, j) || len(t.publicYuETools(ctx)) == 0 {
		return "", wechatapp.ErrAttachmentDenied
	}
	if j.State == "completed" || j.State == "failed" || j.State == "cancelled" {
		return t.yueJobView(j), nil
	}
	if in.Action == "cancel" {
		if err := t.speech.cancelPublicYuE(ctx, j.Fish, j.Binding, j.ID); err != nil {
			return "", err
		}
		j.State = "cancelled"
	} else if j.Binding != "" && j.Submission.GraphSHA256 != "" {
		status, product, err := t.speech.publicYuEStatus(ctx, j.Fish, j.Binding, j.ID, j.Submission)
		if err != nil {
			return "", err
		}
		switch status.State {
		case comfyui.Completed:
			if !t.yueJobAllowed(t.trace, j) {
				return "", wechatapp.ErrAttachmentDenied
			}
			j.State, j.FLACHash, j.Bytes = "completed", product.Evidence.SHA256, product.Evidence.Bytes
			if err := t.writeYuEJob(j, product.FLAC.Data); err != nil {
				return "", err
			}
			return t.yueJobView(j), nil
		case comfyui.Failed, comfyui.Cancelled:
			j.State = string(status.State)
		case comfyui.Unknown:
			j.State = "needs_review"
		}
	} else {
		// The application journal was written before the backend call. A crash
		// during dispatch must never turn a status query into a second submit.
		j.State = "needs_review"
	}
	if err := t.writeYuEJob(j, nil); err != nil {
		return "", err
	}
	return t.yueJobView(j), nil
}

func (t *stationAttachmentTools) yueMediaAllowed(in companion.Input, asset string) (stationWechatYuEJob, bool) {
	if len(asset) != 68 || asset[:4] != "yue-" {
		return stationWechatYuEJob{}, false
	}
	j, err := t.readYuEJob(asset[4:])
	return j, err == nil && j.State == "completed" && j.FLACHash != "" && j.Bytes > 0 && t.yueJobAllowed(in, j)
}

func (t *stationAttachmentTools) stageYuE(ctx context.Context, in companion.Input, asset string) (string, error) {
	j, ok := t.yueMediaAllowed(in, asset)
	if !ok {
		return "", wechatapp.ErrAttachmentDenied
	}
	root, err := t.yueRoot(j.ID)
	if err != nil {
		return "", err
	}
	defer root.Close()
	file, err := workspacefs.OpenRegular(root, "output.flac")
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, j.Bytes+1))
	if err != nil || n != j.Bytes || hex.EncodeToString(hash.Sum(nil)) != j.FLACHash {
		return "", errors.New("YuE 成品内容已变化")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	upload, err := wechatapp.StageMediaUpload(ctx, t.native, wechatapp.MediaUpload{
		Account: in.Account, Filename: j.media().Title, MIME: "audio/flac", Bytes: j.Bytes, SHA256: j.FLACHash,
	}, file, func() bool { _, allowed := t.yueMediaAllowed(in, asset); return allowed })
	if err != nil {
		return "", err
	}
	if _, ok := t.yueMediaAllowed(in, asset); !ok {
		return "", wechatapp.ErrAttachmentDenied
	}
	return upload, nil
}
