package admin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"wechat-bot/internal/devicespec"
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
	Version        int                  `json:"version"`
	FishID         string               `json:"fish_id,omitempty"`
	Bindings       []fishAccountBinding `json:"bindings"`
	DeviceBindings []fishDeviceBinding  `json:"device_bindings,omitempty"`
}

// fishAccountBinding keeps the platform namespace in the durable key even
// though the first administration surface exposes WeChat accounts only.
// A same-looking account ID on a later platform can therefore never merge by
// accident with a WeChat account.
type fishAccountBinding struct {
	Platform string `json:"platform"`
	Account  string `json:"account_id"`
}

// fishDeviceBinding deliberately contains identifiers only. Device connection
// materials, screenshots and application credentials remain owned by their
// respective host registries; linking a phone must not copy them into the
// fish's identity record.
type fishDeviceBinding struct {
	Account   string `json:"account_id"`
	DeviceID  string `json:"device_id"`
	BindingID string `json:"binding_id"`
}

const (
	fishWechatPlatform          = "wechat"
	fishIdentityVersionV1       = 1
	fishIdentityCurrentVersion  = 2
	fishDeviceBindingIDPrefix   = "fish-device-"
	fishDeviceBindingTokenChars = 48
)

func newFishIdentityRegistry(dir string) *fishIdentityRegistry {
	// Do not prevent the existing WeChat admin service from starting because an
	// optional new identity file is damaged. Every identity operation reads and
	// validates it afresh, reports the failure, and never overwrites it.
	return &fishIdentityRegistry{path: filepath.Join(dir, "fish-identity.json")}
}

func (r *fishIdentityRegistry) read() (fishIdentityRecord, error) {
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return fishIdentityRecord{Version: fishIdentityCurrentVersion, Bindings: []fishAccountBinding{}, DeviceBindings: []fishDeviceBinding{}}, nil
	}
	if err != nil || len(data) > 1<<20 {
		return fishIdentityRecord{}, errors.New("鱼身份绑定不可读")
	}
	// Decode the version envelope before the record. json.Unmarshal ignores
	// unknown fields, which would make a v1 record carrying device grants look
	// harmless and could silently authorize a malformed migration.
	var envelope map[string]json.RawMessage
	if json.Unmarshal(data, &envelope) != nil {
		return fishIdentityRecord{}, errors.New("鱼身份绑定无效，原文件未覆盖")
	}
	versionRaw, present := envelope["version"]
	var version int
	if !present || json.Unmarshal(versionRaw, &version) != nil {
		return fishIdentityRecord{}, errors.New("鱼身份绑定无效，原文件未覆盖")
	}
	if version == fishIdentityVersionV1 {
		if _, hasDeviceBindings := envelope["device_bindings"]; hasDeviceBindings {
			return fishIdentityRecord{}, errors.New("鱼身份绑定无效，原文件未覆盖")
		}
	} else if version != fishIdentityCurrentVersion {
		return fishIdentityRecord{}, errors.New("鱼身份绑定无效，原文件未覆盖")
	}
	var record fishIdentityRecord
	if json.Unmarshal(data, &record) != nil || record.Version != version || !validFishID(record.FishID) {
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
	if record.Version == fishIdentityCurrentVersion {
		seenDevices := map[string]bool{}
		seenBindingIDs := map[string]bool{}
		for _, binding := range record.DeviceBindings {
			key := binding.Account + "\x00" + binding.DeviceID
			if !fishAccountIsBound(record, binding.Account) || !validFishDeviceID(binding.DeviceID) || !validFishDeviceBindingID(binding.BindingID) || seenDevices[key] || seenBindingIDs[binding.BindingID] {
				return fishIdentityRecord{}, errors.New("鱼设备绑定无效，原文件未覆盖")
			}
			seenDevices[key] = true
			seenBindingIDs[binding.BindingID] = true
		}
	}
	if record.DeviceBindings == nil {
		record.DeviceBindings = []fishDeviceBinding{}
	}
	return record, nil
}

func fishAccountIsBound(record fishIdentityRecord, account string) bool {
	return slices.Contains(record.Bindings, fishAccountBinding{Platform: fishWechatPlatform, Account: account})
}

// validFishDeviceID delegates to the mobile device-spec validator rather than
// using the looser WeChat account ID rule. This keeps identity links aligned
// with IDs that the device tool can actually address.
func validFishDeviceID(id string) bool {
	settings := devicespec.Defaults()
	settings.Devices = []string{id}
	return settings.Validate() == nil
}

func validFishDeviceBindingID(id string) bool {
	if len(id) != len(fishDeviceBindingIDPrefix)+fishDeviceBindingTokenChars || !strings.HasPrefix(id, fishDeviceBindingIDPrefix) {
		return false
	}
	for _, r := range id[len(fishDeviceBindingIDPrefix):] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
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
	if !validID(account) {
		return "", errors.New("账号标识无效")
	}
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
	// A phone can only be used through a currently bound account. Removing that
	// account must revoke every such link without touching another account's
	// device bindings.
	record.DeviceBindings = slices.DeleteFunc(record.DeviceBindings, func(bound fishDeviceBinding) bool {
		return bound.Account == account
	})
	return r.write(record)
}

// bindDevice creates an explicit local association between a fish-bound WeChat
// account and a registered mobile device ID. It is metadata only: no device
// session, application account, screenshot or private material is copied.
func (r *fishIdentityRegistry) bindDevice(account, deviceID string) (fishID, bindingID string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.read()
	if err != nil {
		return "", "", err
	}
	if !fishAccountIsBound(record, account) {
		return "", "", errors.New("账号未绑定到鱼身份")
	}
	if !validFishDeviceID(deviceID) {
		return "", "", errors.New("设备标识无效")
	}
	for _, binding := range record.DeviceBindings {
		if binding.Account == account && binding.DeviceID == deviceID {
			return record.FishID, binding.BindingID, nil
		}
	}
	bindingID = fishDeviceBindingIDPrefix + randomToken()[:fishDeviceBindingTokenChars]
	record.DeviceBindings = append(record.DeviceBindings, fishDeviceBinding{Account: account, DeviceID: deviceID, BindingID: bindingID})
	slices.SortFunc(record.DeviceBindings, func(i, j fishDeviceBinding) int {
		if i.Account == j.Account {
			return strings.Compare(i.DeviceID, j.DeviceID)
		}
		return strings.Compare(i.Account, j.Account)
	})
	if err := r.write(record); err != nil {
		return "", "", err
	}
	return record.FishID, bindingID, nil
}

// deviceBindings returns bindings only for one already fish-bound account.
// The returned slice is a copy, so callers cannot modify registry state.
func (r *fishIdentityRegistry) deviceBindings(account string) (fishID string, bindings []fishDeviceBinding, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.read()
	if err != nil {
		return "", nil, err
	}
	if !fishAccountIsBound(record, account) {
		return "", nil, errors.New("账号未绑定到鱼身份")
	}
	for _, binding := range record.DeviceBindings {
		if binding.Account == account {
			bindings = append(bindings, binding)
		}
	}
	return record.FishID, bindings, nil
}

// unbindDevice is conditional so a stale management page cannot unbind a
// newly-created replacement. An unbind/rebind always mints a fresh binding ID.
func (r *fishIdentityRegistry) unbindDevice(account, deviceID, expectedBindingID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.read()
	if err != nil {
		return err
	}
	if !fishAccountIsBound(record, account) {
		return errors.New("账号未绑定到鱼身份")
	}
	if !validFishDeviceID(deviceID) || !validFishDeviceBindingID(expectedBindingID) {
		return errors.New("鱼设备绑定已变更，请刷新后再试")
	}
	index := slices.IndexFunc(record.DeviceBindings, func(binding fishDeviceBinding) bool {
		return binding.Account == account && binding.DeviceID == deviceID
	})
	if index < 0 || record.DeviceBindings[index].BindingID != expectedBindingID {
		return errors.New("鱼设备绑定已变更，请刷新后再试")
	}
	record.DeviceBindings = slices.Delete(record.DeviceBindings, index, index+1)
	return r.write(record)
}

func (r *fishIdentityRegistry) write(record fishIdentityRecord) error {
	// Any write is an intentional migration to v2. A read of an old v1 record
	// never grants a device by itself.
	record.Version = fishIdentityCurrentVersion
	data, err := json.Marshal(record)
	if err != nil {
		return errors.New("鱼身份绑定无法编码")
	}
	if err = writePrivate(r.path, data); err != nil {
		return errors.New("鱼身份绑定保存失败")
	}
	return nil
}
