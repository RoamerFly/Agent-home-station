package station

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"wechat-bot/internal/websession"
)

func quarantineToolFile(file toolFile, dataRoot, pendingRoot string) (string, error) {
	root, err := filepath.Abs(dataRoot)
	if err != nil {
		return "", err
	}
	source, err := filepath.EvalSymlinks(file.Path)
	if err != nil || !strings.EqualFold(source, filepath.Clean(file.Path)) {
		return "", errors.New("文件路径已变化")
	}
	relative, err := filepath.Rel(root, source)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("文件不在工作站数据目录")
	}
	target := filepath.Join(pendingRoot, relative)
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil || !strings.EqualFold(parent, filepath.Dir(target)) {
		return "", errors.New("待删除目录包含链接")
	}
	if _, err = os.Lstat(target); !os.IsNotExist(err) {
		return "", errors.New("待删除目录已有同名文件")
	}
	if err = os.Rename(source, target); err != nil {
		return "", errors.New("文件正在使用、无权移动或目标不在同一磁盘；原文件保留")
	}
	return target, nil
}
func (s *localProjectTools) toolFileDeleteRoute(owner *websession.Store, mux *http.ServeMux) {
	mux.HandleFunc("POST /api/local/tool-files/delete", owner.Protect(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			IDs []string `json:"ids"`
		}
		if !websession.Decode(w, r, &in) {
			return
		}
		if len(in.IDs) == 0 {
			websession.Fail(w, 400, "请先选择文件")
			return
		}
		files, warnings := s.toolFiles(r.Context())
		if r.Context().Err() != nil {
			return
		}
		byID := map[string]toolFile{}
		for _, f := range files {
			byID[f.ID] = f
		}
		// One unique batch prevents overwriting prior user-deleted artifacts.
		batch := filepath.Join(`E:\待删除`, "tool-files-"+time.Now().Format("20060102-150405.000000000"))
		moved := []map[string]string{}
		failed := []map[string]string{}
		seen := map[string]bool{}
		for _, id := range in.IDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			f, ok := byID[id]
			if !ok {
				failed = append(failed, map[string]string{"id": id, "reason": "文件已变化或不在正式文件列表，请刷新"})
				continue
			}
			if f.State == "running" || f.State == "queued" {
				failed = append(failed, map[string]string{"id": id, "reason": "任务仍在运行"})
				continue
			}
			path, err := quarantineToolFile(f, s.home.dataDir, batch)
			if err != nil {
				failed = append(failed, map[string]string{"id": id, "reason": err.Error()})
				continue
			}
			moved = append(moved, map[string]string{"id": id, "from": f.Path, "to": path})
		}
		websession.Respond(w, 200, map[string]any{"moved": moved, "failed": failed, "warnings": warnings})
	}))
}
