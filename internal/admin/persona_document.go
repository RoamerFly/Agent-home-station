package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"wechat-bot/internal/companion"
)

const personaDocumentName = "PERSONA.md"

func (m *Manager) personaDocumentPath() string {
	return filepath.Join(m.managedRoot(), personaDocumentName)
}

func validPersonaDocument(content string) bool {
	return strings.TrimSpace(content) != "" && len(content) <= 32000 && utf8.ValidString(content) && !strings.ContainsRune(content, 0)
}

func personaRevision(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func (m *Manager) readPersonaDocument() (string, string, error) {
	path := m.personaDocumentPath()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 32000 {
		return "", "", errors.New("角色文档不可读")
	}
	data, err := os.ReadFile(path)
	if err != nil || !validPersonaDocument(string(data)) {
		return "", "", errors.New("角色文档无效")
	}
	content := string(data)
	return content, personaRevision(content), nil
}

func (m *Manager) ensurePersonaDocument(c companion.Config) error {
	path := m.personaDocumentPath()
	if _, err := os.Lstat(path); err == nil {
		_, _, err = m.readPersonaDocument()
		return err
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	content := "# " + c.Name + "\n\n## 人物与行为\n" + c.Persona + "\n\n## 外观\n" + c.Appearance + "\n\n## 说话方式\n" + c.Style + "\n\n## 相处方式\n" + c.Relationship + "\n"
	if !validPersonaDocument(content) {
		return errors.New("原角色配置无法迁移为 Markdown")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return writePrivate(path, []byte(content))
}

func (m *Manager) withPersonaDocument(c Config) (Config, error) {
	content, _, err := m.readPersonaDocument()
	if err != nil {
		return Config{}, err
	}
	c.Companion.Persona = content
	c.Companion.PersonaDocument = true
	return c, nil
}

func (s *Server) getPersonaDocument(w http.ResponseWriter, _ *http.Request) {
	content, revision, err := s.manager.readPersonaDocument()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	respond(w, 200, map[string]any{"content": content, "revision": revision, "path": s.manager.personaDocumentPath()})
}

func (s *Server) savePersonaDocument(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Content  string `json:"content"`
		Revision string `json:"revision"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !validPersonaDocument(input.Content) {
		fail(w, 400, "角色文档不能为空、超过 32 KB 或包含无效字符")
		return
	}
	m := s.manager
	m.op.Lock()
	defer m.op.Unlock()
	_, current, err := m.readPersonaDocument()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if input.Revision != current {
		fail(w, 409, "角色文档已在其他位置更新，请刷新后再保存")
		return
	}
	m.mu.Lock()
	previous := cloneConfig(m.config)
	m.mu.Unlock()
	next := cloneConfig(previous)
	next.Companion.Persona = input.Content
	next.Companion.PersonaDocument = true
	if err := validate(next); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := m.stop(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	if err := writePrivate(m.personaDocumentPath(), []byte(input.Content)); err != nil {
		if previous.Enabled {
			m.start(previous)
		}
		fail(w, 500, "角色文档保存失败："+err.Error())
		return
	}
	m.mu.Lock()
	m.config = next
	m.mu.Unlock()
	m.log("persona_document_saved")
	if next.Enabled {
		m.start(next)
	}
	respond(w, 200, map[string]any{"content": input.Content, "revision": personaRevision(input.Content), "path": m.personaDocumentPath()})
}
