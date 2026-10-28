package admin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type uploadFailure struct {
	status  int
	message string
}

func manualUploadContext(w http.ResponseWriter, r *http.Request) (context.Context, context.CancelFunc, error) {
	seconds := int64(600)
	if r.URL.Query().Has("timeout_seconds") {
		var err error
		seconds, err = strconv.ParseInt(r.URL.Query().Get("timeout_seconds"), 10, 64)
		if err != nil || seconds < 0 || seconds > int64((1<<63-1)/time.Second)-30 {
			return nil, nil, errors.New("上传超时秒数无效；0表示不设置上传时限")
		}
	}
	var deadline time.Time
	ctx, cancel := context.WithCancel(r.Context())
	if seconds > 0 {
		cancel()
		deadline = time.Now().Add(time.Duration(seconds) * time.Second)
		ctx, cancel = context.WithDeadline(r.Context(), deadline)
	}
	// Override the JSON server's short body-read deadline only on this
	// authenticated upload route. Supported by real HTTP connections; recorders
	// and wrappers may explicitly report ErrNotSupported.
	controller := http.NewResponseController(w)
	for _, err := range []error{controller.SetReadDeadline(deadline), controller.SetWriteDeadline(deadline)} {
		if err != nil && !errors.Is(err, http.ErrNotSupported) {
			cancel()
			return nil, nil, errors.New("无法设置附件连接时限")
		}
	}
	return ctx, cancel, nil
}

func (e *uploadFailure) Error() string { return e.message }

type privateUpload struct {
	file                    *os.File
	name, kind, contentType string
	size                    int64
}

func (u *privateUpload) close() {
	if u.file != nil {
		u.file.Close()
		os.Remove(u.file.Name())
	}
}

// A private service-owned staging file avoids both ParseMultipartForm's global
// temp directory and a whole-file RAM allocation. Field order remains flexible.
func stageManualUpload(ctx context.Context, r *http.Request, dir string, max int64, free func(string) (uint64, error)) (_ *privateUpload, err error) {
	failure := func(code int, text string) error { return &uploadFailure{code, text} }
	if ctx.Err() != nil {
		return nil, failure(408, "附件上传已取消或超时")
	}
	form, err := r.MultipartReader()
	if err != nil {
		return nil, failure(400, "附件表单无效")
	}
	if err = ensureManaged(dir); err != nil {
		return nil, failure(503, "附件私有暂存目录不可用")
	}
	u := &privateUpload{}
	defer func() {
		if err != nil {
			u.close()
		}
	}()
	seen := map[string]bool{}
	for {
		part, e := form.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, failure(400, "附件表单未完整接收或超过后台大小限制")
		}
		key := part.FormName()
		if seen[key] || key != "file" && key != "kind" {
			return nil, failure(400, "只接受一个附件和一个类型字段")
		}
		seen[key] = true
		if key == "kind" {
			if part.FileName() != "" {
				return nil, failure(400, "附件类型字段无效")
			}
			b, e := io.ReadAll(io.LimitReader(part, 33))
			if e != nil || len(b) > 32 || toolsLimit(string(b)) == 0 {
				return nil, failure(400, "请选择有效附件类型")
			}
			u.kind = string(b)
			part.Close()
			continue
		}
		u.name = filepath.Base(strings.ReplaceAll(part.FileName(), "\\", "/"))
		if u.name == "." || u.name == "" || len(u.name) > 256 || !utf8.ValidString(u.name) || strings.ContainsAny(u.name, "\r\n\x00") {
			return nil, failure(400, "附件名称无效")
		}
		u.contentType = part.Header.Get("Content-Type")
		if u.contentType == "" {
			u.contentType = "application/octet-stream"
		}
		u.file, e = os.CreateTemp(dir, ".upload-*")
		if e != nil {
			return nil, failure(503, "附件私有暂存不可用")
		}
		w := &capacityWriter{ctx: ctx, file: u.file, dir: dir, free: free}
		u.size, e = io.Copy(w, io.LimitReader(part, max+1))
		if errors.Is(e, errUploadStorage) {
			return nil, failure(507, "附件暂存空间不足或写入失败，未上传到微信后台")
		}
		if ctx.Err() != nil {
			return nil, failure(408, "附件上传已取消或超时")
		}
		if e != nil {
			return nil, failure(400, "附件内容接收失败")
		}
		if u.size <= 0 || u.size > max {
			return nil, failure(400, "附件大小超过后台能力或内容为空")
		}
		part.Close()
	}
	if !seen["file"] || !seen["kind"] || u.file == nil {
		return nil, failure(400, "请选择一个有效类型和附件")
	}
	if err = u.file.Sync(); err != nil {
		return nil, failure(507, "附件暂存落盘失败")
	}
	if _, err = u.file.Seek(0, 0); err != nil {
		return nil, failure(503, "附件暂存读取失败")
	}
	return u, nil
}

var errUploadStorage = errors.New("upload_storage_unavailable")

type capacityWriter struct {
	ctx       context.Context
	file      *os.File
	dir       string
	free      func(string) (uint64, error)
	remaining uint64
}

func (w *capacityWriter) Write(b []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	// Recheck physical availability at most once per MiB, including unknown-
	// length bodies and files arriving before their kind field. No quota.
	if w.remaining < uint64(len(b)) {
		available, err := w.free(w.dir)
		if err != nil || available < uint64(len(b)) {
			return 0, errUploadStorage
		}
		w.remaining = min(available, max(1<<20, uint64(len(b))))
	}
	n, err := w.file.Write(b)
	w.remaining -= uint64(n)
	if err != nil {
		return n, errUploadStorage
	}
	return n, err
}
