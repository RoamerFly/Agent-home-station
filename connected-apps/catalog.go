// Package catalog binds bundled application sources to host-approved digests.
package catalog

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"

	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/workspacefs"
)

// Only descriptors are embedded. Runtime source stays in the distribution.
//
//go:embed */app.json
var descriptors embed.FS

type Contract struct {
	Package pluginhost.Package
	Digest  string
}

func IDs() []string { return []string{"wechat", "bilibili", "douyin"} }

func Lookup(id string) (Contract, error) {
	if !slices.Contains(IDs(), id) {
		return Contract{}, errors.New("应用不在本机发行目录中")
	}
	data, err := descriptors.ReadFile(id + "/app.json")
	var p pluginhost.Package
	if err != nil || json.Unmarshal(data, &p) != nil || p.ID != id {
		return Contract{}, errors.New("应用发行声明不可用")
	}
	hash := sha256.Sum256(data)
	return Contract{Package: p, Digest: hex.EncodeToString(hash[:])}, nil
}

// VerifySource binds the indexed files on disk to the descriptor in this host.
// The caller can still show the embedded contract when a package is unavailable.
func VerifySource(directory string, contract Contract) error {
	if _, err := pluginhost.VerifyDescriptor(directory, "app.json"); err != nil {
		return errors.New("应用发行源未通过完整性校验")
	}
	root, err := workspacefs.OpenPath(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := workspacefs.OpenRegular(root, "app.json")
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return errors.New("应用发行声明不可读")
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != contract.Digest {
		return errors.New("应用发行源与当前主程序版本不匹配")
	}
	return nil
}

// VerifyBundled checks exactly the applications selected for a distribution.
func VerifyBundled(catalog string, ids []string) ([]Contract, error) {
	if len(ids) == 0 {
		return nil, errors.New("请指定要校验的随包应用")
	}
	seen := make(map[string]bool, len(ids))
	verified := make([]Contract, 0, len(ids))
	for _, id := range ids {
		contract, err := Lookup(id)
		if err != nil {
			return nil, err
		}
		if seen[id] {
			return nil, errors.New("同一应用只能校验一次")
		}
		seen[id] = true
		if err := VerifySource(filepath.Join(catalog, id), contract); err != nil {
			return nil, errors.New(id + "：" + err.Error())
		}
		verified = append(verified, contract)
	}
	return verified, nil
}
