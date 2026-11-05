package admin

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
	"wechat-bot/internal/workspacefs"
)

// The control-owned marker separates a stable account identity from a readable
// physical folder name. Nickname changes never silently rename a live account.
func validWorkspaceDirectory(name string) bool {
	if name == "" || name == "." || name == ".." || utf8.RuneCountInString(name) > 100 || strings.ContainsAny(name, `/\:`) || strings.TrimSpace(name) != name || strings.HasSuffix(name, ".") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || strings.ContainsRune(`<>"|?*`, r) {
			return false
		}
	}
	return true
}

func readableWorkspaceDirectory(id string) string {
	name := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, id)
	name = string([]rune(name)[:min(50, len([]rune(name)))])
	if name == "" {
		name = "wechat-account"
	}
	return name + "--" + accountDirectory(id)[:8]
}

func (m *Manager) configureWorkspaceDirectory() error {
	root, err := os.OpenRoot(m.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := workspacefs.OpenRegular(root, "workspace-directory")
	if err == nil {
		data, readErr := io.ReadAll(io.LimitReader(f, 1025))
		f.Close()
		if readErr != nil || len(data) > 1024 || !utf8.Valid(data) {
			return errors.New("account_workspace_directory_marker_invalid")
		}
		m.workspaceDir = strings.TrimSpace(string(data))
	} else if os.IsNotExist(err) {
		// Existing installations keep their old path until an explicit verified
		// migration. Fresh accounts get a readable stable name immediately.
		legacy := filepath.Join(m.managedRoot(), "pri_workspace", accountDirectory(m.native.Account()))
		if _, err := os.Lstat(legacy); err == nil {
			m.workspaceDir = accountDirectory(m.native.Account())
		} else if os.IsNotExist(err) {
			m.workspaceDir = readableWorkspaceDirectory(m.native.Account())
		} else {
			return err
		}
		if err = writePrivate(filepath.Join(m.dir, "workspace-directory"), []byte(m.workspaceDir+"\n")); err != nil {
			return err
		}
	} else {
		return err
	}
	if !validWorkspaceDirectory(m.workspaceDir) {
		return errors.New("account_workspace_directory_marker_invalid")
	}
	return nil
}

func (m *Manager) privateWorkspaceFor(account string) string {
	name := accountDirectory(account)
	if account == m.native.Account() && m.workspaceDir != "" {
		name = m.workspaceDir
	}
	return filepath.Join(m.managedRoot(), "pri_workspace", name)
}

func (m *Manager) privateVoiceLibrary() string {
	current := filepath.Join(m.privateWorkspaceFor(m.native.Account()), "voice-library")
	if _, err := os.Lstat(filepath.Join(current, "catalog.json")); os.IsNotExist(err) {
		legacy := filepath.Join(m.dir, "voice-library")
		if _, err := os.Lstat(filepath.Join(legacy, "catalog.json")); err == nil {
			return legacy // explicit migration preserves legacy installations
		}
	}
	return current
}

func (m *Manager) writeWorkspaceIdentity() error {
	root, err := os.OpenRoot(m.privateWorkspaceFor(m.native.Account()))
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := workspacefs.OpenRegular(root, "account.json")
	if err == nil {
		var identity struct {
			Account   string `json:"account_id"`
			Scope     string `json:"scope"`
			Directory string `json:"directory"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(f, 8192)).Decode(&identity)
		f.Close() // Windows cannot atomically replace an open destination.
		if decodeErr != nil || identity.Account != m.native.Account() || identity.Scope != accountDirectory(m.native.Account()) {
			return errors.New("private_workspace_belongs_to_another_account_or_identity_invalid")
		}
		if identity.Directory == m.workspaceDir {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	data, _ := json.MarshalIndent(map[string]any{"version": 1, "account_id": m.native.Account(), "scope": accountDirectory(m.native.Account()), "directory": m.workspaceDir}, "", "  ")
	return writePrivate(filepath.Join(m.privateWorkspaceFor(m.native.Account()), "account.json"), data)
}
