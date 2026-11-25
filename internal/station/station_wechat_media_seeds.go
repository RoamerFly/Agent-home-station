package station

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"wechat-bot/internal/bot"
	"wechat-bot/internal/hostfile"
	"wechat-bot/internal/linuxnative"
)

type stationWechatMediaSeed struct {
	Seed    bot.MediaSeed          `json:"seed"`
	Status  linuxnative.ToolStatus `json:"status"`
	Settled bool                   `json:"settled,omitempty"`
}

type stationWechatMediaSeedFile struct {
	Version int                      `json:"version"`
	Items   []stationWechatMediaSeed `json:"items"`
}

const stationWechatMediaSeedFileName = "media-seeds.json"

func (w *stationWechatWorker) mediaSeedPath() string {
	return filepath.Join(w.queueDir(), stationWechatMediaSeedFileName)
}

func (w *stationWechatWorker) readMediaSeeds() ([]stationWechatMediaSeed, error) {
	raw, err := os.ReadFile(w.mediaSeedPath())
	if errors.Is(err, os.ErrNotExist) {
		return []stationWechatMediaSeed{}, nil
	}
	if err != nil {
		return nil, err
	}
	var file stationWechatMediaSeedFile
	if json.Unmarshal(raw, &file) != nil || file.Version != 1 || len(file.Items) > 256 {
		return nil, errors.New("微信跨会话媒体种子账本格式无效")
	}
	return file.Items, nil
}

func (w *stationWechatWorker) writeMediaSeeds(items []stationWechatMediaSeed) error {
	if len(items) > 256 {
		return errors.New("微信跨会话媒体种子数量超过本机限制")
	}
	if err := os.MkdirAll(w.queueDir(), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(stationWechatMediaSeedFile{Version: 1, Items: items})
	if err != nil {
		return err
	}
	return hostfile.WritePrivate(w.mediaSeedPath(), raw)
}

func (w *stationWechatWorker) mediaSeedAllowed(seed bot.MediaSeed) bool {
	if seed.Input.Account == "" || seed.Input.Account != w.policyAccount() || seed.Input.Thread == "" {
		return false
	}
	if w.policy.Grants[seed.Input.Thread].IsZero() {
		return false
	}
	return seed.OriginThread == "" || !w.policy.Grants[seed.OriginThread].IsZero()
}

func (w *stationWechatWorker) policyAccount() string {
	return w.account
}

func (w *stationWechatWorker) mediaSeeds() []bot.MediaSeed {
	w.ownerMu.Lock()
	defer w.ownerMu.Unlock()
	items, err := w.readMediaSeeds()
	if err != nil {
		return nil
	}
	seeds := make([]bot.MediaSeed, 0, len(items))
	for _, item := range items {
		if !item.Settled && item.Seed.Key != "" {
			seeds = append(seeds, item.Seed)
		}
	}
	return seeds
}

func (w *stationWechatWorker) mediaSeedKnown(seed bot.MediaSeed) bool {
	if !w.mediaSeedAllowed(seed) {
		return false
	}
	w.ownerMu.Lock()
	defer w.ownerMu.Unlock()
	items, err := w.readMediaSeeds()
	if err != nil {
		return false
	}
	for _, item := range items {
		if item.Settled || mediaSeedIdentityMismatch(item.Seed, seed) {
			continue
		}
		if seed.Key != "" && item.Seed.Key == seed.Key || seed.JobID != "" && item.Seed.JobID == seed.JobID {
			return true
		}
	}
	return false
}

func (w *stationWechatWorker) mediaSeedReceipt(trace, key string) (linuxnative.ToolStatus, bool) {
	w.ownerMu.Lock()
	defer w.ownerMu.Unlock()
	items, err := w.readMediaSeeds()
	if err != nil {
		return linuxnative.ToolStatus{}, false
	}
	for _, item := range items {
		if item.Seed.Trace == trace && item.Seed.Key == key && item.Status.ID != "" {
			return item.Status, true
		}
	}
	return linuxnative.ToolStatus{}, false
}

func (w *stationWechatWorker) settleMediaSeed(trace, key string, status linuxnative.ToolStatus) error {
	w.ownerMu.Lock()
	defer w.ownerMu.Unlock()
	items, err := w.readMediaSeeds()
	if err != nil {
		return err
	}
	for i := range items {
		if items[i].Seed.Trace == trace && items[i].Seed.Key == key {
			items[i].Status = status
			items[i].Settled = status.State == "accepted" && status.ServerID != ""
			return w.writeMediaSeeds(items)
		}
	}
	return errors.New("跨会话媒体种子不存在")
}

// mediaSeedIdentityMismatch keeps the migration file from becoming a
// cross-account source. The native sender still performs the final
// thread/source checks.
func mediaSeedIdentityMismatch(seed, other bot.MediaSeed) bool {
	return seed.Input.Account != "" && other.Input.Account != "" && seed.Input.Account != other.Input.Account
}
