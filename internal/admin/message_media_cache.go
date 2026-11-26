package admin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const messageObservationVersion = 2

type messageObservation struct {
	Version  int       `json:"version"`
	Kind     string    `json:"kind"`
	SHA256   string    `json:"sha256"`
	Coverage string    `json:"coverage,omitempty"`
	Text     string    `json:"text"`
	Updated  time.Time `json:"updated"`
}

// Message originals and content-only observations belong to the fish's WeChat
// workspace. A digest never conveys a sender or a conversation permission.
func (m *Manager) messageMediaRoot() string {
	return filepath.Join(m.privateWorkspaceFor(m.native.Account()), "messages")
}

func validMessageDigest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func messageMediaKind(kind string) bool {
	return kind == "stickers" || kind == "files" || kind == "images" || kind == "voices"
}

func (m *Manager) storeMessageMedia(kind, digest, ext string, src io.Reader, size int64) (string, error) {
	if !messageMediaKind(kind) || !validMessageDigest(digest) || size <= 0 || size > 512<<20 || len(ext) > 16 || strings.ContainsAny(ext, `/\:`) || (ext != "" && ext[0] != '.') {
		return "", errors.New("message_media_identity_invalid")
	}
	dir := filepath.Join(m.messageMediaRoot(), kind)
	if err := ensureManaged(dir); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	name := digest + strings.ToLower(ext)
	if err := publishVerifiedStream(root, name, src, size, digest); err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func (m *Manager) storeMessageMediaBytes(kind, ext string, data []byte) (string, string, error) {
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	path, err := m.storeMessageMedia(kind, digest, ext, bytes.NewReader(data), int64(len(data)))
	return path, digest, err
}

func (m *Manager) readMessageObservation(kind, digest string) (messageObservation, bool) {
	var value messageObservation
	if !messageMediaKind(kind) || !validMessageDigest(digest) {
		return value, false
	}
	path := filepath.Join(m.messageMediaRoot(), "observations", kind+"-"+digest+".json")
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 16<<10 || json.Unmarshal(data, &value) != nil || value.Version != messageObservationVersion || value.Kind != kind || value.SHA256 != digest || strings.TrimSpace(value.Text) == "" {
		return messageObservation{}, false
	}
	return value, true
}

func (m *Manager) saveMessageObservation(kind, digest, coverage, text string) error {
	if !messageMediaKind(kind) || !validMessageDigest(digest) || strings.TrimSpace(text) == "" || len(text)+len(coverage) > 12<<10 {
		return errors.New("message_observation_invalid")
	}
	dir := filepath.Join(m.messageMediaRoot(), "observations")
	if err := ensureManaged(dir); err != nil {
		return err
	}
	value := messageObservation{Version: messageObservationVersion, Kind: kind, SHA256: digest, Coverage: coverage, Text: text, Updated: time.Now().UTC()}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".observation-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(data); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temp.Name(), filepath.Join(dir, kind+"-"+digest+".json"))
}

func messageTextFile(path, mimeType string, size int64) (string, string, bool) {
	if size <= 0 || size > 128<<10 || !(strings.HasPrefix(mimeType, "text/") || strings.Contains(mimeType, "json") || strings.Contains(mimeType, "xml") || strings.Contains(mimeType, "csv")) {
		return "", "", false
	}
	data, err := os.ReadFile(path)
	if err != nil || int64(len(data)) != size || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", "", false
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	coverage := "完整文本"
	if len(data) > 10<<10 {
		data = data[:10<<10]
		for !utf8.Valid(data) {
			data = data[:len(data)-1]
		}
		coverage = "文本开头摘录；原文件可继续读取"
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", "", false
	}
	return coverage, string(data), true
}

func messageInspectionKey(digest, mimeType, name, packageDigest string, arguments json.RawMessage) (string, bool) {
	if !validMessageDigest(digest) || !validMessageDigest(packageDigest) || !json.Valid(arguments) {
		return "", false
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(arguments, &values) != nil {
		return "", false
	}
	delete(values, "artifact")
	parameters, err := json.Marshal(values)
	if err != nil {
		return "", false
	}
	hash := sha256.Sum256(bytes.Join([][]byte{[]byte(digest), []byte(mimeType), []byte(filepath.Ext(name)), []byte(packageDigest), parameters}, []byte{0}))
	return hex.EncodeToString(hash[:]), true
}

func (m *Manager) readMessageInspection(digest, key string) (json.RawMessage, bool) {
	if !validMessageDigest(digest) || !validMessageDigest(key) {
		return nil, false
	}
	data, err := os.ReadFile(filepath.Join(m.messageMediaRoot(), "inspections", digest, key+".json"))
	if err != nil || len(data) == 0 || len(data) > 1<<20 || !json.Valid(data) {
		return nil, false
	}
	return data, true
}

func (m *Manager) hasMessageInspection(digest string) bool {
	if !validMessageDigest(digest) {
		return false
	}
	entries, err := os.ReadDir(filepath.Join(m.messageMediaRoot(), "inspections", digest))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			return true
		}
	}
	return false
}

func (m *Manager) saveMessageInspection(digest, key string, result json.RawMessage) error {
	if !validMessageDigest(digest) || !validMessageDigest(key) || len(result) == 0 || len(result) > 1<<20 || !json.Valid(result) {
		return errors.New("message_inspection_invalid")
	}
	dir := filepath.Join(m.messageMediaRoot(), "inspections", digest)
	if err := ensureManaged(dir); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".inspection-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(result); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temp.Name(), filepath.Join(dir, key+".json"))
}
