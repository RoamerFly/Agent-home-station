package station

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"wechat-bot/internal/identity"
	"wechat-bot/internal/mediaspec"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

type localWorkTrashRecord struct {
	Application string `json:"application"`
	Original    string `json:"original"`
	Name        string `json:"name"`
	Fish        string `json:"fish"`
	Owner       string `json:"owner"`
	Revision    uint64 `json:"revision"`
	MovedAt     string `json:"moved_at"`
}

func validWorkName(name string) bool {
	if !mediaspec.SafeFilename(name) || len(name) > 255 || strings.ContainsAny(name, ":*?<>|") ||
		strings.TrimRight(name, " .") != name || hiddenWorkName(name) {
		return false
	}
	base := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	return !slices.Contains([]string{"CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9"}, base)
}

func (s *localArtifactStore) editableWork(ref localWorkRef, grant localWorkspaceGrant) (*os.Root, os.FileInfo, error) {
	if ref.kind != "file" || !slices.Contains(s.workApplications(), ref.app) {
		return nil, nil, os.ErrNotExist
	}
	visible, manage := classifiedWork(ref.app, ref.rel)
	if !visible || !manage {
		return nil, nil, errors.New("此作品只支持查看，不能移动或重命名")
	}
	root, err := workspacefs.OpenPath(filepath.Join(s.home.root, ref.app))
	if err != nil {
		return nil, nil, err
	}
	parent, err := workspacefs.OpenExistingDirectory(root, filepath.FromSlash(path.Dir(ref.rel)))
	if err != nil {
		root.Close()
		return nil, nil, err
	}
	info, err := parent.Lstat(path.Base(ref.rel))
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		parent.Close()
		root.Close()
		return nil, nil, os.ErrNotExist
	}
	file, err := workspacefs.OpenRegular(parent, path.Base(ref.rel))
	if err != nil {
		parent.Close()
		root.Close()
		return nil, nil, err
	}
	opened, err := file.Stat()
	file.Close()
	parent.Close()
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, nil, os.ErrNotExist
	}
	return root, info, nil
}

func (s *localArtifactStore) renameWork(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name            string `json:"name"`
		ExpectedVersion string `json:"expected_version"`
	}
	if !websession.Decode(w, r, &in) {
		return
	}
	if !validWorkName(in.Name) || in.ExpectedVersion == "" {
		websession.Fail(w, 400, "请提供有效的新文件名和当前版本")
		return
	}
	ref, ok := parseWorkID(r.PathValue("id"))
	if !ok || ref.kind != "file" {
		websession.Fail(w, 404, "作品不存在")
		return
	}
	access := &localWorkspaceAccess{home: s.home}
	grant, err := access.resolve()
	if err != nil {
		websession.Fail(w, 403, "家目录授权不可用")
		return
	}
	s.worksMu.Lock()
	defer s.worksMu.Unlock()
	var item localWorkItem
	err = access.withCurrentWriteGrant(grant, func() error {
		root, info, e := s.editableWork(ref, grant)
		if e != nil {
			return e
		}
		defer root.Close()
		if workFileVersion(ref.app, ref.rel, info) != in.ExpectedVersion {
			return errors.New("作品已变化，请刷新后重试")
		}
		newRel := path.Join(path.Dir(ref.rel), in.Name)
		if newRel == ref.rel {
			item = workFileItem(ref.app, ref.rel, info)
			return nil
		}
		if _, e := root.Lstat(filepath.FromSlash(newRel)); e == nil {
			return errors.New("目标文件名已存在")
		} else if !os.IsNotExist(e) {
			return e
		}
		oldPath, newPath := filepath.FromSlash(ref.rel), filepath.FromSlash(newRel)
		if e := root.Link(oldPath, newPath); e != nil {
			return e
		}
		linked, e := root.Lstat(newPath)
		if e != nil || !linked.Mode().IsRegular() || !os.SameFile(info, linked) {
			_ = root.Remove(newPath)
			return errors.New("源作品在重命名时发生变化")
		}
		current, e := root.Lstat(oldPath)
		if e != nil || !os.SameFile(info, current) {
			_ = root.Remove(newPath)
			return errors.New("源作品在重命名时发生变化")
		}
		if e := root.Remove(oldPath); e != nil {
			_ = root.Remove(newPath)
			return e
		}
		item = workFileItem(ref.app, newRel, linked)
		return nil
	})
	if err != nil {
		websession.Fail(w, 409, "重命名未完成："+err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	websession.Respond(w, 200, map[string]any{"item": item})
}

func (s *localArtifactStore) trashWork(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpectedVersion string `json:"expected_version"`
	}
	if !websession.Decode(w, r, &in) {
		return
	}
	if in.ExpectedVersion == "" {
		websession.Fail(w, 400, "请提供当前作品版本")
		return
	}
	ref, ok := parseWorkID(r.PathValue("id"))
	if !ok || ref.kind != "file" {
		websession.Fail(w, 404, "作品不存在")
		return
	}
	access := &localWorkspaceAccess{home: s.home}
	grant, err := access.resolve()
	if err != nil {
		websession.Fail(w, 403, "家目录授权不可用")
		return
	}
	s.worksMu.Lock()
	defer s.worksMu.Unlock()
	var item localWorkItem
	err = access.withCurrentWriteGrant(grant, func() error {
		root, info, e := s.editableWork(ref, grant)
		if e != nil {
			return e
		}
		defer root.Close()
		if workFileVersion(ref.app, ref.rel, info) != in.ExpectedVersion {
			return errors.New("作品已变化，请刷新后重试")
		}
		trash, e := workspacefs.EnsureDirectory(root, ".works-trash")
		if e != nil {
			return e
		}
		defer trash.Close()
		token := identity.RandomToken()
		record := localWorkTrashRecord{Application: ref.app, Original: ref.rel, Name: path.Base(ref.rel), Fish: grant.FishID, Owner: grant.OwnerID, Revision: grant.Revision, MovedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")}
		data, e := json.Marshal(record)
		if e != nil {
			return e
		}
		metaPath, dataPath := ".works-trash/"+token+".json", ".works-trash/"+token+".data"
		meta, e := root.OpenFile(filepath.FromSlash(metaPath), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = meta.Write(data)
		if e == nil {
			e = meta.Sync()
		}
		e = errors.Join(e, meta.Close())
		if e != nil {
			_ = root.Remove(filepath.FromSlash(metaPath))
			return e
		}
		if e = root.Link(filepath.FromSlash(ref.rel), filepath.FromSlash(dataPath)); e != nil {
			_ = root.Remove(filepath.FromSlash(metaPath))
			return e
		}
		linked, e := root.Lstat(filepath.FromSlash(dataPath))
		if e != nil || !linked.Mode().IsRegular() || !os.SameFile(info, linked) {
			_ = root.Remove(filepath.FromSlash(dataPath))
			_ = root.Remove(filepath.FromSlash(metaPath))
			return errors.New("源作品在移动时发生变化")
		}
		current, e := root.Lstat(filepath.FromSlash(ref.rel))
		if e != nil || !os.SameFile(info, current) {
			_ = root.Remove(filepath.FromSlash(dataPath))
			_ = root.Remove(filepath.FromSlash(metaPath))
			return errors.New("源作品在移动时发生变化")
		}
		if e = root.Remove(filepath.FromSlash(ref.rel)); e != nil {
			_ = root.Remove(filepath.FromSlash(dataPath))
			_ = root.Remove(filepath.FromSlash(metaPath))
			return e
		}
		item = trashWorkItem(ref.app, token, record, linked)
		return nil
	})
	if err != nil {
		websession.Fail(w, 409, "移至待删除未完成："+err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	websession.Respond(w, 200, map[string]any{"trashed": true, "item": item})
}

func trashWorkItem(app, token string, record localWorkTrashRecord, info os.FileInfo) localWorkItem {
	id := workID("trash", app, token)
	mimeType, _ := workMIME(record.Name)
	return localWorkItem{ID: id, Application: app, Name: record.Name, Path: app + "/.works-trash/" + record.Name,
		OriginalPath: app + "/" + record.Original, Source: "trash", Bytes: info.Size(), ModifiedAt: record.MovedAt,
		MIME: mimeType, CanRestore: true, RestoreURL: "/api/local/works/" + id + "/restore"}
}

func (s *localArtifactStore) readTrash(root *os.Root, app, token string, grant localWorkspaceGrant) (localWorkTrashRecord, os.FileInfo, error) {
	var record localWorkTrashRecord
	if !artifactID("sha-"+token, "sha-") {
		return record, nil, os.ErrNotExist
	}
	file, err := workspacefs.OpenRegular(root, filepath.FromSlash(".works-trash/"+token+".json"))
	if err != nil {
		return record, nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4096))
	file.Close()
	if err != nil || len(raw) >= 4096 || json.Unmarshal(raw, &record) != nil || record.Application != app || record.Fish != grant.FishID || record.Owner != grant.OwnerID || record.Name != path.Base(record.Original) {
		return localWorkTrashRecord{}, nil, os.ErrNotExist
	}
	_, manage := classifiedWork(app, record.Original)
	if !manage {
		return localWorkTrashRecord{}, nil, os.ErrNotExist
	}
	data, err := workspacefs.OpenRegular(root, filepath.FromSlash(".works-trash/"+token+".data"))
	if err != nil {
		return record, nil, err
	}
	info, err := data.Stat()
	data.Close()
	if err != nil || !info.Mode().IsRegular() {
		return record, nil, os.ErrNotExist
	}
	return record, info, nil
}

func (s *localArtifactStore) trashedWorks(ctx context.Context, apps []string) ([]localWorkItem, error) {
	grant, err := (&localWorkspaceAccess{home: s.home}).resolve()
	if err != nil {
		return nil, err
	}
	items := []localWorkItem{}
	for _, app := range apps {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		root, err := workspacefs.OpenPath(filepath.Join(s.home.root, app))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		trash, err := workspacefs.OpenExistingDirectory(root, ".works-trash")
		if os.IsNotExist(err) {
			root.Close()
			continue
		}
		if err != nil {
			root.Close()
			return nil, err
		}
		dir, err := workspacefs.OpenDirectory(trash, ".")
		if err != nil {
			trash.Close()
			root.Close()
			return nil, err
		}
		entries, err := dir.ReadDir(-1)
		dir.Close()
		trash.Close()
		if err != nil {
			root.Close()
			return nil, err
		}
		for _, entry := range entries {
			if ctx.Err() != nil {
				root.Close()
				return nil, ctx.Err()
			}
			if !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			token := strings.TrimSuffix(entry.Name(), ".json")
			record, info, e := s.readTrash(root, app, token, grant)
			if e == nil {
				items = append(items, trashWorkItem(app, token, record, info))
			}
		}
		root.Close()
	}
	return items, nil
}

func (s *localArtifactStore) restoreWork(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if !websession.Decode(w, r, &in) {
		return
	}
	ref, ok := parseWorkID(r.PathValue("id"))
	if !ok || ref.kind != "trash" || !slices.Contains(s.workApplications(), ref.app) {
		websession.Fail(w, 404, "待删除作品不存在")
		return
	}
	access := &localWorkspaceAccess{home: s.home}
	grant, err := access.resolve()
	if err != nil {
		websession.Fail(w, 403, "家目录授权不可用")
		return
	}
	s.worksMu.Lock()
	defer s.worksMu.Unlock()
	var item localWorkItem
	err = access.withCurrentWriteGrant(grant, func() error {
		root, e := workspacefs.OpenPath(filepath.Join(s.home.root, ref.app))
		if e != nil {
			return e
		}
		defer root.Close()
		record, info, e := s.readTrash(root, ref.app, ref.rel, grant)
		if e != nil {
			return e
		}
		parent, e := workspacefs.OpenExistingDirectory(root, filepath.FromSlash(path.Dir(record.Original)))
		if e != nil {
			return e
		}
		parent.Close()
		source := ".works-trash/" + ref.rel + ".data"
		if e = root.Link(filepath.FromSlash(source), filepath.FromSlash(record.Original)); e != nil {
			return errors.New("原位置已有文件或不可写，请先处理冲突")
		}
		linked, e := root.Lstat(filepath.FromSlash(record.Original))
		if e != nil || !os.SameFile(info, linked) {
			_ = root.Remove(filepath.FromSlash(record.Original))
			return errors.New("恢复校验失败")
		}
		if e = root.Remove(filepath.FromSlash(source)); e != nil {
			_ = root.Remove(filepath.FromSlash(record.Original))
			return e
		}
		_ = root.Remove(filepath.FromSlash(".works-trash/" + ref.rel + ".json"))
		item = workFileItem(ref.app, record.Original, linked)
		return nil
	})
	if err != nil {
		websession.Fail(w, 409, "恢复未完成："+err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	websession.Respond(w, 200, map[string]any{"restored": true, "item": item})
}
