package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	appcatalog "wechat-bot/connected-apps"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/workspacefs"
)

func (h *localHomeService) configureAppCatalog() {
	if executable, err := os.Executable(); err == nil {
		h.appCatalog = filepath.Join(filepath.Dir(executable), "connected-apps")
	}
}

func (h *localHomeService) appInstallRoutes(s *Server, mux *http.ServeMux) {
	mux.HandleFunc("GET /api/local/apps/catalog", s.protected(func(w http.ResponseWriter, r *http.Request) {
		contract, err := appcatalog.Lookup("wechat")
		if err != nil {
			fail(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		_, _, err = h.verifyAppSource("wechat")
		respond(w, http.StatusOK, map[string]any{"applications": []any{map[string]any{
			"id": "wechat", "name": contract.Package.Name, "version": contract.Package.Version,
			"digest": contract.Digest, "source_available": err == nil, "state": "source_only",
		}}})
	}))
	mux.HandleFunc("POST /api/local/apps/{app}/install", s.protected(h.installAppSource))
}

func appDescriptorDigest(root *os.Root) (string, error) {
	file, err := workspacefs.OpenRegular(root, "app.json")
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return "", errors.New("应用发行声明不可读")
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func (h *localHomeService) verifyAppSource(id string) (appcatalog.Contract, string, error) {
	contract, err := appcatalog.Lookup(id)
	if err != nil {
		return contract, "", err
	}
	directory := filepath.Join(h.appCatalog, id)
	if h.appCatalog == "" {
		return contract, "", errors.New("本机未提供应用发行源")
	}
	if _, err := pluginhost.VerifyDescriptor(directory, "app.json"); err != nil {
		return contract, "", errors.New("应用发行源未通过完整性校验")
	}
	root, err := workspacefs.EnsurePath(directory)
	if err != nil {
		return contract, "", err
	}
	defer root.Close()
	digest, err := appDescriptorDigest(root)
	if err != nil || digest != contract.Digest {
		return contract, "", errors.New("应用发行源与当前主程序版本不匹配")
	}
	return contract, directory, nil
}

// Installation prepares editable application source only. Account connection,
// credentials and runtime permissions stay in their existing host registries.
func (h *localHomeService) installAppSource(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Digest   string `json:"digest"`
		Revision uint64 `json:"revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	h.appInstallMu.Lock()
	defer h.appInstallMu.Unlock()
	record, err := h.identity.read()
	if err != nil || record.LocalOwner == nil || !record.LocalOwner.HomeAccess || record.LocalOwner.Revision != in.Revision {
		fail(w, http.StatusConflict, errLocalHomeRevisionConflict.Error())
		return
	}
	id := r.PathValue("app")
	contract, sourcePath, err := h.verifyAppSource(id)
	if err != nil || contract.Digest != in.Digest {
		fail(w, http.StatusConflict, "请刷新应用发行目录后重试")
		return
	}
	homePath, err := h.directory(record)
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	home, err := workspacefs.EnsurePath(homePath)
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	defer home.Close()
	parent, err := workspacefs.EnsureDirectory(home, "Connected Apps")
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	defer parent.Close()
	result := func(state string) {
		respond(w, http.StatusOK, map[string]any{"id": id, "digest": contract.Digest, "result": state,
			"state": "source_only", "connected": false, "directory": filepath.Join(homePath, "Connected Apps", id)})
	}
	if exists, err := checkoutExistingPluginDirectory(parent, id); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	} else if exists {
		existing, err := parent.OpenRoot(id)
		if err != nil {
			fail(w, http.StatusConflict, err.Error())
			return
		}
		digest, err := appDescriptorDigest(existing)
		_ = existing.Close()
		if err != nil || digest != contract.Digest {
			fail(w, http.StatusConflict, "应用目录已存在不同版本，请先保留并整理已有修改")
			return
		}
		result("existing")
		return
	}
	source, err := os.OpenRoot(sourcePath)
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	defer source.Close()
	stageName, stage, err := newCheckoutStage(parent)
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	defer func() {
		_ = stage.Close()
		if stageName != "" {
			_ = parent.RemoveAll(stageName)
		}
	}()
	total := int64(0)
	err = copyCheckedPluginFile(r.Context(), source, stage, "app.json", contract.Digest, &total)
	names := make([]string, 0, len(contract.Package.Files))
	for name := range contract.Package.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err != nil {
			break
		}
		err = copyCheckedPluginFile(r.Context(), source, stage, name, contract.Package.Files[name], &total)
	}
	if err == nil {
		err = stage.Close()
	}
	if err != nil {
		fail(w, http.StatusConflict, "应用源码复制未完成，已有目录保持原样")
		return
	}
	// Keep revocation and publishing in the same identity critical section.
	h.identity.mu.Lock()
	defer h.identity.mu.Unlock()
	release, err := lockFishIdentityRegistry(h.identity.path + ".lock")
	if err != nil {
		fail(w, http.StatusConflict, errLocalHomeRevisionConflict.Error())
		return
	}
	defer release()
	current, err := h.identity.readHeld()
	if err != nil || current.FishID != record.FishID || current.LocalOwner == nil || *current.LocalOwner != *record.LocalOwner {
		fail(w, http.StatusConflict, errLocalHomeRevisionConflict.Error())
		return
	}
	if err := parent.Rename(stageName, id); err != nil {
		fail(w, http.StatusConflict, "应用目录发布失败，请检查现有目录后重试")
		return
	}
	stageName = ""
	result("created")
}
