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
	"unicode"
)

// The resource library contains public recordings downloaded by this account.
// Conversation identifiers and request history never leave the private index.
func librarySong(a mediaAsset, account string) bool {
	return a.Account == account && a.Hash != "" && a.URL != "" && a.Music == nil && strings.HasPrefix(a.MIME, "audio/")
}

func musicLibraryMatch(a mediaAsset, query string) (string, int) {
	parts := func(value string) []string {
		return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsNumber(r)
		})
	}
	terms := parts(query)
	if len(terms) == 0 {
		return "", 0
	}
	name := strings.Join(parts(a.Title+" "+a.Filename), "")
	matched := 0
	for _, term := range terms {
		if strings.Contains(name, term) {
			matched++
		}
	}
	if matched == len(terms) {
		return "exact", matched
	}
	// Song requests often include an anime, scene or alternate artist spelling
	// that is absent from an otherwise matching saved recording. Offer a
	// clearly labelled candidate only when most terms still match locally.
	if matched >= 2 && matched*5 >= len(terms)*3 {
		return "partial", matched
	}
	return "", 0
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
	exact, partial := []map[string]any{}, []map[string]any{}
	for _, a := range items {
		if !librarySong(a, account) {
			continue
		}
		quality, matched := musicLibraryMatch(a, query)
		if quality == "" {
			continue
		}
		if err := m.materializeLibrarySong(a); err != nil {
			continue // A missing or corrupt local file is not a usable match.
		}
		row := map[string]any{"asset_id": a.ID, "title": a.Title, "filename": a.Filename, "mime": a.MIME, "bytes": a.Bytes, "source_url": a.URL, "is_generated": false, "original_cached": true, "match_quality": quality}
		if quality == "exact" {
			exact = append(exact, row)
		} else {
			row["matched_terms"] = matched
			partial = append(partial, row)
		}
	}
	rows := exact
	if len(rows) == 0 {
		sort.SliceStable(partial, func(i, j int) bool { return partial[i]["matched_terms"].(int) > partial[j]["matched_terms"].(int) })
		rows = partial
	}
	if offset > len(rows) {
		return nil, 0, errors.New("歌曲资源库分页参数无效")
	}
	return rows[offset : offset+min(limit, len(rows)-offset)], len(rows), nil
}
