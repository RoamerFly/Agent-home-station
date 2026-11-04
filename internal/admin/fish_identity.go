package admin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// fishIdentityRegistry is deliberately separate from an account's private
// configuration. An account can be paused, restored, or purged without making
// a nickname, a workspace directory, or an account-local file the fish's
// identity. At this stage there is one locally-owned fish, with zero or more
// active platform-account bindings.
type fishIdentityRegistry struct {
	mu   sync.Mutex
	path string
}

type fishIdentityRecord struct {
	Version  int                  `json:"version"`
	FishID   string               `json:"fish_id,omitempty"`
	Bindings []fishAccountBinding `json:"bindings"`
}

// fishAccountBinding keeps the platform namespace in the durable key even
// though the first administration surface exposes WeChat accounts only.
// A same-looking account ID on a later platform can therefore never merge by
// accident with a WeChat account.
type fishAccountBinding struct {
	Platform string `json:"platform"`
	Account  string `json:"account_id"`
}

const fishWechatPlatform = "wechat"

func newFishIdentityRegistry(dir string) *fishIdentityRegistry {
	// Do not prevent the existing WeChat admin service from starting because an
	// optional new identity file is damaged. Every identity operation reads and
	// validates it afresh, reports the failure, and never overwrites it.
	return &fishIdentityRegistry{path: filepath.Join(dir, "fish-identity.json")}
}

func (r *fishIdentityRegistry) read() (fishIdentityRecord, error) {
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return fishIdentityRecord{Version: 1, Bindings: []fishAccountBinding{}}, nil
	}
	if err != nil || len(data) > 1<<20 {
		return fishIdentityRecord{}, errors.New("鱼身份绑定不可读")
	}
	var record fishIdentityRecord
	if json.Unmarshal(data, &record) != nil || record.Version != 1 || !validFishID(record.FishID) {
		return fishIdentityRecord{}, errors.New("鱼身份绑定无效，原文件未覆盖")
	}
	seen := map[string]bool{}
	for _, binding := range record.Bindings {
		key := binding.Platform + "\x00" + binding.Account
		if binding.Platform != fishWechatPlatform || !validID(binding.Account) || seen[key] {
			return fishIdentityRecord{}, errors.New("鱼身份绑定无效，原文件未覆盖")
		}
		seen[key] = true
	}
	if record.Bindings == nil {
		record.Bindings = []fishAccountBinding{}
	}
	return record, nil
}

func validFishID(id string) bool {
	if len(id) != len("fish-")+48 || !strings.HasPrefix(id, "fish-") {
		return false
	}
	for _, r := range id[len("fish-"):] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func (r *fishIdentityRegistry) bindings() (string, map[string]bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.read()
	if err != nil {
		return "", nil, err
	}
	bound := make(map[string]bool, len(record.Bindings))
	for _, binding := range record.Bindings {
		if binding.Platform == fishWechatPlatform {
			bound[binding.Account] = true
		}
	}
	return record.FishID, bound, nil
}

func (r *fishIdentityRegistry) bind(account string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.read()
	if err != nil {
		return "", err
	}
	if record.FishID == "" {
		record.FishID = "fish-" + randomToken()[:48]
	}
	binding := fishAccountBinding{Platform: fishWechatPlatform, Account: account}
	if slices.Contains(record.Bindings, binding) {
		return record.FishID, nil
	}
	record.Bindings = append(record.Bindings, binding)
	slices.SortFunc(record.Bindings, func(i, j fishAccountBinding) int {
		if i.Platform == j.Platform {
			return strings.Compare(i.Account, j.Account)
		}
		return strings.Compare(i.Platform, j.Platform)
	})
	if err = r.write(record); err != nil {
		return "", err
	}
	return record.FishID, nil
}

// unbind intentionally keeps FishID even after the final account is removed.
// A later explicit binding resumes the same local fish rather than inventing a
// second identity.
func (r *fishIdentityRegistry) unbind(account string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.read()
	if err != nil {
		return err
	}
	before := len(record.Bindings)
	record.Bindings = slices.DeleteFunc(record.Bindings, func(bound fishAccountBinding) bool {
		return bound.Platform == fishWechatPlatform && bound.Account == account
	})
	if len(record.Bindings) == before {
		return nil
	}
	return r.write(record)
}

func (r *fishIdentityRegistry) write(record fishIdentityRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return errors.New("鱼身份绑定无法编码")
	}
	if err = writePrivate(r.path, data); err != nil {
		return errors.New("鱼身份绑定保存失败")
	}
	return nil
}
