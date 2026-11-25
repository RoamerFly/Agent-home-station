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
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/workspacefs"
)

// The application owns this journal, its source check and its delivery.
// The workstation RVC backend sees only the WAV, request and opaque ID.
type stationWechatRVCJob struct {
	ID, Account, Thread, Sender, Trace, Fish, RequestHash string
	Source                                                companion.SourceRef
	MediaGrant                                            time.Time
	Submission                                            publicRVCSubmission
	State, WAVHash                                        string
	Bytes                                                 int64
	ResumePending                                         bool
}

func (j stationWechatRVCJob) expectedID() string {
	return localCheckpointKey("wechat-rvc", j.Account, j.Thread, j.Sender, j.Trace, j.Source, j.MediaGrant, j.RequestHash)
}

func (j stationWechatRVCJob) media() companion.GeneratedMedia {
	return companion.GeneratedMedia{ID: "reply-rvc-" + j.ID, AssetID: "rvc-" + j.ID, Kind: "file", Title: "大肥鱼换声.wav", CachedMIME: "audio/wav"}
}

func (t *stationAttachmentTools) publicRVCTools(ctx context.Context) []mcptools.Tool {
	voice, ok := t.publicRVCVoicesTool(ctx)
	if !ok {
		return nil
	}
	tools := []mcptools.Tool{voice}
	if t.rvcMu != nil && t.replyAllowed(t.trace) && t.store != nil {
		tools = append(tools, localRVCStartToolSpec(), mcptools.NewTool(localRVCJobTool,
			"查询、取消或续办微信应用原 RVC 任务；完成后取得已核验 WAV 文件。提交回执不明时只查询原任务，不重复转换。",
			`{"type":"object","properties":{"job_id":{"type":"string"},"action":{"type":"string","enum":["status","cancel","resume"]}},"required":["job_id","action"],"additionalProperties":false}`))
	}
	return tools
}

// RVC conversion needs an application-owned task and media delivery adapter.
// Voice discovery can already use the workstation's current registration.
func (t *stationAttachmentTools) publicRVCVoicesTool(ctx context.Context) (mcptools.Tool, bool) {
	if t.projects == nil || t.projects.rvc == nil || t.projectOwnerAllowed == nil ||
		!t.projectOwnerAllowed(t.trace) || !t.allowed() {
		return mcptools.Tool{}, false
	}
	record, err := t.projects.home.identity.Read()
	if err != nil || record.LocalOwner == nil {
		return mcptools.Tool{}, false
	}
	if _, err := t.projects.publicRVCVoices(ctx, record.FishID); err != nil {
		return mcptools.Tool{}, false
	}
	return localRVCVoicesToolSpec(), true
}

func (t *stationAttachmentTools) callPublicRVCVoices(ctx context.Context, raw json.RawMessage) (string, error) {
	if _, ok := t.publicRVCVoicesTool(ctx); !ok {
		return "", errors.New("工作站 RVC 声线当前不可用")
	}
	if strictLocalMemoryDecode(raw, &struct{}{}) != nil {
		return "", mcptools.ArgumentError("$", "声线查询不需要参数")
	}
	record, err := t.projects.home.identity.Read()
	if err != nil || record.LocalOwner == nil {
		return "", errors.New("工作站身份不可用")
	}
	voices, err := t.projects.publicRVCVoices(ctx, record.FishID)
	if err != nil || !t.projectOwnerAllowed(t.trace) || !t.allowed() {
		return "", errors.New("工作站 RVC 声线当前不可用")
	}
	return localProcessJSON(map[string]any{"voices": voices})
}

func (t *stationAttachmentTools) rvcJobAllowed(in companion.Input, j stationWechatRVCJob) bool {
	if t.projectOwnerAllowed == nil || !t.projectOwnerAllowed(in) || !t.replyAllowed(in) || t.store == nil ||
		j.ID != j.expectedID() || j.Account != in.Account || j.Thread != in.Thread || j.Sender != in.Sender ||
		!j.MediaGrant.Equal(t.replies[in.Thread]) {
		return false
	}
	original := companion.Input{Channel: "wechat", Account: j.Account, Thread: j.Thread, Sender: j.Sender, TraceID: j.Trace, Source: &j.Source}
	current, ok := t.store.SourceForInput(original)
	return ok && *current == j.Source
}

func (t *stationAttachmentTools) rvcJobRoot(id string) (*os.Root, error) {
	if !validLocalRVCHex(id, 64) {
		return nil, errors.New("RVC 应用任务编号无效")
	}
	return workspacefs.OpenPath(filepath.Join(t.dir, "rvc-jobs", id))
}

func (t *stationAttachmentTools) readRVCJob(id string) (stationWechatRVCJob, error) {
	var j stationWechatRVCJob
	root, err := t.rvcJobRoot(id)
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
		return stationWechatRVCJob{}, errors.New("RVC 应用任务记录无效")
	}
	return j, nil
}

func (t *stationAttachmentTools) writeRVCJob(j stationWechatRVCJob, wav []byte) error {
	root, err := workspacefs.EnsurePath(filepath.Join(t.dir, "rvc-jobs", j.ID))
	if err != nil {
		return err
	}
	defer root.Close()
	if len(wav) != 0 {
		output, err := root.OpenFile("output.wav", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if os.IsExist(err) {
			existing, openErr := workspacefs.OpenRegular(root, "output.wav")
			if openErr != nil {
				return openErr
			}
			hash := sha256.New()
			n, readErr := io.Copy(hash, io.LimitReader(existing, int64(len(wav))+1))
			readErr = errors.Join(readErr, existing.Close())
			if readErr != nil || n != int64(len(wav)) || hex.EncodeToString(hash.Sum(nil)) != j.WAVHash {
				return errors.New("RVC 已有成品与任务结果不一致")
			}
		} else if err != nil {
			return err
		} else {
			_, err = output.Write(wav)
			if err == nil {
				err = output.Sync()
			}
			if err = errors.Join(err, output.Close()); err != nil {
				_ = root.Remove("output.wav")
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

func (t *stationAttachmentTools) rvcJobView(j stationWechatRVCJob) string {
	view := map[string]any{"job_id": j.ID, "state": j.State}
	if j.State == "completed" {
		media := j.media()
		t.preparedSpeech(media)
		view["media"] = []companion.GeneratedMedia{media}
	}
	result, _ := localProcessJSON(view)
	return result
}

func (t *stationAttachmentTools) callPublicRVCConvert(ctx context.Context, raw json.RawMessage) (string, error) {
	if t.rvcMu == nil {
		return "", errors.New("微信 RVC 任务暂不可用")
	}
	t.rvcMu.Lock()
	defer t.rvcMu.Unlock()
	if len(t.publicRVCTools(ctx)) < 3 || len(raw) > 64<<10 || t.store == nil {
		return "", wechatapp.ErrAttachmentDenied
	}
	var in struct {
		Artifact string `json:"artifact_id"`
		VoiceID  string `json:"voice_profile_id"`
		Title    string `json:"title"`
	}
	if strictLocalMemoryDecode(raw, &in) != nil || !artifactID(in.Artifact, "work-") || in.VoiceID == "" {
		return "", mcptools.ArgumentError("$", "请选择已登记的 WAV 成品和 RVC 声线")
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
	j := stationWechatRVCJob{Account: t.trace.Account, Thread: t.trace.Thread, Sender: t.trace.Sender,
		Trace: t.trace.TraceID, Fish: record.FishID, Source: *source, MediaGrant: t.replies[t.trace.Thread],
		RequestHash: hex.EncodeToString(hash[:]), State: "submitting"}
	j.ID = j.expectedID()
	if previous, err := t.readRVCJob(j.ID); err == nil {
		if !t.rvcJobAllowed(t.trace, previous) {
			return "", wechatapp.ErrAttachmentDenied
		}
		if previous.State == "submitting" {
			previous.State = "needs_review"
		}
		return t.rvcJobView(previous), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	access := &localWorkspaceAccess{home: t.projects.home, fishID: record.FishID, ownerID: record.LocalOwner.ID}
	grant, err := access.resolve()
	if err != nil {
		return "", err
	}
	bound, err := t.projects.home.artifacts.bindProjectArtifact(ctx, in.Artifact, access, grant)
	if err != nil {
		return "", err
	}
	wav, evidence, err := readRVCReference(ctx, bound)
	if err != nil || evidence.seconds() > 600 {
		return "", errors.New("RVC 原件不可用或超过 600 秒")
	}
	if !t.rvcJobAllowed(t.trace, j) || bound.check(ctx) != nil {
		return "", wechatapp.ErrAttachmentDenied
	}
	if err := t.writeRVCJob(j, nil); err != nil {
		return "", err
	}
	j.Submission, err = t.projects.submitPublicRVC(ctx, j.Fish, j.ID, in.VoiceID, in.Title, bound.artifact.SHA256, wav)
	if err != nil {
		j.State = "needs_review"
	} else {
		j.State = "queued"
	}
	if saveErr := t.writeRVCJob(j, nil); saveErr != nil {
		return "", saveErr
	}
	if !t.rvcJobAllowed(t.trace, j) {
		return "", wechatapp.ErrAttachmentDenied
	}
	return t.rvcJobView(j), nil
}

func (t *stationAttachmentTools) callPublicRVCJob(ctx context.Context, raw json.RawMessage) (string, error) {
	if t.rvcMu == nil {
		return "", errors.New("微信 RVC 任务暂不可用")
	}
	t.rvcMu.Lock()
	defer t.rvcMu.Unlock()
	var in struct {
		ID     string `json:"job_id"`
		Action string `json:"action"`
	}
	if strictLocalMemoryDecode(raw, &in) != nil || !validLocalRVCHex(in.ID, 64) || (in.Action != "status" && in.Action != "cancel" && in.Action != "resume") {
		return "", mcptools.ArgumentError("$", "请提供 RVC 任务编号和 status、cancel 或 resume")
	}
	j, err := t.readRVCJob(in.ID)
	if err != nil || !t.rvcJobAllowed(t.trace, j) || len(t.publicRVCTools(ctx)) < 3 {
		return "", wechatapp.ErrAttachmentDenied
	}
	if j.State == "completed" || j.State == "cancelled" || j.State == "failed" && in.Action != "resume" {
		return t.rvcJobView(j), nil
	}
	if j.Submission.Binding == "" {
		j.State = "needs_review"
	} else if in.Action == "resume" {
		if j.Submission.RemoteID == "" || j.ResumePending || j.State != "failed" && j.State != "needs_review" {
			j.State = "needs_review"
		} else {
			j.ResumePending = true
			j.State = "resuming"
			if err := t.writeRVCJob(j, nil); err != nil {
				return "", err
			}
			state, err := t.projects.resumePublicRVC(ctx, j.Fish, j.ID, j.Submission)
			if err != nil {
				j.State = "needs_review"
			} else {
				j.State = state
				if state == "completed" {
					j.State = "needs_review"
				}
				j.ResumePending = false
			}
		}
	} else if in.Action == "cancel" {
		state, err := t.projects.cancelPublicRVC(ctx, j.Fish, j.ID, j.Submission)
		if err != nil {
			return "", err
		}
		j.State = state
		if state == "completed" {
			// A completion may race with cancellation. Fetch and verify its WAV
			// through status before advertising any deliverable media.
			j.State = "needs_review"
		}
	} else {
		state, product, err := t.projects.publicRVCStatus(ctx, j.Fish, j.ID, j.Submission)
		if err != nil {
			return "", err
		}
		j.State = state
		if j.Submission.RemoteID == "" && validLocalRVCHex(product.RemoteID, 32) {
			j.Submission.RemoteID = product.RemoteID
		}
		if state != "failed" && state != "needs_review" {
			j.ResumePending = false
		}
		if state == "completed" {
			if !t.rvcJobAllowed(t.trace, j) {
				return "", wechatapp.ErrAttachmentDenied
			}
			j.WAVHash, j.Bytes = product.SHA256, int64(len(product.WAV))
			if err := t.writeRVCJob(j, product.WAV); err != nil {
				return "", err
			}
			return t.rvcJobView(j), nil
		}
	}
	if err := t.writeRVCJob(j, nil); err != nil {
		return "", err
	}
	return t.rvcJobView(j), nil
}

func (t *stationAttachmentTools) rvcMediaAllowed(in companion.Input, asset string) (stationWechatRVCJob, bool) {
	if len(asset) != 68 || asset[:4] != "rvc-" {
		return stationWechatRVCJob{}, false
	}
	j, err := t.readRVCJob(asset[4:])
	return j, err == nil && j.State == "completed" && j.WAVHash != "" && j.Bytes > 0 && t.rvcJobAllowed(in, j)
}

func (t *stationAttachmentTools) stageRVC(ctx context.Context, in companion.Input, asset string) (string, error) {
	j, ok := t.rvcMediaAllowed(in, asset)
	if !ok {
		return "", wechatapp.ErrAttachmentDenied
	}
	root, err := t.rvcJobRoot(j.ID)
	if err != nil {
		return "", err
	}
	defer root.Close()
	file, err := workspacefs.OpenRegular(root, "output.wav")
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, j.Bytes+1))
	if err != nil || n != j.Bytes || hex.EncodeToString(hash.Sum(nil)) != j.WAVHash {
		return "", errors.New("RVC 成品内容已变化")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	upload, err := wechatapp.StageMediaUpload(ctx, t.native, wechatapp.MediaUpload{
		Account: in.Account, Filename: j.media().Title, MIME: "audio/wav", Bytes: j.Bytes, SHA256: j.WAVHash,
	}, file, func() bool { _, allowed := t.rvcMediaAllowed(in, asset); return allowed })
	if err != nil {
		return "", err
	}
	if _, ok := t.rvcMediaAllowed(in, asset); !ok {
		return "", wechatapp.ErrAttachmentDenied
	}
	return upload, nil
}
