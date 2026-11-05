package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/workspacefs"
)

type localArtifactStore struct {
	home *localHomeService
	chat *localChatService
}

type localArtifact struct {
	ID, Name, SHA256, Fish, Owner, Request string
	PathHash                               string
	Bytes                                  int64
	HomeRevision                           uint64
	Source                                 *companion.SourceRef `json:",omitempty"`
}

type localArtifactInput struct {
	Path     string `json:"path"`
	Expected string `json:"expected_sha256"`
	Key      string `json:"request_key"`
}

func (a localArtifact) id() string {
	return "work-" + localCheckpointKey(a.Fish, a.Owner, a.HomeRevision, a.Request, a.Source, a.Name, a.PathHash)
}

func artifactID(id, prefix string) bool {
	value := strings.TrimPrefix(id, prefix)
	bytes, err := hex.DecodeString(value)
	return strings.HasPrefix(id, prefix) && len(bytes) == sha256.Size && err == nil && strings.ToLower(id) == id
}

func (a localArtifact) view() map[string]any {
	return map[string]any{"artifact_id": a.ID, "filename": a.Name, "sha256": a.SHA256, "bytes": a.Bytes, "status": "registered"}
}

func (s *localArtifactStore) sourceValid(a localArtifact) bool {
	if a.Source == nil {
		return true
	}
	store, in, err := s.chat.conversation()
	if err != nil || in.Account != a.Fish || in.Sender != a.Owner {
		return false
	}
	in.TraceID, in.Source = a.Request, a.Source
	in.MessageTime = a.Source.OccurredAt
	_, ok := store.SourceForInput(in)
	return ok
}

func (s *localArtifactStore) access(a localArtifact) (*localWorkspaceAccess, bool) {
	access := &localWorkspaceAccess{home: s.home, fishID: a.Fish, ownerID: a.Owner}
	grant, err := access.resolve()
	return access, err == nil && grant.FishID == a.Fish && grant.OwnerID == a.Owner && s.sourceValid(a)
}

func (s *localArtifactStore) read(id string) (localArtifact, error) {
	var a localArtifact
	if !artifactID(id, "work-") {
		return a, errors.New("invalid_artifact_id")
	}
	root, err := workspacefs.OpenPath(filepath.Join(s.home.dataDir, "artifacts", id))
	if err != nil {
		return a, err
	}
	defer root.Close()
	file, err := workspacefs.OpenRegular(root, "record.json")
	if err != nil {
		return a, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 32<<10))
	if err != nil || len(raw) >= 32<<10 || json.Unmarshal(raw, &a) != nil || a.ID != id || a.id() != id || a.Bytes < 1 || a.Bytes > stationAttachmentLimit || !safeMediaFilename(a.Name) {
		return localArtifact{}, errors.New("artifact_record_invalid")
	}
	if _, ok := s.access(a); !ok {
		return localArtifact{}, errors.New("artifact_source_or_home_changed")
	}
	return a, nil
}

func (s *localArtifactStore) open(ctx context.Context, a localArtifact, allowed func() bool) (*os.File, error) {
	root, err := workspacefs.OpenPath(filepath.Join(s.home.dataDir, "artifacts", a.ID))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := workspacefs.OpenRegular(root, "original")
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	n, err := io.Copy(attachmentGrantWriter{allow: func() bool { return ctx.Err() == nil && allowed() }, out: hash}, io.LimitReader(file, a.Bytes+1))
	if err != nil || n != a.Bytes || hex.EncodeToString(hash.Sum(nil)) != a.SHA256 || !allowed() {
		file.Close()
		return nil, errors.New("artifact_integrity_or_authorization_changed")
	}
	_, err = file.Seek(0, io.SeekStart)
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func (s *localArtifactStore) publish(ctx context.Context, in localArtifactInput, source *companion.SourceRef) (localArtifact, error) {
	access := &localWorkspaceAccess{home: s.home}
	grant, err := access.resolve()
	if err != nil {
		return localArtifact{}, err
	}
	if !validID(in.Key) || in.Expected != "" && !artifactID("sha-"+in.Expected, "sha-") {
		return localArtifact{}, errors.New("需要请求编号和文件的 SHA-256")
	}
	path, err := localWorkspacePath(grant, in.Path)
	if err != nil {
		return localArtifact{}, err
	}
	return s.publishFile(ctx, in, source, access, grant, path, nil)
}

// publishFile copies an opened regular file into the same immutable artifact
// store for both home files and host-validated project tool outputs.
func (s *localArtifactStore) publishFile(ctx context.Context, in localArtifactInput, source *companion.SourceRef, access *localWorkspaceAccess, grant localWorkspaceGrant, path string, validate func(*os.File, int64) error) (localArtifact, error) {
	name := filepath.Base(path)
	if !safeMediaFilename(name) || len(name) > 255 || strings.ContainsAny(name, ":*?<>|") {
		return localArtifact{}, errors.New("成品文件名无效")
	}
	a := localArtifact{Name: name, PathHash: localCheckpointKey(path), Fish: grant.FishID, Owner: grant.OwnerID, HomeRevision: grant.Revision, Request: in.Key, Source: source}
	a.ID = a.id()
	check := func() bool { return ctx.Err() == nil && access.current(grant) && s.sourceValid(a) }
	if cached, err := s.read(a.ID); err == nil {
		if in.Expected != "" && cached.SHA256 != in.Expected {
			return localArtifact{}, errors.New("相同请求的成品内容已变更，请使用新请求登记")
		}
		file, err := s.open(ctx, cached, check)
		if err != nil {
			return localArtifact{}, err
		}
		file.Close()
		return cached, nil
	} else if !os.IsNotExist(err) {
		return localArtifact{}, err
	}
	parent, err := workspacefs.OpenPath(filepath.Dir(path))
	if err != nil {
		return localArtifact{}, err
	}
	defer parent.Close()
	original, err := workspacefs.OpenRegular(parent, name)
	if err != nil {
		return localArtifact{}, err
	}
	defer original.Close()
	info, err := original.Stat()
	if err != nil || info.Size() < 1 || info.Size() > stationAttachmentLimit {
		return localArtifact{}, errors.New("成品大小须在 1 字节至 512 MiB 内")
	}
	if validate != nil {
		if err := validate(original, info.Size()); err != nil {
			return localArtifact{}, err
		}
	}
	a.Bytes = info.Size()
	if !check() {
		return localArtifact{}, errors.New("成品来源已变更")
	}
	root, err := workspacefs.EnsurePath(filepath.Join(s.home.dataDir, "artifacts"))
	if err != nil {
		return localArtifact{}, err
	}
	defer root.Close()
	stage := ".publish-" + randomToken()
	if err := root.Mkdir(stage, 0700); err != nil {
		return localArtifact{}, err
	}
	defer func() {
		_ = root.Remove(filepath.Join(stage, "original"))
		_ = root.Remove(filepath.Join(stage, "record.json"))
		_ = root.Remove(stage)
	}()
	file, err := root.OpenFile(filepath.Join(stage, "original"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return localArtifact{}, err
	}
	hash := sha256.New()
	n, err := io.Copy(attachmentGrantWriter{allow: func() bool { return ctx.Err() == nil }, out: io.MultiWriter(file, hash)}, io.LimitReader(original, a.Bytes+1))
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	digest := hex.EncodeToString(hash.Sum(nil))
	after, statErr := original.Stat()
	if err != nil || statErr != nil || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) || n != a.Bytes || in.Expected != "" && digest != in.Expected || !check() {
		return localArtifact{}, errors.New("成品快照未通过内容或来源校验")
	}
	a.SHA256 = digest
	a.ID = a.id()
	if err := writeLegacyStagedFile(root, filepath.Join(stage, "record.json"), a); err != nil {
		return localArtifact{}, err
	}
	if err := access.withCurrentWriteGrant(grant, func() error { return root.Rename(stage, a.ID) }); err != nil {
		cached, priorErr := s.read(a.ID)
		if priorErr != nil || cached.SHA256 != a.SHA256 {
			return localArtifact{}, err
		}
		file, checkErr := s.open(ctx, cached, check)
		if checkErr != nil {
			return localArtifact{}, checkErr
		}
		file.Close()
	}
	if !check() {
		return localArtifact{}, errors.New("成品来源已变更")
	}
	return a, nil
}

func (s *localArtifactStore) routes(owner *Server, mux *http.ServeMux) {
	mux.HandleFunc("POST /api/local/artifacts", owner.protected(func(w http.ResponseWriter, r *http.Request) {
		var in localArtifactInput
		if !decode(w, r, &in) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		a, err := s.publish(ctx, in, nil)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		respond(w, 200, a.view())
	}))
	mux.HandleFunc("GET /api/local/artifacts/{artifact}", owner.protected(func(w http.ResponseWriter, r *http.Request) {
		a, err := s.read(r.PathValue("artifact"))
		if err != nil {
			fail(w, 404, "成品不存在或来源已变化")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		respond(w, 200, a.view())
	}))
	mux.HandleFunc("GET /api/local/artifacts/{artifact}/download", owner.protected(func(w http.ResponseWriter, r *http.Request) {
		a, err := s.read(r.PathValue("artifact"))
		if err != nil {
			fail(w, 404, "成品不存在或来源已变化")
			return
		}
		file, err := s.open(r.Context(), a, func() bool { _, allowed := s.access(a); return allowed })
		if err != nil {
			fail(w, 404, "成品不可读取或完整性校验未通过")
			return
		}
		defer file.Close()
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": a.Name}))
		http.ServeContent(w, r, a.Name, time.Time{}, file)
	}))
}
