package station

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"wechat-bot/internal/hostfile"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

const sharedPersonaDocumentName = "PERSONA.md"
const sharedPersonaDocumentLimit = 32000

func sharedPersonaDocumentPath(dataDir string) string {
	return filepath.Join(dataDir, "workspaces", "home", sharedPersonaDocumentName)
}

func validSharedPersonaDocument(content string) bool {
	return strings.TrimSpace(content) != "" && len(content) <= sharedPersonaDocumentLimit && utf8.ValidString(content) && !strings.ContainsRune(content, 0)
}

func sharedPersonaRevision(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// A document that already exists is never regenerated from legacy settings.
// This also refuses links and malformed files so a startup cannot silently
// replace an owner's edited Markdown with a default.
func readSharedPersonaDocument(path string) (string, string, error) {
	root, err := workspacefs.OpenPath(filepath.Dir(path))
	if err != nil {
		return "", "", err
	}
	defer root.Close()
	info, err := root.Lstat(sharedPersonaDocumentName)
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() || info.Size() > sharedPersonaDocumentLimit {
		return "", "", errors.New("角色文档不是有效的普通文件")
	}
	file, err := workspacefs.OpenRegular(root, sharedPersonaDocumentName)
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, sharedPersonaDocumentLimit+1))
	if err != nil || !validSharedPersonaDocument(string(raw)) {
		return "", "", errors.New("角色文档内容无效")
	}
	content := string(raw)
	return content, sharedPersonaRevision(content), nil
}

// Copy the current WeChat Markdown into the workstation's shared home only
// when the destination is missing. The source remains in place for rollback.
// An older local-model persona is used only when no Markdown source exists.
func ensureSharedPersonaDocument(dataDir string) error {
	path := sharedPersonaDocumentPath(dataDir)
	root, err := workspacefs.EnsurePath(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	release, err := hostfile.Lock(path + ".lock")
	if err != nil {
		return err
	}
	defer release()
	if _, _, err = readSharedPersonaDocument(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, source := range legacyPersonaDocumentPaths(dataDir) {
		content, _, readErr := readSharedPersonaDocument(source)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return readErr
		}
		return createSharedPersonaDocument(root, content)
	}
	model, err := newLocalModelStore(stationAuthorityDir(dataDir)).View()
	if err != nil {
		return err
	}
	return createSharedPersonaDocument(root, model.Persona)
}

func legacyPersonaDocumentPaths(dataDir string) []string {
	return []string{
		filepath.Join(dataDir, "applications", "wechat", "legacy-admin", "managed-workspaces", sharedPersonaDocumentName),
		filepath.Join(dataDir, "apps", "wechat", "legacy-admin", "managed-workspaces", sharedPersonaDocumentName),
		filepath.Join(dataDir, "workspace", "home", sharedPersonaDocumentName),
		filepath.Join(dataDir, "managed-workspaces", "home", sharedPersonaDocumentName),
		filepath.Join(dataDir, "managed-workspaces", sharedPersonaDocumentName),
		filepath.Join(dataDir, "workspaces", sharedPersonaDocumentName),
	}
}

func createSharedPersonaDocument(root *os.Root, content string) error {
	if !validSharedPersonaDocument(content) {
		return errors.New("原角色配置无法迁移为 Markdown")
	}
	file, err := root.OpenFile(sharedPersonaDocumentName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	written, err := io.WriteString(file, content)
	if err == nil && written != len(content) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		_ = root.Remove(sharedPersonaDocumentName)
		return errors.New("角色文档迁移写入失败")
	}
	return nil
}

func (h *localChatService) personaDocumentRoutes(s *websession.Store, mux *http.ServeMux) {
	mux.HandleFunc("GET /api/persona-document", s.Protect(h.personaDocumentView))
	mux.HandleFunc("POST /api/persona-document", s.Protect(h.personaDocumentSave))
}

func (h *localChatService) personaDocumentView(w http.ResponseWriter, _ *http.Request) {
	path := sharedPersonaDocumentPath(h.home.dataDir)
	content, revision, err := readSharedPersonaDocument(path)
	if err != nil {
		websession.Fail(w, http.StatusConflict, "主角色文档不可读，原文件保留")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	websession.Respond(w, http.StatusOK, map[string]any{"content": content, "revision": revision, "path": path})
}

func (h *localChatService) personaDocumentSave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var input struct {
		Content  string `json:"content"`
		Revision string `json:"revision"`
	}
	if !websession.Decode(w, r, &input) {
		return
	}
	if !validSharedPersonaDocument(input.Content) {
		websession.Fail(w, http.StatusBadRequest, "主角色文档不能为空、超过 32 KB 或包含无效字符")
		return
	}
	path := sharedPersonaDocumentPath(h.home.dataDir)
	release, err := hostfile.Lock(path + ".lock")
	if err != nil {
		websession.Fail(w, http.StatusConflict, "主角色文档暂不可写")
		return
	}
	defer release()
	_, revision, err := readSharedPersonaDocument(path)
	if err != nil {
		websession.Fail(w, http.StatusConflict, "主角色文档不可读，原文件保留")
		return
	}
	if revision != input.Revision {
		websession.Fail(w, http.StatusConflict, "主角色文档已在其他位置更新，请刷新后再保存")
		return
	}
	if err := hostfile.WritePrivate(path, []byte(input.Content)); err != nil {
		websession.Fail(w, http.StatusInternalServerError, "主角色文档保存失败")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	websession.Respond(w, http.StatusOK, map[string]any{"content": input.Content, "revision": sharedPersonaRevision(input.Content), "path": path})
}
