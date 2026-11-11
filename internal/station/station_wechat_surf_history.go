package station

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

type stationSurfReadSummary struct {
	Verified  bool   `json:"verified_public_read"`
	Title     string `json:"title,omitempty"`
	Site      string `json:"site,omitempty"`
	Candidate string `json:"candidate_id,omitempty"`
	SourceID  string `json:"source_id,omitempty"`
	Offset    int    `json:"offset"`
	Returned  int    `json:"returned_characters"`
	Total     int    `json:"total_characters"`
	Complete  bool   `json:"complete"`
}

type stationSurfHistoryRow struct {
	Job   string `json:"job"`
	At    string `json:"at"`
	Run   string `json:"run"`
	State string `json:"state"`
	stationSurfReadSummary
	Bookmarked bool `json:"bookmarked"`
	Readable   bool `json:"readable"`
}

type stationSurfHistoryPage struct {
	Rows          []stationSurfHistoryRow `json:"rows"`
	Limit         int                     `json:"limit"`
	Next          string                  `json:"next_after"`
	SourceVersion string                  `json:"source_version"`
}

type stationSurfBookmark struct {
	Account   string `json:"account"`
	Job       string `json:"job"`
	Candidate string `json:"candidate_id"`
	Run       string `json:"run"`
}

func surfHistoryJob(account string, job pluginhost.Job) bool {
	trace := job.Context.Authorization
	return trace != "" && strings.HasPrefix(trace, "surf-") && job.Plugin == stationSurfPlugin && job.Tool == "web.read" &&
		job.Context.Account == account && job.Context.Thread == "host:surf:"+account &&
		job.Context.Requester == "host:surf" && job.Context.Purpose == "private public web browsing" &&
		len(job.Context.SourceVersions) == 1 && job.Context.SourceVersions[trace] > 0 &&
		legacyMemoryDigestValid(job.ID)
}

func (w *stationWechatWorker) saveSurfBookmark(account string, job pluginhost.Job, candidate string) error {
	if !surfHistoryJob(account, job) || job.State != "completed" || job.CancelRequested || candidate == "" {
		return apphost.ErrUnavailable
	}
	root, err := workspacefs.OpenPath(w.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	dir, err := workspacefs.EnsureDirectory(root, "surf-bookmarks")
	if err != nil {
		return err
	}
	defer dir.Close()
	name := job.ID + ".json"
	expected := stationSurfBookmark{Account: account, Job: job.ID, Candidate: candidate, Run: job.Context.Authorization}
	raw, err := readLegacyImportFile(dir, name, 4<<10)
	if err == nil {
		var saved stationSurfBookmark
		if json.Unmarshal(raw, &saved) != nil || saved != expected {
			return apphost.ErrUnavailable
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temp := ".bookmark-" + identity.RandomToken()
	defer dir.Remove(temp)
	if err := writeLegacyStagedFile(dir, temp, expected); err != nil {
		return err
	}
	return dir.Rename(temp, name)
}

func (w *stationWechatWorker) surfBookmarked(account string, job pluginhost.Job) bool {
	root, err := workspacefs.OpenPath(w.dir)
	if err != nil {
		return false
	}
	defer root.Close()
	raw, err := readLegacyImportFile(root, filepath.Join("surf-bookmarks", job.ID+".json"), 4<<10)
	var mark stationSurfBookmark
	return err == nil && json.Unmarshal(raw, &mark) == nil && mark.Account == account && mark.Job == job.ID && mark.Run == job.Context.Authorization
}

func (w *stationWechatWorker) surfHistoryVersion(ctx context.Context, host *stationShareHost, account string) (string, error) {
	w.surfMu.Lock()
	defer w.surfMu.Unlock()
	state, err := w.surfSchedule(account)
	if err != nil {
		return "", err
	}
	item, err := host.store.Installed(ctx, stationSurfPlugin)
	if errors.Is(err, sql.ErrNoRows) {
		return localCheckpointKey(account, state.Revision, "no-public-reader"), nil
	}
	if err != nil {
		return "", err
	}
	config, err := host.store.Configuration(ctx, stationSurfPlugin)
	if err != nil {
		return "", err
	}
	return localCheckpointKey(account, state.Revision, item.Digest, item.Revision, item.Enabled, item.Removed, config.Revision), nil
}

// Listing uses the small public task result. It never reads a private page
// body, starts the plugin or repeats a network request.
func (w *stationWechatWorker) surfHistory(ctx context.Context, host *stationShareHost, account, after string, limit int) (stationSurfHistoryPage, error) {
	out := stationSurfHistoryPage{Rows: []stationSurfHistoryRow{}, Limit: limit}
	version, err := w.surfHistoryVersion(ctx, host, account)
	if err != nil {
		return out, err
	}
	out.SourceVersion = version
	sequence := int64(0)
	if after != "" {
		stamp, position, _ := strings.Cut(after, ":")
		sequence, err = strconv.ParseInt(position, 10, 64)
		if err != nil || stamp != version || sequence < 1 {
			return out, apphost.ErrLifecycleConflict
		}
	}
	page, err := host.store.JobsForTool(ctx, stationSurfPlugin, "web.read", sequence, limit)
	if err != nil {
		return out, err
	}
	for _, job := range page.Items {
		if !surfHistoryJob(account, job) {
			continue
		}
		row := stationSurfHistoryRow{Job: job.ID, At: job.Created, Run: job.Context.Authorization, State: job.State, Bookmarked: w.surfBookmarked(account, job)}
		_ = json.Unmarshal(job.Result, &row.stationSurfReadSummary)
		// Historical summaries remain visible after a tool is disabled.
		row.Readable = job.State == "completed" && row.Verified && !job.CancelRequested && surfHistorySourceAllowed(ctx, w, host, account, job) == nil
		out.Rows = append(out.Rows, row)
	}
	if page.HasMore {
		out.Next = version + ":" + strconv.FormatInt(page.Next, 10)
	}
	latest, err := w.surfHistoryVersion(ctx, host, account)
	if err != nil || latest != version {
		return stationSurfHistoryPage{}, apphost.ErrLifecycleConflict
	}
	return out, nil
}

func surfHistorySourceAllowed(ctx context.Context, w *stationWechatWorker, host *stationShareHost, account string, job pluginhost.Job) error {
	if !surfHistoryJob(account, job) {
		return apphost.ErrUnavailable
	}
	w.surfMu.Lock()
	defer w.surfMu.Unlock()
	state, err := w.surfSchedule(account)
	if err != nil || job.Context.EvidenceEpoch == "" {
		return apphost.ErrLifecycleConflict
	}
	var summary stationSurfReadSummary
	if json.Unmarshal(job.Result, &summary) != nil || !summary.Verified || summary.SourceID == "" {
		return apphost.ErrLifecycleConflict
	}
	matched := false
	for _, source := range state.Settings.Sources {
		if source.ID == summary.SourceID && source.Enabled && localCheckpointKey(source) == job.Context.EvidenceEpoch {
			matched = true
			break
		}
	}
	if !matched {
		return apphost.ErrLifecycleConflict
	}
	item, err := surfInstallation(ctx, host)
	if err != nil || item.Digest != job.PackageDigest || item.Package.Version != job.Version {
		return apphost.ErrLifecycleConflict
	}
	configuration, err := host.store.Configuration(ctx, stationSurfPlugin)
	if err != nil || strconv.Itoa(configuration.Revision) != job.ConfigRevision {
		return apphost.ErrLifecycleConflict
	}
	return ctx.Err()
}

func (w *stationWechatWorker) readSurfHistoryBody(ctx context.Context, host *stationShareHost, account, id, version string) (json.RawMessage, error) {
	if !legacyMemoryDigestValid(id) || !legacyMemoryDigestValid(version) {
		return nil, apphost.ErrNotFound
	}
	check := func(ctx context.Context, job pluginhost.Job) error {
		if err := surfHistorySourceAllowed(ctx, w, host, account, job); err != nil {
			return err
		}
		fresh, err := w.surfHistoryVersion(ctx, host, account)
		if err != nil || fresh != version {
			return apphost.ErrLifecycleConflict
		}
		return ctx.Err()
	}
	job, err := host.store.Job(ctx, id)
	if err != nil {
		return nil, apphost.ErrNotFound
	}
	return host.store.PrivateJobResult(ctx, job, check)
}

func (s *stationWechat) surfHistoryRoute(owner *websession.Store, body bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		limit := 10
		if !body {
			if raw := r.URL.Query().Get("limit"); raw != "" {
				var err error
				limit, err = strconv.Atoi(raw)
				if err != nil || limit < 1 || limit > 200 {
					websession.Fail(w, 400, "每页请输入 1 到 200 条")
					return
				}
			}
		}
		id := r.PathValue("connection")
		var connection apphost.Connection
		var err error
		if s.stationWechatNative == nil {
			connection, err = (&stationWechatArchive{store: s}).savedConnection(id)
		} else {
			var status apphost.ConnectionStatus
			status, err = s.stationWechatNative.Inspect(r.Context(), id)
			connection = status.Connection
		}
		if err != nil || connection.ID != id || wechatapp.ConnectionID(connection.AccountID) != id {
			websession.Fail(w, 404, "微信连接暂不可核对")
			return
		}
		worker, err := s.worker(id, false)
		if err != nil {
			websession.Fail(w, 503, "浏览记录暂不可读")
			return
		}
		if worker == nil {
			if body {
				websession.Fail(w, 404, "浏览记录不存在")
				return
			}
			websession.Respond(w, 200, stationSurfHistoryPage{Rows: []stationSurfHistoryRow{}, Limit: limit})
			return
		}
		root, err := workspacefs.OpenPath(filepath.Join(worker.dir, "project-tools"))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				websession.Fail(w, 503, "浏览记录暂不可读")
				return
			}
			if body {
				websession.Fail(w, 404, "浏览记录不存在")
				return
			}
			websession.Respond(w, 200, stationSurfHistoryPage{Rows: []stationSurfHistoryRow{}, Limit: limit})
			return
		}
		root.Close()
		host, err := s.shareHost(worker, connection.AccountID)
		if err != nil {
			websession.Fail(w, 503, "浏览记录暂不可读")
			return
		}
		var result any
		if body {
			result, err = worker.readSurfHistoryBody(r.Context(), host, connection.AccountID, r.PathValue("job"), r.URL.Query().Get("source_version"))
		} else {
			result, err = worker.surfHistory(r.Context(), host, connection.AccountID, r.URL.Query().Get("after"), limit)
		}
		if _, _, ok := owner.Lookup(r); !ok {
			websession.Fail(w, 401, "请重新登录")
			return
		}
		if err != nil {
			websession.Fail(w, 409, "浏览来源或读取授权已变化，请重新读取记录")
			return
		}
		websession.Respond(w, 200, result)
	}
}
