package station

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/strictjson"
	"wechat-bot/internal/workspacefs"
	catalog "wechat-bot/plugins"
)

func surfInstallation(ctx context.Context, host *stationShareHost) (pluginhost.InstalledPackage, error) {
	item, err := host.store.Installed(ctx, stationSurfPlugin)
	contract, declared := catalog.ContractFor(stationSurfPlugin)
	if err != nil || declared != nil || !item.Enabled || item.Removed || item.Digest != contract.ManifestSHA256 || !slices.Contains(item.Permissions, "network") {
		return pluginhost.InstalledPackage{}, apphost.ErrLifecycleNotReady
	}
	return item, nil
}

type stationSurfTools struct {
	worker         *stationWechatWorker
	host           *stationShareHost
	account, trace string
	revision       int
	settings       stationSurfSettings
	installed      pluginhost.InstalledPackage
	active         func() error
	run            context.Context
	traceMatched   bool
	known          map[string]mcptools.DiscoveryItem
	read           map[string]bool
}

type stationSurfContextKey struct{}

func (s *stationWechat) prepareSurf(ctx context.Context, w *stationWechatWorker, state stationSurfSchedule, allowed func() error) (func(context.Context, string) (companion.ToolAccess, error), error) {
	root, err := workspacefs.OpenPath(filepath.Join(w.dir, "project-tools"))
	if err != nil {
		return nil, apphost.ErrLifecycleNotReady
	}
	root.Close()
	host, err := s.shareHost(w, state.Account)
	if err != nil {
		return nil, err
	}
	item, err := surfInstallation(ctx, host)
	if err != nil {
		return nil, err
	}
	// Capture installation identity at the beginning of the run.
	return func(ctx context.Context, trace string) (companion.ToolAccess, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return &stationSurfTools{worker: w, host: host, account: state.Account, trace: trace,
			revision: state.Revision, settings: state.Settings, installed: item, active: allowed,
			known: map[string]mcptools.DiscoveryItem{}, read: map[string]bool{}}, nil
	}, nil
}

func (t *stationSurfTools) ForkTools() companion.ToolAccess { return t }

func (t *stationSurfTools) SetTrace(in companion.Input) {
	t.traceMatched = in.Preview && in.Surf && !in.SurfForceShare && !in.ReportOnly &&
		in.Channel == "host" && in.Account == t.account && in.Thread == "host:surf:"+t.account &&
		in.TraceID == t.trace && in.Sender == "" && in.Mode == "direct" && !in.IsGroup &&
		in.Source == nil && in.QuotedSource == nil && in.ReplyContext == nil
}

func (t *stationSurfTools) MemoryResultAuthorization() string {
	if !t.traceMatched || t.run == nil || t.run.Err() != nil {
		return ""
	}
	return localCheckpointKey("station-surf", t.account, t.trace, t.revision, t.installed.Revision)
}

func (t *stationSurfTools) MemoryResultAuthorized(stamp string) bool {
	return stamp != "" && stamp == t.MemoryResultAuthorization()
}

func (t *stationSurfTools) BindSurfBrowseContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	t.worker.surfMu.Lock()
	defer t.worker.surfMu.Unlock()
	if err := t.active(); err != nil {
		return nil, nil, err
	}
	state, err := t.worker.surfSchedule(t.account)
	if err != nil || !state.Enabled || state.Revision != t.revision || ctx.Err() != nil || t.run != nil {
		return nil, nil, apphost.ErrLifecycleConflict
	}
	if t.worker.surfCancel != nil {
		t.worker.surfCancel()
	}
	state.LastRun = &stationSurfRun{Trace: t.trace, At: time.Now().UTC(), State: "started"}
	if err := t.worker.saveSurf(state); err != nil {
		return nil, nil, err
	}
	bounded, cancel := context.WithTimeout(context.WithValue(ctx, stationSurfContextKey{}, t), 5*time.Minute)
	t.run = bounded
	t.worker.surfCancel = cancel
	return bounded, cancel, nil
}

func (t *stationSurfTools) check(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if ctx.Value(stationSurfContextKey{}) != t || t.MemoryResultAuthorization() == "" {
		return apphost.ErrLifecycleConflict
	}
	if err := t.active(); err != nil {
		return err
	}
	t.worker.surfMu.Lock()
	defer t.worker.surfMu.Unlock()
	state, err := t.worker.surfSchedule(t.account)
	if err != nil || !state.Enabled || state.Revision != t.revision || state.LastRun == nil || state.LastRun.Trace != t.trace || state.Settings.clock().Quiet(time.Now()) {
		return apphost.ErrLifecycleConflict
	}
	current, err := surfInstallation(ctx, t.host)
	if err != nil || current.Revision != t.installed.Revision || current.Digest != t.installed.Digest {
		return apphost.ErrLifecycleNotReady
	}
	return ctx.Err()
}

func (t *stationSurfTools) List(ctx context.Context) ([]mcptools.Tool, error) {
	if err := t.check(ctx); err != nil {
		return nil, err
	}
	sources := []string{}
	for _, source := range t.settings.Sources {
		if source.Enabled {
			sources = append(sources, source.ID+"（"+source.Name+"）")
		}
	}
	return []mcptools.Tool{
		{Name: "web_discover", Description: "按已授权的来源编号发现公开候选。来源：" + strings.Join(sources, "、"), InputSchema: json.RawMessage(`{"type":"object","properties":{"source_id":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}},"required":["source_id"],"additionalProperties":false}`)},
		{Name: "web_fetch", Description: "用本轮实际发现的 candidate_id 读取公开正文。续读传 offset 和上页 sha256。", InputSchema: json.RawMessage(`{"type":"object","properties":{"candidate_id":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1},"expected_sha256":{"type":"string"}},"required":["candidate_id"],"additionalProperties":false}`)},
	}, nil
}

type stationSurfQuery struct {
	offset, limit                int
	url, expectedHash, candidate string
	source                       mcptools.DiscoverySource
}

func (t *stationSurfTools) Call(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	if err := t.check(ctx); err != nil {
		return "", err
	}
	var query stationSurfQuery
	var args map[string]any
	capability := "web.discover"
	switch name {
	case "web_discover":
		var input struct {
			Source string `json:"source_id"`
			Offset int    `json:"offset"`
			Limit  *int   `json:"limit"`
		}
		if strictjson.DecodeKnownFields(raw, &input) != nil || input.Offset < 0 || input.Limit != nil && *input.Limit < 1 {
			return "", errors.New("surf_discovery_arguments_invalid")
		}
		for _, source := range t.settings.Sources {
			if source.Enabled && source.ID == input.Source {
				query.source = source
				break
			}
		}
		if query.source.ID == "" {
			return "", errors.New("surf_source_not_authorized")
		}
		query.offset, query.limit, query.url = input.Offset, 12, query.source.URL
		if input.Limit != nil {
			query.limit = *input.Limit
		}
		args = map[string]any{"source": query.source, "offset": query.offset, "limit": query.limit}
	case "web_fetch":
		capability = "web.read"
		var input struct {
			Candidate string `json:"candidate_id"`
			Offset    int    `json:"offset"`
			Limit     *int   `json:"limit"`
			Hash      string `json:"expected_sha256"`
		}
		if strictjson.DecodeKnownFields(raw, &input) != nil || input.Offset < 0 || input.Limit != nil && *input.Limit < 1 ||
			(input.Offset > 0 || input.Hash != "") && !legacyMemoryDigestValid(input.Hash) {
			return "", errors.New("surf_read_arguments_invalid")
		}
		entry, ok := t.known[input.Candidate]
		if !ok {
			return "", errors.New("surf_candidate_not_in_this_run")
		}
		query.offset, query.limit, query.url, query.expectedHash, query.candidate = input.Offset, 4000, entry.URL, input.Hash, input.Candidate
		if input.Limit != nil {
			query.limit = *input.Limit
		}
		args = map[string]any{"url": query.url, "offset": query.offset, "limit": query.limit, "expected_sha256": query.expectedHash}
	default:
		return "", errors.New("surf_tool_unavailable")
	}
	wire, _ := json.Marshal(args)
	return t.invoke(ctx, capability, query, wire)
}

func (t *stationSurfTools) invoke(ctx context.Context, capability string, query stationSurfQuery, raw json.RawMessage) (string, error) {
	conf, err := t.host.store.Configuration(ctx, stationSurfPlugin)
	if err != nil {
		return "", err
	}
	inv := pluginhost.Invocation{
		Account: t.account, Thread: "host:surf:" + t.account, Requester: "host:surf", Purpose: "private public web browsing",
		RequestKey:    "surf-" + localCheckpointKey(t.trace, capability, string(raw), t.installed.Revision, conf.Revision),
		Authorization: t.trace, SourceVersions: map[string]int{t.trace: t.revision},
	}
	reader := t.host.surfRead
	if reader == nil {
		reader = mcptools.ReadPublicPluginPage
	}
	policy := &stationSurfCall{tools: t, capability: capability, query: query, invocation: inv, config: conf.Revision, arguments: raw, reader: reader}
	if err = t.host.controller.EnsureStarted(ctx, stationSurfPlugin, t.installed.Revision); err != nil {
		return "", err
	}
	call, err := t.host.controller.Invoke(ctx, pluginruntime.CallRequest{Plugin: stationSurfPlugin, Capability: capability, Context: inv, Arguments: raw}, policy)
	if err != nil || call.Job.State != "completed" {
		return "", errors.New("surf_public_read_not_completed")
	}
	result, err := t.host.store.PrivateJobResult(ctx, call.Job, func(ctx context.Context, j pluginhost.Job) error {
		return policy.Check(ctx, j, pluginhost.Capability{})
	})
	if err != nil {
		return "", err
	}
	if capability == "web.discover" {
		var page struct {
			Candidates []mcptools.DiscoveryItem `json:"candidates"`
		}
		if json.Unmarshal(result, &page) != nil {
			return "", errors.New("surf_discovery_result_invalid")
		}
		for _, candidate := range page.Candidates {
			t.known[candidate.ID] = candidate
		}
	} else {
		t.read[query.candidate] = true
	}
	return string(result), nil
}

func (t *stationSurfTools) ReviewSurf(ctx context.Context, decision companion.Decision) (companion.Decision, error) {
	out := companion.Decision{Action: "silent", Reason: "surf_browse_only"}
	if err := t.check(ctx); err != nil {
		return out, err
	}
	t.worker.surfMu.Lock()
	defer t.worker.surfMu.Unlock()
	state, err := t.worker.surfSchedule(t.account)
	if err != nil || !state.Enabled || state.Revision != t.revision || state.LastRun == nil || state.LastRun.Trace != t.trace || ctx.Err() != nil {
		return out, apphost.ErrLifecycleConflict
	}
	state.LastRun.State = "completed"
	state.LastRun.Candidates, state.LastRun.ReadCandidates = len(t.known), len(t.read)
	if decision.Action == "silent" && decision.SurfAction == "bookmark" && t.read[decision.SurfItemID] {
		state.LastRun.Bookmark = t.known[decision.SurfItemID].Title
		out.SurfAction, out.SurfItemID, out.Reason = "bookmark", decision.SurfItemID, "surf_bookmarked"
	}
	return out, t.worker.saveSurf(state)
}

type stationSurfCall struct {
	tools      *stationSurfTools
	capability string
	query      stationSurfQuery
	invocation pluginhost.Invocation
	config     int
	arguments  json.RawMessage
	reader     func(context.Context, string, int64, time.Duration) ([]byte, string, string, error)
	observed   string
}

func (p *stationSurfCall) Check(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability) error {
	var expected, actual any
	if j.Plugin != stationSurfPlugin || j.Tool != p.capability || cap.Async ||
		j.PackageDigest != p.tools.installed.Digest || j.Version != p.tools.installed.Package.Version ||
		j.ConfigRevision != strconv.Itoa(p.config) || !reflect.DeepEqual(j.Context, p.invocation) ||
		json.Unmarshal(p.arguments, &expected) != nil || json.Unmarshal(j.Arguments, &actual) != nil || !reflect.DeepEqual(actual, expected) {
		return errors.New("surf_public_invocation_changed")
	}
	if err := p.tools.check(ctx); err != nil {
		return err
	}
	conf, err := p.tools.host.store.Configuration(ctx, stationSurfPlugin)
	if err != nil || conf.Revision != p.config {
		return errors.New("surf_public_configuration_changed")
	}
	return nil
}

func (p *stationSurfCall) Service(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, method string, raw json.RawMessage) (json.RawMessage, error) {
	var request struct {
		URL     string `json:"url"`
		Max     int64  `json:"max_bytes"`
		Timeout int    `json:"timeout_seconds"`
	}
	timeout := 20
	if p.capability == "web.discover" {
		timeout = 12
	}
	if method != "wechat/http_read" || strictjson.DecodeKnownFields(raw, &request) != nil ||
		request.URL != p.query.url || request.Max != 2<<20 || request.Timeout != timeout || p.observed != "" {
		return nil, errors.New("surf_public_route_not_authorized")
	}
	if err := p.Check(ctx, j, cap); err != nil {
		return nil, err
	}
	body, mime, final, err := p.reader(ctx, request.URL, request.Max, time.Duration(timeout)*time.Second)
	if err != nil {
		return json.Marshal(map[string]any{"ok": false, "error": "surf_public_read_failed"})
	}
	if err := p.Check(ctx, j, cap); err != nil {
		return nil, err
	}
	p.observed = final
	return json.Marshal(map[string]any{"ok": true, "url": final, "content_type": mime, "body_base64": base64.StdEncoding.EncodeToString(body)})
}

func (p *stationSurfCall) Validate(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	if err := p.Check(ctx, j, cap); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	return p.validateResult(raw)
}

func (p *stationSurfCall) validateResult(raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	var out struct {
		OK              bool                     `json:"ok"`
		Source          string                   `json:"source"`
		URL             string                   `json:"url"`
		Text            string                   `json:"text"`
		Hash            string                   `json:"sha256"`
		Offset          int                      `json:"offset"`
		Limit           int                      `json:"requested_limit"`
		Next            *int                     `json:"next_offset"`
		Complete        bool                     `json:"complete"`
		TotalItems      int                      `json:"total_items"`
		TotalCharacters int                      `json:"total_characters"`
		Returned        int                      `json:"returned_characters"`
		MetadataOnly    bool                     `json:"metadata_only"`
		Candidates      []mcptools.DiscoveryItem `json:"candidates"`
		Coverage        struct {
			Kind     string `json:"kind"`
			Start    int    `json:"start_offset"`
			End      int    `json:"end_offset"`
			Total    int    `json:"total_characters"`
			Complete bool   `json:"complete"`
		} `json:"read_coverage"`
	}
	invalid := errors.New("surf_public_result_unverified")
	if json.Unmarshal(raw, &out) != nil {
		return pluginruntime.VerifiedResult{}, invalid
	}
	if !out.OK {
		return pluginruntime.VerifiedResult{Failed: true, Result: json.RawMessage(`{"error":"surf_public_read_failed"}`)}, nil
	}
	if p.observed == "" || out.Offset != p.query.offset || out.Limit != p.query.limit {
		return pluginruntime.VerifiedResult{}, invalid
	}
	view := map[string]any{"offset": out.Offset, "requested_limit": out.Limit, "next_offset": out.Next, "complete": out.Complete, "untrusted_external_content": true}
	if p.capability == "web.discover" {
		if out.Source != p.observed || !out.MetadataOnly || out.Text != "" || !surfCoverage(out.Offset, out.Limit, len(out.Candidates), out.TotalItems, out.Next, out.Complete) {
			return pluginruntime.VerifiedResult{}, invalid
		}
		for i := range out.Candidates {
			row := &out.Candidates[i]
			link, err := url.Parse(row.URL)
			if err != nil || link.Hostname() == "" || link.User != nil || (link.Scheme != "http" && link.Scheme != "https") ||
				row.Title == "" || row.Source != p.query.source.Name {
				return pluginruntime.VerifiedResult{}, invalid
			}
			row.ID, row.SourceID, row.MediaURL = "candidate-"+localCheckpointKey(p.query.source.ID, row.URL, row.Title)[:32], p.query.source.ID, ""
		}
		view["candidates"], view["total_items"], view["metadata_only"] = out.Candidates, out.TotalItems, true
		view["source"] = out.Source
	} else {
		if out.URL != p.observed || !legacyMemoryDigestValid(out.Hash) || p.query.expectedHash != "" && out.Hash != p.query.expectedHash ||
			out.Returned < 1 || out.Returned != utf8.RuneCountInString(out.Text) || !surfCoverage(out.Offset, out.Limit, out.Returned, out.TotalCharacters, out.Next, out.Complete) ||
			out.Coverage.Kind != "page_body" || out.Coverage.Start != out.Offset || out.Coverage.End != out.Offset+out.Returned || out.Coverage.Total != out.TotalCharacters || out.Coverage.Complete != out.Complete {
			return pluginruntime.VerifiedResult{}, invalid
		}
		if out.Offset == 0 && out.Complete && hexDigest([]byte(out.Text)) != out.Hash {
			return pluginruntime.VerifiedResult{}, invalid
		}
		view["candidate_id"], view["sha256"], view["text"], view["total_characters"], view["returned_characters"] = p.query.candidate, out.Hash, out.Text, out.TotalCharacters, out.Returned
		view["url"], view["read_coverage"] = out.URL, out.Coverage
	}
	private, err := json.Marshal(view)
	return pluginruntime.VerifiedResult{Result: json.RawMessage(`{"verified_public_read":true}`), Private: private}, err
}

func surfCoverage(offset, limit, returned, total int, next *int, complete bool) bool {
	if offset < 0 || limit < 1 || total < offset || returned != min(limit, total-offset) {
		return false
	}
	end := offset + returned
	return complete == (end == total) && (complete && next == nil || !complete && next != nil && *next == end)
}
