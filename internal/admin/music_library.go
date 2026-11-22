package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The resource library contains public recordings downloaded by this account.
// Conversation identifiers and request history never leave the private index.
func librarySong(a mediaAsset, account string) bool {
	return a.Account == account && a.Hash != "" && a.URL != "" && a.Music == nil && strings.HasPrefix(a.MIME, "audio/")
}

func (m *Manager) musicLibraryDirectory(account string) string {
	digest := sha256.Sum256([]byte(account))
	return filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(m.dir))), "resources", "music", hex.EncodeToString(digest[:8]))
}

func (m *Manager) materializeLibrarySong(a mediaAsset) error {
	data, err := m.cachedAssetData(a)
	if err != nil {
		return err
	}
	dir := m.musicLibraryDirectory(a.Account)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	ext := strings.ToLower(filepath.Ext(a.Filename))
	if ext != ".m4a" && ext != ".mp3" && ext != ".wav" && ext != ".flac" && ext != ".ogg" {
		ext = ".audio"
	}
	path := filepath.Join(dir, a.Hash+ext)
	if _, err = os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err = os.WriteFile(path, data, 0600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	entry := map[string]any{"asset_id": a.ID, "title": a.Title, "filename": a.Filename, "mime": a.MIME, "sha256": a.Hash, "bytes": a.Bytes, "source_url": a.URL, "file": filepath.Base(path)}
	meta, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return writePrivate(filepath.Join(dir, a.Hash+".json"), meta)
}

func (m *Manager) searchMusicLibrary(account, query string, offset, limit int) ([]map[string]any, int, error) {
	if strings.TrimSpace(query) == "" || offset < 0 || limit < 1 {
		return nil, 0, errors.New("歌曲资源库检索参数无效")
	}
	m.assetMu.Lock()
	defer m.assetMu.Unlock()
	items, err := m.loadAssets()
	if err != nil {
		return nil, 0, err
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Updated.After(items[j].Updated) })
	rows := []map[string]any{}
	for _, a := range items {
		if !librarySong(a, account) || !strings.Contains(strings.ToLower(a.Title+" "+a.Filename), strings.ToLower(query)) {
			continue
		}
		if err := m.materializeLibrarySong(a); err != nil {
			continue // A missing or corrupt local file is not a usable match.
		}
		rows = append(rows, map[string]any{"asset_id": a.ID, "title": a.Title, "filename": a.Filename, "mime": a.MIME, "bytes": a.Bytes, "source_url": a.URL, "is_generated": false, "original_cached": true})
	}
	if offset > len(rows) {
		return nil, 0, errors.New("歌曲资源库分页参数无效")
	}
	return rows[offset : offset+min(limit, len(rows)-offset)], len(rows), nil
}
