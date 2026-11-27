package station

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

const localChatAttachmentLimit = 128 << 20
const localChatAttachmentTool = "home_attachment_read"

type localChatAttachment struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MIME        string `json:"mime"`
	Bytes       int64  `json:"bytes"`
	Fish        string `json:"-"`
	Owner       string `json:"-"`
	PreviewURL  string `json:"preview_url,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
}

type localChatHistoryItem struct {
	companion.LocalConversationItem
	Attachments []localChatAttachment `json:"attachments,omitempty"`
}

type localChatAttachmentDisk struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	MIME  string `json:"mime"`
	Bytes int64  `json:"bytes"`
	Fish  string `json:"fish"`
	Owner string `json:"owner"`
	File  string `json:"file"`
}

type localChatRequestManifest struct {
	Text              string                  `json:"text"`
	Attachments       []string                `json:"attachments"`
	ContinueRequestID string                  `json:"continue_request_id,omitempty"`
	ModelSelection    localChatModelSelection `json:"model_selection,omitempty"`
	Owner             string                  `json:"owner"`
}

func localAttachmentID(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}

func localAttachmentName(raw string) string {
	name := strings.TrimSpace(filepath.Base(strings.ReplaceAll(raw, "\\", "/")))
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return "附件"
	}
	if len(name) > 240 {
		name = string([]rune(name)[:min(80, utf8.RuneCountInString(name))])
	}
	return name
}

func localAttachmentDataName(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if len(ext) > 16 || strings.ContainsAny(ext, "/\\:\x00") {
		ext = ""
	}
	return "source" + ext
}

func (h *localChatService) attachmentAccess(in companion.Input) (*localWorkspaceAccess, localWorkspaceGrant, error) {
	access := &localWorkspaceAccess{home: h.home, fishID: in.Account, ownerID: in.Sender}
	grant, err := access.resolve()
	return access, grant, err
}

func attachmentRoot(grant localWorkspaceGrant, sub string, create bool) (*os.Root, error) {
	root, err := workspacefs.OpenPath(grant.Directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if create {
		return workspacefs.EnsureDirectory(root, filepath.Join("messages", sub))
	}
	return workspacefs.OpenExistingDirectory(root, filepath.Join("messages", sub))
}

func attachmentView(d localChatAttachmentDisk) localChatAttachment {
	a := localChatAttachment{ID: d.ID, Name: d.Name, MIME: d.MIME, Bytes: d.Bytes}
	a.DownloadURL = "/api/local/chat/attachments/" + d.ID + "/download"
	if previewableChatImage(d.MIME) {
		a.PreviewURL = "/api/local/chat/attachments/" + d.ID + "/preview"
	}
	return a
}

func previewableChatImage(kind string) bool {
	switch kind {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif":
		return true
	}
	return false
}

func readAttachment(grant localWorkspaceGrant, id string) (localChatAttachmentDisk, *os.File, error) {
	if !localAttachmentID(id) {
		return localChatAttachmentDisk{}, nil, errors.New("附件编号无效")
	}
	root, err := attachmentRoot(grant, "files", false)
	if err != nil {
		return localChatAttachmentDisk{}, nil, err
	}
	defer root.Close()
	meta, err := workspacefs.OpenRegular(root, filepath.Join(id, "meta.json"))
	if err != nil {
		return localChatAttachmentDisk{}, nil, err
	}
	defer meta.Close()
	var d localChatAttachmentDisk
	if err = json.NewDecoder(io.LimitReader(meta, 4096)).Decode(&d); err != nil {
		return d, nil, err
	}
	if d.ID != id || d.Fish != grant.FishID || d.Owner != grant.OwnerID || d.File != localAttachmentDataName(d.Name) {
		return d, nil, errors.New("附件归属或记录无效")
	}
	f, err := workspacefs.OpenRegular(root, filepath.Join(id, d.File))
	if err != nil {
		return d, nil, err
	}
	info, err := f.Stat()
	if err != nil || info.Size() != d.Bytes {
		f.Close()
		return d, nil, errors.New("附件内容已变化")
	}
	return d, f, nil
}

func verifyAttachmentBytes(f *os.File, id string) error {
	if !localAttachmentID(id) {
		return errors.New("附件编号无效")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != id {
		return errors.New("附件内容已变化")
	}
	return nil
}

func (h *localChatService) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	_, in, err := h.conversation()
	if err != nil {
		websession.Fail(w, 409, err.Error())
		return
	}
	access, grant, err := h.attachmentAccess(in)
	if err != nil {
		websession.Fail(w, 409, err.Error())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, localChatAttachmentLimit+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		websession.Fail(w, 400, "请上传 multipart 文件")
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" || part.FileName() == "" {
		websession.Fail(w, 400, "请提供 file 文件字段")
		return
	}
	name := localAttachmentName(part.FileName())
	var result localChatAttachment
	err = access.withCurrentWriteGrant(grant, func() error {
		root, err := attachmentRoot(grant, "files", true)
		if err != nil {
			return err
		}
		defer root.Close()
		var nonce [16]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return err
		}
		tmp := ".upload-" + hex.EncodeToString(nonce[:])
		f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		defer root.Remove(tmp)
		hash := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(f, hash), io.LimitReader(part, localChatAttachmentLimit+1))
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n == 0 {
			return errors.New("文件为空")
		}
		if n > localChatAttachmentLimit {
			return fmt.Errorf("附件超过 128 MiB")
		}
		if extra, err := reader.NextPart(); err != io.EOF || extra != nil {
			return errors.New("每次只上传一个文件")
		}
		id := hex.EncodeToString(hash.Sum(nil))
		if d, existing, err := readAttachment(grant, id); err == nil {
			if err := verifyAttachmentBytes(existing, id); err != nil {
				existing.Close()
				return err
			}
			existing.Close()
			result = attachmentView(d)
			return nil
		}
		if err := root.Mkdir(id, 0700); err != nil {
			return err
		}
		dataName := localAttachmentDataName(name)
		if err := root.Rename(tmp, filepath.Join(id, dataName)); err != nil {
			return err
		}
		stored, err := workspacefs.OpenRegular(root, filepath.Join(id, dataName))
		if err != nil {
			return err
		}
		var sniff [512]byte
		read, _ := stored.Read(sniff[:])
		stored.Close()
		kind := http.DetectContentType(sniff[:read])
		if kind == "application/octet-stream" {
			if byExt := mime.TypeByExtension(filepath.Ext(name)); byExt != "" {
				kind = byExt
			}
		}
		d := localChatAttachmentDisk{ID: id, Name: name, MIME: kind, Bytes: n, Fish: grant.FishID, Owner: grant.OwnerID, File: dataName}
		meta, err := root.OpenFile(filepath.Join(id, "meta.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		encoded := json.NewEncoder(meta).Encode(d)
		closed := meta.Close()
		if encoded != nil {
			return encoded
		}
		if closed != nil {
			return closed
		}
		result = attachmentView(d)
		return nil
	})
	if err != nil {
		status := 409
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) || strings.Contains(err.Error(), "128 MiB") {
			status = http.StatusRequestEntityTooLarge
		}
		websession.Fail(w, status, err.Error())
		return
	}
	websession.Respond(w, 200, map[string]any{"attachment": result})
}

func (h *localChatService) serveAttachment(w http.ResponseWriter, r *http.Request) {
	mode := r.PathValue("mode")
	if mode != "preview" && mode != "download" {
		websession.Fail(w, 404, "附件路径不存在")
		return
	}
	if r.PathValue("mode") != "preview" && r.PathValue("mode") != "download" {
		websession.Fail(w, 404, "附件入口不存在")
		return
	}
	_, in, err := h.conversation()
	if err != nil {
		websession.Fail(w, 409, err.Error())
		return
	}
	access, grant, err := h.attachmentAccess(in)
	if err != nil {
		websession.Fail(w, 409, err.Error())
		return
	}
	d, f, err := readAttachment(grant, r.PathValue("id"))
	if err != nil || !access.current(grant) {
		if f != nil {
			f.Close()
		}
		websession.Fail(w, 404, "附件不存在")
		return
	}
	defer f.Close()
	if mode == "preview" && !previewableChatImage(d.MIME) {
		websession.Fail(w, 404, "此文件不支持预览")
		return
	}
	w.Header().Set("Content-Type", d.MIME)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if mode == "download" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": d.Name}))
	}
	info, _ := f.Stat()
	http.ServeContent(w, r, d.Name, info.ModTime(), f)
}

func (h *localChatService) requestManifestRoot(in companion.Input, create bool) (*os.Root, error) {
	base := filepath.Join(stationMemoryDir(h.home.dataDir), in.Account)
	root, err := workspacefs.EnsurePath(base)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if create {
		return workspacefs.EnsureDirectory(root, "local-chat-requests")
	}
	return workspacefs.OpenExistingDirectory(root, "local-chat-requests")
}

func (h *localChatService) readRequestManifest(in companion.Input, requestID string) (*localChatRequestManifest, error) {
	if !identity.ValidPathID(requestID) {
		return nil, errors.New("请求编号无效")
	}
	root, err := h.requestManifestRoot(in, false)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := workspacefs.OpenRegular(root, requestID+".json")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var m localChatRequestManifest
	if err := json.NewDecoder(io.LimitReader(f, 64<<10)).Decode(&m); err != nil {
		return nil, err
	}
	if m.Owner != in.Sender {
		return nil, errors.New("请求归属已变化")
	}
	return &m, nil
}

func (h *localChatService) bindRequestAttachments(in companion.Input, requestID, text, continueID string, ids []string, selection localChatModelSelection, existing bool) ([]companion.IncomingMediaRef, error) {
	manifest, err := h.readRequestManifest(in, requestID)
	if err != nil {
		return nil, err
	}
	if manifest != nil && (manifest.Text != text || manifest.ContinueRequestID != continueID || !sameAttachmentIDs(manifest.Attachments, ids) || !sameModelSelection(manifest.ModelSelection, selection)) {
		return nil, errors.New("同一请求编号的附件、正文或模型选项已变化，请使用新请求编号")
	}
	if existing && manifest == nil && (len(ids) > 0 || !emptyModelSelection(selection)) {
		return nil, errors.New("原请求没有附件或模型选项记录")
	}
	refs := make([]companion.IncomingMediaRef, 0, len(ids))
	if len(ids) > 0 {
		access, grant, err := h.attachmentAccess(in)
		if err != nil {
			return nil, err
		}
		if !access.current(grant) {
			return nil, errors.New("家目录授权已变化")
		}
		for _, id := range ids {
			d, f, err := readAttachment(grant, id)
			if err != nil {
				return nil, err
			}
			if err := verifyAttachmentBytes(f, id); err != nil {
				f.Close()
				return nil, err
			}
			f.Close()
			kind := "file"
			switch {
			case strings.HasPrefix(d.MIME, "image/"):
				kind = "image"
			case strings.HasPrefix(d.MIME, "video/"):
				kind = "video"
			case strings.HasPrefix(d.MIME, "audio/"):
				kind = "voice"
			}
			refs = append(refs, companion.IncomingMediaRef{Source: companion.SourceRef{MessageID: requestID}, AssetID: id, Kind: kind, Filename: d.Name, UploaderID: in.Sender, ObservationState: "available"})
		}
	}
	if manifest == nil && !existing {
		manifest = &localChatRequestManifest{Text: text, ContinueRequestID: continueID, Attachments: append([]string(nil), ids...), ModelSelection: selection, Owner: in.Sender}
		root, err := h.requestManifestRoot(in, true)
		if err != nil {
			return nil, err
		}
		defer root.Close()
		f, err := root.OpenFile(requestID+".json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil, err
		}
		writeErr := json.NewEncoder(f).Encode(manifest)
		closeErr := f.Close()
		if writeErr != nil {
			return nil, writeErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	return refs, nil
}

func emptyModelSelection(s localChatModelSelection) bool {
	return s.Model == "" && s.ThinkingEnabled == nil && s.ReasoningEffort == ""
}
func sameModelSelection(a, b localChatModelSelection) bool {
	if a.Model != b.Model || a.ReasoningEffort != b.ReasoningEffort || (a.ThinkingEnabled == nil) != (b.ThinkingEnabled == nil) {
		return false
	}
	return a.ThinkingEnabled == nil || *a.ThinkingEnabled == *b.ThinkingEnabled
}

func sameAttachmentIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (h *localChatService) historyAttachments(in companion.Input, requestID string) []localChatAttachment {
	_, grant, err := h.attachmentAccess(in)
	if err != nil {
		return nil
	}
	m, err := h.readRequestManifest(in, requestID)
	if err != nil || m == nil {
		return nil
	}
	out := make([]localChatAttachment, 0, len(m.Attachments))
	for _, id := range m.Attachments {
		d, f, err := readAttachment(grant, id)
		if err == nil {
			f.Close()
			out = append(out, attachmentView(d))
		}
	}
	return out
}

func homeAttachmentTool() mcptools.Tool {
	return mcptools.NewTool(localChatAttachmentTool, "读取主人本机聊天上传的原始附件。id 取当前消息 media_observations 的 asset_id；图片和支持的文档会作为原件送入下一次模型请求，文本按 offset 分段返回。其他音视频保留工作目录中的真实路径供已登记工具读取。", `{"type":"object","properties":{"id":{"type":"string","minLength":64,"maxLength":64},"offset":{"type":"integer","minimum":0},"length":{"type":"integer","minimum":1}},"required":["id"],"additionalProperties":false}`)
}

func (t *localToolSet) readHomeAttachment(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		ID     string `json:"id"`
		Offset int64  `json:"offset"`
		Length int64  `json:"length"`
	}
	if strictLocalMemoryDecode(raw, &in) != nil || !localAttachmentID(in.ID) || in.Offset < 0 || in.Length < 0 {
		return "", errors.New("附件读取参数无效")
	}
	if t.access == nil || t.run == nil || !t.run.Tracked {
		return "", errors.New("本轮附件读取不可用")
	}
	if err := t.run.CheckSource(ctx); err != nil {
		return "", err
	}
	grant, err := t.access.resolve()
	if err != nil {
		return "", err
	}
	d, f, err := readAttachment(grant, in.ID)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if !t.access.current(grant) {
		return "", errors.New("家目录授权已变更")
	}
	if strings.HasPrefix(d.MIME, "image/") && t.directMedia != nil && d.Bytes <= 32<<20 {
		data, err := io.ReadAll(f)
		if err != nil {
			return "", err
		}
		if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != d.ID {
			return "", errors.New("附件内容已变化")
		}
		if err := deepseek.AttachImage(ctx, d.Name, deepseek.Image{Data: data}); err != nil {
			return "", err
		}
		return `{"attached":true,"mode":"direct_image","coverage":"original_file"}`, nil
	}
	if strings.HasPrefix(d.MIME, "image/") && d.Bytes <= 32<<20 {
		data, err := io.ReadAll(f)
		if err != nil {
			return "", err
		}
		if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != d.ID {
			return "", errors.New("附件内容已变化")
		}
		check := func() error {
			if err := t.run.CheckSource(ctx); err != nil {
				return err
			}
			if !t.access.current(grant) {
				return errors.New("家目录授权已变更")
			}
			current, original, err := readAttachment(grant, d.ID)
			if err != nil {
				return err
			}
			defer original.Close()
			if current.Bytes != d.Bytes || current.MIME != d.MIME {
				return errors.New("附件记录已变化")
			}
			actual, err := io.ReadAll(io.LimitReader(original, d.Bytes+1))
			if err != nil {
				return err
			}
			sum := sha256.Sum256(actual)
			if hex.EncodeToString(sum[:]) != d.ID {
				return errors.New("附件内容已变化")
			}
			return nil
		}
		observation, err := t.access.home.observeLocalStationImages(ctx, "home-chat:"+t.traceID+":"+d.ID,
			"请依据图片本身描述画面和可见文字，着重回答当前问题。", []deepseek.Image{{Data: data}}, 1024, check)
		if err == nil {
			out, _ := json.Marshal(map[string]any{"id": d.ID, "name": d.Name, "mode": "station_vision", "observation": observation, "coverage": "original_image"})
			return string(out), nil
		}
		// The original file remains available to other registered tools even
		// when the station vision provider is not configured.
	}
	if t.directMedia != nil && d.Bytes <= 100<<20 && !strings.HasPrefix(d.MIME, "video/") && !strings.HasPrefix(d.MIME, "audio/") && !strings.HasPrefix(d.MIME, "image/") {
		data, err := io.ReadAll(f)
		if err == nil {
			if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != d.ID {
				return "", errors.New("附件内容已变化")
			}
			if _, attachErr := t.directMedia.AttachDocument(ctx, d.Name, data, false); attachErr == nil {
				return `{"attached":true,"mode":"direct_document","coverage":"original_file"}`, nil
			}
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
	}
	if strings.HasPrefix(d.MIME, "text/") || strings.HasSuffix(d.Name, ".md") || strings.HasSuffix(d.Name, ".json") || strings.HasSuffix(d.Name, ".csv") {
		if in.Offset > d.Bytes {
			return "", errors.New("offset 超过文件长度")
		}
		length := in.Length
		if length == 0 {
			length = 16000
		}
		length = min(length, 64000)
		if _, err := f.Seek(in.Offset, io.SeekStart); err != nil {
			return "", err
		}
		data, err := io.ReadAll(io.LimitReader(f, length))
		if err != nil {
			return "", err
		}
		out, _ := json.Marshal(map[string]any{"id": d.ID, "name": d.Name, "content": string(data), "next_offset": in.Offset + int64(len(data)), "has_more": in.Offset+int64(len(data)) < d.Bytes})
		return string(out), nil
	}
	path := filepath.Join(grant.Directory, "messages", "files", d.ID, d.File)
	out, _ := json.Marshal(map[string]any{"id": d.ID, "name": d.Name, "mime": d.MIME, "bytes": d.Bytes, "path": path, "note": "原文件已在大肥鱼工作目录；音视频可交给对应已登记工具处理。"})
	return string(out), nil
}
