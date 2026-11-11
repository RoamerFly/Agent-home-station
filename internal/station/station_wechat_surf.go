package station

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/stationruntime"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

const (
	stationSurfFile   = "surf-schedule.json"
	stationSurfPlugin = "web-search"
)

type stationSurfSettings struct {
	Topics             []string                   `json:"topics"`
	Sources            []mcptools.DiscoverySource `json:"sources"`
	IntervalMinutes    int                        `json:"interval_minutes"`
	IntervalMaxMinutes int                        `json:"interval_max_minutes"`
	DailyLimit         int                        `json:"daily_limit"`
	ToolCalls          int                        `json:"tool_calls"`
	Timezone           string                     `json:"timezone"`
	QuietStart         string                     `json:"quiet_start"`
	QuietEnd           string                     `json:"quiet_end"`
}

type stationSurfSchedule struct {
	Account     string              `json:"account"`
	Revision    int                 `json:"revision"`
	Enabled     bool                `json:"enabled"`
	Settings    stationSurfSettings `json:"settings"`
	Next        time.Time           `json:"next,omitzero"`
	Day         string              `json:"day,omitempty"`
	Claims      int                 `json:"claims"`
	LastKey     string              `json:"last_key,omitempty"`
	LastRequest string              `json:"last_request,omitempty"`
	LastRun     *stationSurfRun     `json:"last_run,omitempty"`
}

type stationSurfRun struct {
	Trace          string    `json:"trace"`
	At             time.Time `json:"at"`
	State          string    `json:"state"`
	Candidates     int       `json:"candidates"`
	ReadCandidates int       `json:"read_candidates"`
	Bookmark       string    `json:"bookmark,omitempty"`
	Share          string    `json:"share,omitempty"`
}

type stationSurfUpdate struct {
	Action           string               `json:"action"`
	Revision         int                  `json:"revision"`
	Key              string               `json:"key"`
	Settings         *stationSurfSettings `json:"settings,omitempty"`
	AllowPublicReads bool                 `json:"allow_public_reads,omitempty"`
}

func stationSurfDefaults(account string) stationSurfSchedule {
	return stationSurfSchedule{Account: account, Settings: stationSurfSettings{
		Topics: []string{"海洋趣闻", "日常小发现"}, Sources: mcptools.DefaultDiscoverySources(),
		IntervalMinutes: 60, IntervalMaxMinutes: 150, DailyLimit: 4, ToolCalls: 12, Timezone: "Asia/Shanghai",
		QuietStart: "00:00", QuietEnd: "08:00",
	}}
}

func (p stationSurfSettings) clock() companion.Config {
	return companion.Config{Timezone: p.Timezone, QuietStart: p.QuietStart, QuietEnd: p.QuietEnd}
}

func (p stationSurfSettings) valid() bool {
	if p.IntervalMinutes < 15 || p.IntervalMaxMinutes < p.IntervalMinutes || p.IntervalMaxMinutes > 1440 ||
		p.DailyLimit < 1 || p.DailyLimit > 24 || p.ToolCalls < 3 || p.ToolCalls > 30 || len(p.Topics) < 1 || len(p.Topics) > 20 ||
		len(p.Sources) < 1 || len(p.Sources) > 12 || p.Timezone == "Local" || p.Timezone == "" || len(p.Timezone) > 100 {
		return false
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return false
	}
	for _, clock := range []string{p.QuietStart, p.QuietEnd} {
		if value, err := time.Parse("15:04", clock); err != nil || value.Format("15:04") != clock {
			return false
		}
	}
	for _, topic := range p.Topics {
		if strings.TrimSpace(topic) == "" || len(topic) > 200 {
			return false
		}
	}
	seen := map[string]bool{}
	active := false
	for _, source := range p.Sources {
		if !identity.ValidPathID(source.ID) || seen[source.ID] || mcptools.ValidateDiscoverySource(source) != nil {
			return false
		}
		seen[source.ID] = true
		active = active || source.Enabled
	}
	return active
}

func (p stationSurfSettings) next(now time.Time) (time.Time, error) {
	span := int64(p.IntervalMaxMinutes-p.IntervalMinutes)*60 + 1
	jitter, err := rand.Int(rand.Reader, big.NewInt(span))
	if err != nil {
		return time.Time{}, err
	}
	return now.UTC().Add(time.Duration(int64(p.IntervalMinutes)*60+jitter.Int64()) * time.Second), nil
}

func (w *stationWechatWorker) surfSchedule(account string) (stationSurfSchedule, error) {
	state := stationSurfDefaults(account)
	root, err := workspacefs.OpenPath(w.dir)
	if err != nil {
		return state, err
	}
	defer root.Close()
	raw, err := readLegacyImportFile(root, stationSurfFile, 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	var saved stationSurfSchedule
	if err != nil || json.Unmarshal(raw, &saved) != nil || saved.Account != account || saved.Revision < 1 ||
		!saved.Settings.valid() || saved.Claims < 0 || saved.Claims > 24 || (saved.Enabled && saved.Next.IsZero()) {
		return state, apphost.ErrUnavailable
	}
	return saved, nil
}

// The caller holds surfMu. Keeping this separate from worker.mu lets a
// lifecycle stop wait for the running worker without deadlocking its claim.
func (w *stationWechatWorker) saveSurf(state stationSurfSchedule) error {
	root, err := workspacefs.OpenPath(w.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if info, err := root.Lstat(stationSurfFile); err == nil && !info.Mode().IsRegular() || err != nil && !errors.Is(err, os.ErrNotExist) {
		return apphost.ErrUnavailable
	}
	temp := ".surf-" + identity.RandomToken()
	defer root.Remove(temp)
	if err := writeLegacyStagedFile(root, temp, state); err != nil {
		return err
	}
	return root.Rename(temp, stationSurfFile)
}

func (w *stationWechatWorker) updateSurf(account string, in stationSurfUpdate, ready bool, now time.Time) (stationSurfSchedule, error) {
	w.surfMu.Lock()
	defer w.surfMu.Unlock()
	state, err := w.surfSchedule(account)
	if err != nil {
		return state, err
	}
	request := localCheckpointKey(in)
	if state.LastKey == in.Key {
		if state.LastRequest != request {
			return state, apphost.ErrLifecycleConflict
		}
		return state, nil
	}
	if in.Revision != state.Revision {
		return state, apphost.ErrLifecycleConflict
	}
	switch in.Action {
	case "save":
		state.Settings = *in.Settings
		state.Enabled, state.Next = false, time.Time{}
	case "pause":
		state.Enabled, state.Next = false, time.Time{}
	case "resume":
		if !ready {
			return state, apphost.ErrLifecycleNotReady
		}
		if !state.Enabled {
			state.Next, err = state.Settings.next(now)
			if err != nil {
				return state, err
			}
		}
		state.Enabled = true
	}
	if state.LastRun != nil && state.LastRun.State == "started" {
		state.LastRun.State = "interrupted"
	}
	state.Revision++
	state.LastKey, state.LastRequest = in.Key, request
	if err := w.saveSurf(state); err != nil {
		return state, err
	}
	if w.surfCancel != nil {
		w.surfCancel()
		w.surfCancel = nil
	}
	return state, nil
}

func (in stationSurfUpdate) valid() bool {
	if in.Revision < 0 || !identity.ValidPathID(in.Key) {
		return false
	}
	switch in.Action {
	case "save":
		return !in.AllowPublicReads && in.Settings != nil && in.Settings.valid()
	case "pause":
		return !in.AllowPublicReads && in.Settings == nil
	case "resume":
		return in.AllowPublicReads && in.Settings == nil
	}
	return false
}

func (s *stationWechat) surfRoute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ctx := r.PathValue("connection"), r.Context()
	if !artifactID(id, "wechat-") {
		websession.Fail(w, 404, "微信连接不存在")
		return
	}
	var in stationSurfUpdate
	writing := r.Method == http.MethodPut
	if writing {
		if !websession.Decode(w, r, &in) {
			return
		}
		if !in.valid() {
			websession.Fail(w, 400, "请检查冲浪配置、操作编号和公开读取授权")
			return
		}
	}
	connection, err := s.stationWechatNative.Inspect(ctx, id)
	if err != nil || wechatapp.ConnectionID(connection.AccountID) != id || connection.ID != id {
		websession.Fail(w, 404, "微信连接暂不可核对")
		return
	}
	worker, err := s.worker(id, writing && in.Action == "save")
	if err != nil {
		websession.Fail(w, 503, "微信冲浪设置暂不可读")
		return
	}
	state := stationSurfDefaults(connection.AccountID)
	ready := false
	if worker != nil {
		ready = stationruntime.ToolRuntimeReady(s.shareOptions, s.workspaceRoot) && s.surfPluginReady(ctx, worker, connection.AccountID)
		if writing {
			state, err = worker.updateSurf(connection.AccountID, in, ready, time.Now())
		} else {
			worker.surfMu.Lock()
			state, err = worker.surfSchedule(connection.AccountID)
			worker.surfMu.Unlock()
		}
	} else if writing {
		err = apphost.ErrLifecycleNotReady
	}
	switch {
	case errors.Is(err, apphost.ErrLifecycleConflict):
		websession.Fail(w, 409, "配置或操作编号已变化，请刷新后重试")
	case errors.Is(err, apphost.ErrLifecycleNotReady):
		websession.Fail(w, 409, "请先保存冲浪设置并启用此连接的公开网页工具")
	case err != nil:
		websession.Fail(w, 503, "微信冲浪设置暂不可读或保存")
	default:
		state.LastKey, state.LastRequest = "", ""
		websession.Respond(w, 200, struct {
			stationSurfSchedule
			PublicReaderReady bool `json:"public_reader_ready"`
		}{state, ready})
	}
}

func (s *stationWechat) surfPluginReady(ctx context.Context, worker *stationWechatWorker, account string) bool {
	root, err := workspacefs.OpenPath(filepath.Join(worker.dir, "project-tools"))
	if err != nil {
		return false
	}
	root.Close()
	host, err := s.shareHost(worker, account)
	if err != nil {
		return false
	}
	_, err = surfInstallation(ctx, host)
	return err == nil
}

func (w *stationWechatWorker) claimSurf(ctx context.Context, account string, now time.Time, allowed func() error, prepare func(context.Context, stationSurfSchedule) (func(context.Context, string) (companion.ToolAccess, error), error)) (*bot.SurfTask, error) {
	w.surfMu.Lock()
	defer w.surfMu.Unlock()
	state, err := w.surfSchedule(account)
	if err != nil || !state.Enabled || now.Before(state.Next) || state.Settings.clock().Quiet(now) {
		return nil, err
	}
	loc, err := time.LoadLocation(state.Settings.Timezone)
	if err != nil {
		return nil, err
	}
	day := now.In(loc).Format("2006-01-02")
	if day != state.Day {
		state.Day, state.Claims = day, 0
	}
	if state.Claims >= state.Settings.DailyLimit || ctx.Err() != nil {
		return nil, nil
	}
	if err = allowed(); err != nil {
		return nil, err
	}
	factory, err := prepare(ctx, state)
	if errors.Is(err, apphost.ErrLifecycleNotReady) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	state.Next, err = state.Settings.next(now)
	if err != nil {
		return nil, err
	}
	topic := state.Settings.Topics[state.Claims%len(state.Settings.Topics)]
	state.Claims++
	if err := w.saveSurf(state); err != nil {
		return nil, err
	}
	quiet := state.Settings.clock()
	mayShare := false
	if root, err := workspacefs.OpenPath(w.dir); err == nil {
		grant, err := readSurfShareGrants(root, account)
		root.Close()
		mayShare = err == nil && grant.Auto && len(grant.Targets) > 0
	}
	return &bot.SurfTask{Topic: topic, PreferredKind: "text", QuietDeadline: quiet.QuietDeadline(now),
		PrivateBrowseTools: factory, PrivateQuietHours: &quiet, PrivateToolCalls: state.Settings.ToolCalls, PrivateMayShare: mayShare}, nil
}
