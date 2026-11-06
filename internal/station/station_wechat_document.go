package station

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

const stationDocumentLimit = 128 << 20

type stationDocumentTools struct {
	available func(context.Context, string) bool
	inspect   func(context.Context, stationDocumentInput) (string, error)
}

type stationDocumentInput struct {
	record              stationAttachmentRecord
	file                *os.File
	extension, epoch    string
	requester, trace    string
	versions            map[string]int
	page, offset, limit int
	check               func(context.Context) error
}

func stationDocumentExtension(name string) string {
	switch ext := strings.ToLower(filepath.Ext(name)); ext {
	case ".docx", ".pdf":
		return ext
	default:
		return ""
	}
}

func (t *stationAttachmentTools) documentRefs() []*companion.SourceRef {
	var refs []*companion.SourceRef
	for _, ref := range []*companion.SourceRef{t.trace.Source, t.trace.QuotedSource} {
		if ref != nil && ref.MessageType == 49 {
			refs = append(refs, ref)
		}
	}
	for _, event := range t.trace.UnreadMessages {
		if ref := event.Source; ref != nil && ref.MessageType == 49 && ref.MessageID == event.MessageID &&
			ref.Sender == t.trace.Sender && event.Sender == t.trace.Sender {
			refs = append(refs, ref)
		}
	}
	return refs
}

func (t *stationAttachmentTools) inspectDocument(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Message string `json:"message_id"`
		Page    *int   `json:"page"`
		Offset  int    `json:"text_offset"`
		Limit   *int   `json:"text_limit"`
	}
	if !t.allowed() || t.documents.available == nil || t.documents.inspect == nil {
		return "", wechatapp.ErrAttachmentDenied
	}
	if strictLocalMemoryDecode(raw, &args) != nil || args.Message == "" || len(args.Message) > 8192 || args.Offset < 0 ||
		args.Page != nil && *args.Page < 1 || args.Limit != nil && (*args.Limit < 1 || *args.Limit > 100000) {
		return "", mcptools.ArgumentError("$", "需要原消息编号，页码从 1 开始，正文偏移非负，每批可读取 1–100000 个字符")
	}
	var selected *companion.SourceRef
	for _, ref := range t.documentRefs() {
		if ref.MessageID == args.Message {
			selected = ref
			break
		}
	}
	if selected == nil {
		return "", wechatapp.ErrAttachmentDenied
	}
	request := t.trace
	request.TraceID, request.Source = args.Message, selected
	access, err := t.attachmentAccess(request, args.Message, t.allowed)
	if err != nil {
		return "", err
	}
	access.maxBytes = stationDocumentLimit
	epoch, err := t.store.RecallEpoch(ctx)
	if err != nil {
		return "", wechatapp.ErrAttachmentDenied
	}
	source, err := access.source(ctx, t.trace.Thread, args.Message)
	if err != nil {
		return "", err
	}
	sourceInput := companion.Input{Channel: "wechat", Account: source.Account, Thread: source.Thread, Sender: source.Sender,
		TraceID: source.Message, MessageTime: source.Occurred, Source: selected}
	stored, ok := t.store.SourceForInput(sourceInput)
	if !ok {
		return "", wechatapp.ErrAttachmentDenied
	}
	access.ref = stored
	ext := stationDocumentExtension(source.Filename)
	if source.Kind != "file" || ext == "" {
		return "", mcptools.ArgumentError("$.message_id", "此工具读取已关联的 Word（.docx）或 PDF 附件")
	}
	if ext == ".docx" && args.Page != nil {
		return "", mcptools.ArgumentError("$.page", "DOCX 请按 text_offset 续读正文")
	}
	if !t.documents.available(ctx, ext) {
		return "", errors.New("当前文档格式的本地文档工坊或运行环境未启用")
	}
	check := func(ctx context.Context) error { return access.confirmObservationSource(ctx, source, epoch) }
	if err := check(ctx); err != nil {
		return "", err
	}
	record, file, err := access.open(ctx, source)
	if errors.Is(err, os.ErrNotExist) {
		if err := access.DownloadAttachment(ctx, source.Thread, source.Message); err != nil {
			return "", err
		}
		record, file, err = access.open(ctx, source)
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	if record.Bytes > stationDocumentLimit {
		return "", errors.New("文档原件超过文档工坊的 128 MiB 输入上限")
	}
	if err := verifyStationDocument(file, record.Bytes, ext); err != nil {
		return "", err
	}
	if err := check(ctx); err != nil {
		return "", err
	}
	in := stationDocumentInput{record: record, file: file, extension: ext, epoch: epoch, requester: t.trace.Sender, trace: t.trace.TraceID,
		versions: map[string]int{stored.EventID: stored.Revision}, offset: args.Offset, limit: 4000, check: check}
	if ext == ".pdf" {
		in.page = 1
		if args.Page != nil {
			in.page = *args.Page
		}
	}
	if args.Limit != nil {
		in.limit = *args.Limit
	}
	return t.documents.inspect(ctx, in)
}

func verifyStationDocument(file *os.File, size int64, ext string) error {
	if ext == ".docx" {
		if validateLocalDOCX(file, size) != nil {
			return errors.New("Word 附件缺少有效的 DOCX 文档结构")
		}
	} else {
		var header [5]byte
		if _, err := file.ReadAt(header[:], 0); err != nil || !bytes.Equal(header[:], []byte("%PDF-")) {
			return errors.New("附件缺少有效的 PDF 文件头")
		}
	}
	return nil
}
