package station

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/wechatui"
	"wechat-bot/internal/workspacefs"
)

const wechatCanvasFile = "flow-canvas.json"

func readWechatCanvas(root *os.Root) (wechatui.CanvasView, error) {
	var view wechatui.CanvasView
	var raw []byte
	var err error
	if root != nil {
		raw, err = readLegacyImportFile(root, wechatCanvasFile, 128<<10)
	} else {
		err = os.ErrNotExist
	}
	view.Saved = err == nil
	if errors.Is(err, os.ErrNotExist) {
		raw, err = wechatui.DefaultCanvas()
	}
	if err != nil || json.Unmarshal(raw, &view.Document) != nil || wechatui.ValidateCanvas(view.Document) != nil {
		return wechatui.CanvasView{}, errors.New("画布文件不可读，原文件已保留")
	}
	view.Revision = hexDigest(raw)
	return view, nil
}

func (s *stationWechat) canvasRoute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("connection")
	if !identity.ValidPathID(id) {
		websession.Fail(w, 400, "连接编号无效")
		return
	}
	connection, err := s.Inspect(r.Context(), id)
	if err != nil || connection.ID != id || connection.AccountID == "" {
		websession.Fail(w, 404, "无法核对当前微信连接")
		return
	}
	var input struct {
		Base     string                  `json:"base_revision"`
		Document wechatui.CanvasDocument `json:"document"`
	}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.Base == "" || wechatui.ValidateCanvas(input.Document) != nil {
			websession.Fail(w, 400, "画布内容无效，请保留草稿并检查后再保存")
			return
		}
	}
	worker, err := s.worker(id, r.Method == http.MethodPost)
	if err != nil {
		websession.Fail(w, 503, "画布暂时不可用，原文件已保留")
		return
	}
	var root *os.Root
	if worker != nil {
		worker.canvasMu.Lock()
		defer worker.canvasMu.Unlock()
		root, err = workspacefs.OpenPath(worker.dir)
		if err != nil {
			websession.Fail(w, 503, "画布目录不可读，原文件已保留")
			return
		}
		defer root.Close()
	}
	view, err := readWechatCanvas(root)
	if err != nil {
		websession.Fail(w, 503, err.Error())
		return
	}
	if r.Method == http.MethodPost {
		if input.Base != view.Revision {
			websession.Fail(w, 409, "画布已在其他页面更新，请先保留草稿再载入服务器版本")
			return
		}
		data, marshalErr := json.Marshal(input.Document)
		if marshalErr != nil {
			websession.Fail(w, 400, "画布内容无效")
			return
		}
		// Bind the save to the same current connection, including offline accounts.
		now, inspectErr := s.stationWechatNative.Inspect(r.Context(), id)
		if inspectErr != nil || now.ID != id || now.AccountID != connection.AccountID {
			websession.Fail(w, 409, "微信连接已变化，草稿仍保留")
			return
		}
		temporary := ".canvas-" + identity.RandomToken()
		defer root.Remove(temporary)
		file, createErr := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if createErr == nil {
			_, createErr = file.Write(data)
			createErr = errors.Join(createErr, file.Close())
		}
		if createErr == nil {
			createErr = root.Rename(temporary, wechatCanvasFile)
		}
		if createErr != nil {
			websession.Fail(w, 503, "画布未能保存，草稿仍保留")
			return
		}
		view = wechatui.CanvasView{Document: input.Document, Revision: hexDigest(data), Saved: true}
	}
	websession.Respond(w, 200, struct {
		wechatui.CanvasView
		Connection apphost.ConnectionStatus `json:"connection"`
	}{view, connection})
}
