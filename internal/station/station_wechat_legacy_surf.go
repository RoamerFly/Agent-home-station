package station

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/websession"
)

const (
	legacySurfSourceFile   = "legacy-surf-source.json"
	legacySurfSettingsFile = "surf.json"
	legacySurfHistoryFile  = "surf-history.json"
	legacySurfSettingsMax  = 64 << 10
	legacySurfHistoryMax   = 8 << 20
)

// A supplemental capture must reproduce every entry in the original
// manifest once the new surf projections are removed.
func legacySurfSupplementMatches(guard *legacySnapshotGuard, original string) bool {
	if guard == nil || original == "" {
		return false
	}
	base := guard.manifest
	base.Files = make([]legacySnapshotEntry, 0, len(guard.manifest.Files))
	for _, entry := range guard.manifest.Files {
		dir, leaf := filepath.Split(filepath.FromSlash(entry.Path))
		dir = filepath.ToSlash(dir)
		underAdmin := dir == "admin/" || (strings.HasPrefix(dir, "admin/accounts/") &&
			strings.Count(dir, "/") == 3 && validLegacySnapshotDigest(strings.TrimSuffix(strings.TrimPrefix(dir, "admin/accounts/"), "/")))
		if underAdmin && (leaf == legacySurfSettingsFile || leaf == legacySurfHistoryFile) {
			continue
		}
		base.Files = append(base.Files, entry)
	}
	raw, err := json.Marshal(base)
	return err == nil && len(base.Files) < len(guard.manifest.Files) && hexDigest(raw) == original
}

type legacySurfSettings struct {
	Enabled          bool                 `json:"enabled"`
	ForceShare       bool                 `json:"force_share"`
	MinMinutes       int                  `json:"interval_minutes"`
	MaxMinutes       int                  `json:"interval_max_minutes"`
	DailyLimit       int                  `json:"max_sessions_per_day"`
	Topics           []string             `json:"topics"`
	TargetCount      int                  `json:"target_count"`
	InteractionCount int                  `json:"interaction_count"`
	Discovery        *legacySurfDiscovery `json:"discovery,omitempty"`
}

type legacySurfDiscovery struct {
	Sources      []legacySurfSource `json:"sources"`
	Weights      map[string]int     `json:"weights"`
	CooldownDays int                `json:"cooldown_days"`
	MaxSources   int                `json:"max_sources"`
	MaxDepth     int                `json:"max_depth"`
}

type legacySurfSource struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`
	Site    string `json:"site,omitempty"`
}

type legacySurfCandidate struct {
	Title      string `json:"title"`
	Kind       string `json:"kind"`
	Site       string `json:"site,omitempty"`
	Bookmarked bool   `json:"bookmarked,omitempty"`
	Selected   bool   `json:"selected,omitempty"`
}

type legacySurfObservation struct {
	State    string    `json:"state"`
	Site     string    `json:"site,omitempty"`
	Offset   int       `json:"offset,omitempty"`
	Returned int       `json:"returned_characters,omitempty"`
	Total    int       `json:"total_characters,omitempty"`
	Complete bool      `json:"complete,omitempty"`
	At       time.Time `json:"at"`
}

type legacySurfSession struct {
	Thread       string                  `json:"thread"`
	At           time.Time               `json:"at"`
	State        string                  `json:"state"`
	Interest     string                  `json:"interest,omitempty"`
	Candidates   []legacySurfCandidate   `json:"candidates"`
	Observations []legacySurfObservation `json:"observations"`
	Visited      int                     `json:"visited_count"`
	Failures     int                     `json:"failure_count"`
}

type legacySurfProjection[T any] struct {
	Version int    `json:"version"`
	Account string `json:"account"`
	Status  string `json:"status"`
	Data    T      `json:"data"`
}

func (p *legacySnapshotPlan) snapshotSurf(root *os.Root, dir, account string) error {
	for _, file := range []struct {
		name    string
		limit   int64
		project func([]byte, string) ([]byte, error)
		valid   func([]byte, string) bool
	}{
		{legacySurfSettingsFile, legacySurfSettingsMax, projectLegacySurfSettings, validLegacySurfProjection[*legacySurfSettings]},
		{legacySurfHistoryFile, legacySurfHistoryMax, projectLegacySurfHistory, validLegacySurfProjection[[]legacySurfSession]},
	} {
		path := filepath.Join(dir, file.name)
		raw, err := readLegacyImportFile(root, path, file.limit)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return errors.New("旧冲浪档案不可读或超出支持范围")
		}
		if p.verifying {
			if !file.valid(raw, account) {
				return errLegacySnapshotIntegrity
			}
			p.addSmall(path, raw, nil)
			continue
		}
		projected, err := file.project(raw, account)
		if err != nil {
			return err
		}
		if int64(len(projected)) > file.limit {
			return errors.New("旧冲浪档案预览超出支持范围")
		}
		p.addSmall(path, raw, projected)
	}
	return nil
}

func validLegacySurfProjection[T any](raw []byte, account string) bool {
	var projected legacySurfProjection[T]
	if json.Unmarshal(raw, &projected) != nil || projected.Version != 1 || projected.Account != account ||
		(projected.Status != "available" && projected.Status != "unreadable") {
		return false
	}
	canonical, err := json.Marshal(projected)
	return err == nil && bytes.Equal(canonical, raw)
}

func projectLegacySurfSettings(raw []byte, account string) ([]byte, error) {
	var old struct {
		Settings *struct {
			legacySurfSettings
			Discovery *struct {
				legacySurfDiscovery
				Sources []struct {
					legacySurfSource
					URL string `json:"url"`
				} `json:"sources"`
			} `json:"discovery"`
			Threads []string          `json:"threads"`
			Grants  []json.RawMessage `json:"interaction_grants"`
		} `json:"settings"`
	}
	result := legacySurfProjection[*legacySurfSettings]{Version: 1, Account: account, Status: "unreadable"}
	if json.Unmarshal(raw, &old) != nil || old.Settings == nil {
		return json.Marshal(result)
	}
	settings := old.Settings.legacySurfSettings
	if settings.MinMinutes < 15 || settings.MinMinutes > 1440 {
		return json.Marshal(result)
	}
	if settings.MaxMinutes == 0 {
		settings.MaxMinutes = min(1440, settings.MinMinutes*2)
	}
	if settings.MaxMinutes < settings.MinMinutes || settings.MaxMinutes > 1440 ||
		settings.DailyLimit < 1 || settings.DailyLimit > 24 || len(settings.Topics) == 0 || len(settings.Topics) > 20 {
		return json.Marshal(result)
	}
	settings.TargetCount, settings.InteractionCount = len(old.Settings.Threads), len(old.Settings.Grants)
	if discovery := old.Settings.Discovery; discovery != nil {
		clean := discovery.legacySurfDiscovery
		clean.Weights = make(map[string]int)
		for _, key := range []string{"recommendations", "community", "subscriptions", "related", "search"} {
			if value, ok := discovery.Weights[key]; ok && value >= 0 && value <= 100 {
				clean.Weights[key] = value
			}
		}
		for _, source := range discovery.Sources {
			source.Site = legacySurfSite(source.URL)
			clean.Sources = append(clean.Sources, source.legacySurfSource)
		}
		settings.Discovery = &clean
	}
	result.Status, result.Data = "available", &settings
	return json.Marshal(result)
}

func projectLegacySurfHistory(raw []byte, account string) ([]byte, error) {
	type item struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		URL   string `json:"url"`
		Kind  string `json:"kind"`
	}
	var old struct {
		Version  int `json:"version"`
		Sessions []struct {
			Account      string    `json:"account"`
			Thread       string    `json:"thread"`
			At           time.Time `json:"at"`
			State        string    `json:"state"`
			Interest     string    `json:"interest"`
			Candidates   []item    `json:"candidates"`
			Observations []struct {
				legacySurfObservation
				URL string `json:"url"`
			} `json:"observations"`
			Visited  []string `json:"visited"`
			Failures []string `json:"failures"`
			Selected string   `json:"selected"`
		} `json:"sessions"`
	}
	result := legacySurfProjection[[]legacySurfSession]{Version: 1, Account: account, Status: "unreadable"}
	if json.Unmarshal(raw, &old) != nil || old.Version != 1 || len(old.Sessions) > 120 {
		return json.Marshal(result)
	}
	result.Status, result.Data = "available", []legacySurfSession{}
	for _, row := range old.Sessions {
		private := row.Thread == "host:surf:"+account
		if row.Account != account || (!private && !identity.ValidPathID(row.Thread)) || row.At.IsZero() {
			continue
		}
		session := legacySurfSession{Thread: row.Thread, At: row.At, State: row.State, Visited: len(row.Visited), Failures: len(row.Failures)}
		if private {
			session.Interest = row.Interest
		}
		for _, entry := range row.Candidates {
			selected := entry.ID != "" && entry.ID == row.Selected
			session.Candidates = append(session.Candidates, legacySurfCandidate{
				Title: entry.Title, Kind: entry.Kind, Site: legacySurfSite(entry.URL),
				Bookmarked: selected && row.State == "bookmarked", Selected: selected,
			})
		}
		for _, entry := range row.Observations {
			entry.Site = legacySurfSite(entry.URL)
			session.Observations = append(session.Observations, entry.legacySurfObservation)
		}
		result.Data = append(result.Data, session)
	}
	slices.SortStableFunc(result.Data, func(a, b legacySurfSession) int { return b.At.Compare(a.At) })
	return json.Marshal(result)
}

func legacySurfSite(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

func (s *stationWechat) legacySurf(w http.ResponseWriter, r *http.Request, archiveOnly bool) {
	w.Header().Set("Cache-Control", "no-store")
	query := r.URL.Query()
	part := query.Get("part")
	limit := 10
	if raw := query.Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if part != "history" || err != nil || limit < 1 || limit > 120 {
			websession.Fail(w, http.StatusBadRequest, "每页请输入 1 到 120 条")
			return
		}
	}
	after := query.Get("after")
	if (part != "" && part != "history") || (after != "" && part != "history") {
		websession.Fail(w, http.StatusBadRequest, "旧冲浪档案查询参数无效")
		return
	}
	id := r.PathValue("connection")
	archive, err := s.legacyReceiptArchive(id)
	if err != nil {
		legacyImportFailure(w, err)
		return
	}
	root, source, err := s.openLegacyImportSource(legacySurfSourceFile)
	supplement := !errors.Is(err, errLegacyImportNotConfigured)
	if !supplement {
		root, source, err = s.openLegacyImport()
	}
	if err != nil {
		legacyImportFailure(w, err)
		return
	}
	defer root.Close()
	if source.guard == nil || (supplement && source.ManifestDigest == "") || (archive.SnapshotManifestDigest != source.guard.digest() &&
		(!supplement || !legacySurfSupplementMatches(source.guard, archive.SnapshotManifestDigest))) {
		legacyImportFailure(w, errLegacySnapshotIntegrity)
		return
	}
	account := archive.Plan.Account
	config, _, err := legacyImportAccountPaths(root, source, account)
	if err != nil {
		legacyImportFailure(w, err)
		return
	}
	path := func(leaf string) string { return filepath.ToSlash(filepath.Join(filepath.Dir(config), leaf)) }
	read := func(leaf string, max int64, projection any) (bool, error) {
		name := path(leaf)
		if _, ok := source.guard.entry(name); !ok {
			return false, nil
		}
		data, err := source.guard.readFile(root, name, max)
		if err != nil {
			return false, err
		}
		if json.Unmarshal(data, projection) != nil {
			return false, errLegacySnapshotIntegrity
		}
		return true, nil
	}
	if part == "" {
		var settings legacySurfProjection[*legacySurfSettings]
		found, err := read(legacySurfSettingsFile, legacySurfSettingsMax, &settings)
		if err != nil || found && (settings.Version != 1 || settings.Account != account) {
			legacyImportFailure(w, errLegacySnapshotIntegrity)
			return
		}
		if !found {
			settings.Status = "missing"
		}
		_, history := source.guard.entry(path(legacySurfHistoryFile))
		websession.Respond(w, http.StatusOK, map[string]any{
			"schedule": "paused", "settings_status": settings.Status, "settings": settings.Data, "history_available": history,
		})
		return
	}
	var history legacySurfProjection[[]legacySurfSession]
	found, err := read(legacySurfHistoryFile, legacySurfHistoryMax, &history)
	if err != nil || found && (history.Version != 1 || history.Account != account) {
		legacyImportFailure(w, errLegacySnapshotIntegrity)
		return
	}
	if !found {
		legacyImportFailure(w, apphost.ErrNotFound)
		return
	}
	var grants map[string]time.Time
	if !archiveOnly {
		worker, err := s.worker(id, false)
		if err != nil {
			legacyImportFailure(w, err)
			return
		}
		if worker != nil {
			worker.mu.Lock()
			defer worker.mu.Unlock()
			grants = worker.policy.HistoryGrants
		}
	}
	rows := []legacySurfSession{}
	for _, row := range history.Data {
		if row.Thread == "host:surf:"+account {
			row.Thread = ""
		} else if grants[row.Thread].IsZero() {
			continue
		}
		rows = append(rows, row)
	}
	revision := localCheckpointKey([]any{id, source.guard.digest(), grants})
	offset := 0
	if after != "" {
		version, position, _ := strings.Cut(after, ":")
		offset, err = strconv.Atoi(position)
		if err != nil || version != revision || offset < 1 || offset >= len(rows) {
			legacyImportFailure(w, errLegacyImportChanged)
			return
		}
	}
	end := min(offset+limit, len(rows))
	next := ""
	if end < len(rows) {
		next = revision + ":" + strconv.Itoa(end)
	}
	websession.Respond(w, http.StatusOK, map[string]any{"status": history.Status, "sessions": rows[offset:end], "limit": limit, "next_after": next})
}
