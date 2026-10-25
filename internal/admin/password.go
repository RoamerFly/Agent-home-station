package admin

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

func validPassword(value string) bool {
	return len(value) >= 5 && len(value) <= 128 && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

// Password keeps existing credentials unless the owner changes them. A web-
// managed password overrides the bootstrap token and optional environment seed.
func Password(dir, explicit string) (string, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", errors.New("admin directory unavailable")
	}
	for _, name := range []string{"admin-password", "admin-token"} {
		if name == "admin-token" && explicit != "" {
			if !validPassword(explicit) {
				return "", errors.New("admin password must be 5 to 128 bytes")
			}
			return explicit, nil
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || len(data) > 1024 {
			return "", errors.New("admin credential unavailable")
		}
		value := strings.TrimSpace(string(data))
		if !validPassword(value) {
			return "", errors.New("admin credential invalid")
		}
		return value, nil
	}
	if err := writePrivate(filepath.Join(dir, "admin-token"), []byte("admin\n")); err != nil {
		return "", err
	}
	return "admin", nil
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !validPassword(input.New) {
		fail(w, 400, "新密码应为5至128字节，首尾不能有空白或换行")
		return
	}
	cookie, err := r.Cookie("wechat_admin_session")
	if err != nil {
		fail(w, 401, "请重新登录")
		return
	}
	oldHash, newHash := sha256.Sum256([]byte(input.Current)), sha256.Sum256([]byte(input.New))
	s.mu.Lock()
	current, exists := s.sessions[cookie.Value]
	if !exists || time.Now().After(current.Expires) {
		s.mu.Unlock()
		fail(w, 401, "会话已失效，请重新登录")
		return
	}
	if subtle.ConstantTimeCompare(oldHash[:], s.passwordHash[:]) != 1 {
		s.mu.Unlock()
		fail(w, 400, "当前管理密码不正确")
		return
	}
	if err = writePrivate(filepath.Join(s.manager.dir, "admin-password"), []byte(input.New+"\n")); err != nil {
		s.mu.Unlock()
		fail(w, 500, "新密码未能保存，原密码仍然有效")
		return
	}
	s.passwordHash = newHash
	s.sessions = map[string]session{}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "wechat_admin_session", Path: "/", Value: "", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	s.manager.log("admin_password_changed")
	respond(w, 200, map[string]bool{"ok": true, "reauthenticate": true})
}
