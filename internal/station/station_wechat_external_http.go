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
	Grant, MediaGrant                        time.Time
	Registration, RequestHash, Tool          string
	State, Text                              string
	SHA256, MIME, DeclaredKind               string
	Bytes                                    int64
}

func (j stationWechatExternalHTTPJob) expectedID() string {
	return localCheckpointKey("wechat-external-http", j.Account, j.Thread, j.Sender, j.Trace, j.Source, j.RequestHash)
}

// Records written by the first text-only adapter included the grant and
// registration in their ID. Continue to read them without resubmitting.
func (j stationWechatExternalHTTPJob) previousID() string {
	return localCheckpointKey("wechat-external-http", j.Account, j.Thread, j.Sender, j.Trace, j.Source, j.Grant, j.Registration, j.RequestHash)
}

func (j stationWechatExternalHTTPJob) validID() bool {
	return j.ID == j.expectedID() || j.ID == j.previousID()
}

func (j stationWechatExternalHTTPJob) media() companion.GeneratedMedia {
	extension := ".bin"
	switch j.MIME {
	case "image/png":
		extension = ".png"
	case "image/jpeg":
		extension = ".jpg"
	case "video/mp4":
		extension = ".mp4"
	case "audio/mpeg":
		extension = ".mp3"
	case "audio/wav", "audio/x-wav":
		extension = ".wav"
	case "application/pdf":
		extension = ".pdf"
	}
	return companion.GeneratedMedia{ID: "reply-external-" + j.ID, AssetID: "external-" + j.ID,
		Kind: "file", Title: "工作站外部结果" + extension, CachedMIME: j.MIME}
}

func (t *stationAttachmentTools) externalHTTPAllowed(in companion.Input, j stationWechatExternalHTTPJob) bool {
	if t.projectOwnerAllowed == nil || !t.projectOwnerAllowed(in) || !t.scopeAllowed(in, t.grants) || t.store == nil ||
		!j.validID() || j.Account != in.Account || j.Thread != in.Thread || j.Sender != in.Sender ||
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
	raw, err := io.ReadAll(io.LimitReader(file, 16<<20))
	if err != nil || len(raw) >= 16<<20 || json.Unmarshal(raw, &j) != nil || j.ID != id || !j.validID() {
		return stationWechatExternalHTTPJob{}, errors.New("外部工具应用任务记录无效")
	}
	return j, nil
}

func (t *stationAttachmentTools) writeExternalHTTPJob(j stationWechatExternalHTTPJob, binary []byte) error {
	root, err := workspacefs.EnsurePath(filepath.Join(t.dir, "external-http-jobs", j.ID))
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

func (t *stationAttachmentTools) externalHTTPView(j stationWechatExternalHTTPJob, offset, limit int, reused bool) (string, error) {
	view := map[string]any{"tool": j.Tool, "task_id": j.ID, "state": j.State, "reused": reused}
	if j.State == "completed" {
		if j.Bytes > 0 {
			if !t.replyAllowed(t.trace) || !j.MediaGrant.Equal(t.replies[t.trace.Thread]) {
				return "", wechatapp.ErrAttachmentDenied
			}
			media := j.media()
			t.preparedSpeech(media)
			view["media"] = []companion.GeneratedMedia{media}
			view["declared_kind"] = j.DeclaredKind
			view["delivery_kind"] = "file"
		} else {
			raw, _ := json.Marshal(map[string]string{"text": j.Text})
			page, err := localExternalTextPage(raw, offset, limit)
			if err != nil {
				return "", err
			}
			view["output"] = page
		}
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
	if len(arguments) > 8<<10 {
		return "", mcptools.ArgumentError("$.arguments", "外部工具参数超过协议的 8 KiB 限制")
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
	if tools[index].MediaURLPath != "" || tools[index].TextPath == "" && tools[index].MediaKind == "" {
		return "", errors.New("该工具没有当前微信适配器支持的响应类型")
	}
	if tools[index].MediaKind != "" && !t.replyAllowed(t.trace) {
		return "", wechatapp.ErrAttachmentDenied
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
		Trace: t.trace.TraceID, Fish: owner.FishID, Source: *source, Grant: t.grants[t.trace.Thread], MediaGrant: t.replies[t.trace.Thread],
		Registration: externalDigest(registration), Tool: input.Tool,
		RequestHash: localCheckpointKey(input.Tool, arguments), State: "submitting"}
	j.ID = j.expectedID()
	previous, previousErr := t.readExternalHTTPJob(j.ID)
	if errors.Is(previousErr, os.ErrNotExist) {
		previous, previousErr = t.readExternalHTTPJob(j.previousID())
	}
	if previousErr == nil {
		if !t.externalHTTPAllowed(t.trace, previous) || previous.Tool != input.Tool || previous.Fish != owner.FishID {
			return "", wechatapp.ErrAttachmentDenied
		}
		if previous.Registration != j.Registration {
			return localProcessJSON(map[string]any{"tool": input.Tool, "task_id": previous.ID, "state": "needs_review", "reused": true,
				"instruction": "工作站登记已变化；请核对原请求，不要重新提交同一操作。"})
		}
		if previous.State == "submitting" {
			previous.State = "needs_review"
		}
		return t.externalHTTPView(previous, input.Offset, input.Limit, true)
	} else if !errors.Is(previousErr, os.ErrNotExist) {
		return "", previousErr
	}
	if input.Offset != 0 {
		return "", mcptools.ArgumentError("$.text_offset", "首次提交请从正文起点读取")
	}
	if !t.externalHTTPAllowed(t.trace, j) {
		return "", wechatapp.ErrAttachmentDenied
	}
	if err := t.writeExternalHTTPJob(j, nil); err != nil {
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
	output, callErr := t.projects.publicExternalHTTP(bound, owner.FishID, j.Registration, input.Tool, arguments)
	cancel()
	<-done
	if callErr != nil {
		j.State = "needs_review"
	} else {
		j.State, j.Text = "completed", output.Text
		if len(output.Media) == 1 {
			media := output.Media[0]
			j.Bytes = int64(len(media.Data))
			j.SHA256 = externalDigest(media.Data)
			j.DeclaredKind = media.Kind
			j.MIME, _, _ = mime.ParseMediaType(media.MIMEType)
			if j.MIME == "" {
				j.MIME = "application/octet-stream"
			}
		}
	}
	var binary []byte
	if len(output.Media) == 1 && callErr == nil {
		binary = output.Media[0].Data
	}
	if err := t.writeExternalHTTPJob(j, binary); err != nil {
		return "", err
	}
	if !t.externalHTTPAllowed(t.trace, j) {
		return "", wechatapp.ErrAttachmentDenied
	}
	return t.externalHTTPView(j, 0, input.Limit, false)
}

func (t *stationAttachmentTools) externalHTTPMediaAllowed(in companion.Input, asset string) (stationWechatExternalHTTPJob, bool) {
	var empty stationWechatExternalHTTPJob
	if len(asset) != 73 || asset[:9] != "external-" || !validLocalRVCHex(asset[9:], 64) {
		return empty, false
	}
	j, err := t.readExternalHTTPJob(asset[9:])
	return j, err == nil && j.State == "completed" && j.Bytes > 0 && j.SHA256 != "" &&
		j.MediaGrant.Equal(t.replies[in.Thread]) && t.replyAllowed(in) && t.externalHTTPAllowed(in, j)
}

func (t *stationAttachmentTools) stageExternalHTTP(ctx context.Context, in companion.Input, asset string) (string, error) {
	j, ok := t.externalHTTPMediaAllowed(in, asset)
	if !ok {
		return "", wechatapp.ErrAttachmentDenied
	}
	root, err := t.externalHTTPRoot(j.ID)
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
		return "", errors.New("外部工具成品内容已变化")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	upload, err := wechatapp.StageMediaUpload(ctx, t.native, wechatapp.MediaUpload{
		Account: in.Account, Filename: j.media().Title, MIME: j.MIME, Bytes: j.Bytes, SHA256: j.SHA256,
	}, file, func() bool { _, allowed := t.externalHTTPMediaAllowed(in, asset); return allowed })
	if err != nil {
		return "", err
	}
	if _, ok := t.externalHTTPMediaAllowed(in, asset); !ok {
		return "", wechatapp.ErrAttachmentDenied
	}
	return upload, nil
}
