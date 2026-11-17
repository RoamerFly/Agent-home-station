package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

func (m *Manager) verifyNamedDuplicate(a mediaAsset) error {
	root, e := os.OpenRoot(m.assetDirectory())
	if e != nil {
		return e
	}
	defer root.Close()
	for _, name := range []string{a.Hash, a.StorageName} {
		info, e := root.Lstat(name)
		if e != nil || !info.Mode().IsRegular() {
			return errors.New("素材副本不可读")
		}
		f, e := root.Open(name)
		if e != nil {
			return e
		}
		h := sha256.New()
		n, e := io.Copy(h, f)
		f.Close()
		if e != nil || n != a.Bytes || hex.EncodeToString(h.Sum(nil)) != a.Hash {
			return errors.New("素材副本校验失败，保留原件")
		}
	}
	return root.Remove(a.Hash)
}

// Stable asset IDs and hashes remain internal references. Only the physical
// filename changes; callers resolve it through the authoritative catalogue.
func readableAssetName(name, hash string) string {
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '-'
		}
		return r
	}, name)
	name = strings.TrimFunc(name, func(r rune) bool { return unicode.IsSpace(r) || r == '.' })
	if name == "" {
		name = "资源.bin"
	}
	ext := filepath.Ext(name)
	if ext == "" {
		ext = ".bin"
	}
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if len([]rune(stem)) > 90 {
		stem = string([]rune(stem)[:90])
	}
	// A short identity suffix makes collision handling deterministic across
	// accounts and restarts without retaining an extensionless hash duplicate.
	if len(hash) >= 8 {
		stem += "-" + hash[:8]
	}
	return stem + ext
}

func (m *Manager) storedAssetName(hash string) string {
	var items []mediaAsset
	b, e := os.ReadFile(filepath.Join(m.dir, "media-assets.json"))
	if e == nil && len(b) < 16<<20 && json.Unmarshal(b, &items) == nil {
		for _, a := range items {
			if a.Hash == hash && a.StorageName != "" && safeMediaFilename(a.StorageName) {
				return a.StorageName
			}
		}
	}
	return hash
}

func (m *Manager) nameAssetFiles(items []mediaAsset) ([]mediaAsset, error) {
	names := map[string]string{}
	for _, a := range items {
		if a.StorageName != "" && safeMediaFilename(a.StorageName) {
			names[a.Hash] = a.StorageName
		}
	}
	for i := range items {
		a := &items[i]
		if len(a.Hash) != 64 || strings.Trim(a.Hash, "0123456789abcdef") != "" {
			continue
		}
		if a.Account != m.native.Account() {
			continue
		}
		if names[a.Hash] == "" {
			names[a.Hash] = readableAssetName(a.Filename, a.Hash)
		}
		a.StorageName = names[a.Hash]
	}
	return items, nil
}

func (m *Manager) publishAssetNames(items []mediaAsset) error {
	for _, a := range items {
		if a.Account != m.native.Account() || a.StorageName == "" {
			continue
		}
		root, err := os.OpenRoot(m.assetDirectory())
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		old, e := root.Lstat(a.Hash)
		if os.IsNotExist(e) {
			root.Close()
			if err := m.recoverReadableLegacyAsset(a); err != nil {
				return err
			}
			continue
		}
		if e != nil {
			root.Close()
			return e
		}
		if !old.Mode().IsRegular() {
			root.Close()
			return errors.New("素材原件不是普通文件")
		}
		if _, e = root.Lstat(a.StorageName); e == nil {
			// A repeated import may have recreated the old hash object. Both copies
			// must verify before removing that duplicate.
			root.Close()
			if err = m.verifyNamedDuplicate(a); err != nil {
				return err
			}
			continue
		} else if !os.IsNotExist(e) {
			root.Close()
			return e
		}
		err = root.Rename(a.Hash, a.StorageName)
		root.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) recoverReadableLegacyAsset(a mediaAsset) error {
	target := filepath.Join(m.assetDirectory(), a.StorageName)
	if _, e := os.Stat(target); e == nil {
		return nil
	}
	rel, e := filepath.Rel(filepath.Join(m.dir, "managed-workspaces"), m.assetDirectory())
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	source := filepath.Join(m.dir, "workspace", rel, a.Hash)
	info, e := os.Lstat(source)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if !info.Mode().IsRegular() {
		return errors.New("旧素材不是普通文件")
	}
	resolved, e := filepath.EvalSymlinks(source)
	if e != nil || !strings.EqualFold(resolved, source) {
		return errors.New("旧素材路径包含链接")
	}
	f, e := os.Open(source)
	if e != nil {
		return e
	}
	h := sha256.New()
	n, e := io.Copy(h, f)
	f.Close()
	if e != nil || n != a.Bytes || hex.EncodeToString(h.Sum(nil)) != a.Hash {
		return errors.New("旧素材校验失败，原件保留")
	}
	return os.Rename(source, target)
}
