package admin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/plugins"
)

type mcpSettings struct {
	Enabled          bool                    `json:"enabled"`
	WebSearchEnabled bool                    `json:"web_search_enabled"`
	WebFetchEnabled  bool                    `json:"web_fetch_enabled"`
	MaxToolCalls     int                     `json:"max_tool_calls"`
	Servers          []mcptools.ServerConfig `json:"servers"`
}

func mcpDefaults() mcpSettings { return mcpSettings{true, true, true, 3, []mcptools.ServerConfig{}} }
func loadMCP(dir string) (mcpSettings, error) {
	repository, err := newExternalConfigurationRepository(dir, "")
	if err != nil {
		return mcpSettings{}, err
	}
	return repository.snapshot().Settings, nil
}
func validateMCP(c mcpSettings) error {
	if c.MaxToolCalls < 1 || c.MaxToolCalls > 5 {
		return errors.New("每次回复工具次数应为 1 至 5")
	}
	if _, err := mcptools.New(c.Servers); err != nil {
		return errors.New("MCP 服务配置无效，请检查 ID、命令、协议与地址")
	}
	for _, server := range c.Servers {
		if len(server.Name) > 128 || len(server.Command) > 2048 || len(server.Args) > 64 || len(server.Env) > 32 || len(server.Headers) > 16 {
			return errors.New("MCP 配置超过限制")
		}
		for _, arg := range server.Args {
			if len(arg) > 4096 || strings.ContainsRune(arg, '\x00') {
				return errors.New("MCP 参数无效")
			}
		}
		for key, value := range server.Env {
			if key == "" || strings.ContainsAny(key, "=\x00\r\n") || strings.ContainsRune(value, '\x00') || len(value) > 8192 {
				return errors.New("MCP 环境变量无效")
			}
		}
		for key, value := range server.Headers {
			if key == "" || strings.ContainsAny(key+value, "\x00\r\n") || len(value) > 8192 || strings.EqualFold(key, "host") {
				return errors.New("MCP 请求头无效")
			}
		}
	}
	return nil
}
func cloneMCP(c mcpSettings) mcpSettings {
	b, _ := json.Marshal(c)
	var out mcpSettings
	_ = json.Unmarshal(b, &out)
	return out
}
func redactedMCP(c mcpSettings) mcpSettings {
	c = cloneMCP(c)
	for i := range c.Servers {
		c.Servers[i].Env = map[string]string{}
		c.Servers[i].Headers = map[string]string{}
	}
	return c
}

type permittedTools struct {
	privateSurfOrigin    *privateSurfOrigin
	outboundReviewer     companion.LanguageModel
	discoveryReader      func(context.Context, mcptools.DiscoverySource, string) ([]mcptools.DiscoveryItem, error)
	webPageReader        func(context.Context, string, int64, time.Duration) ([]byte, string, string, error)
	trace                companion.Input
	hub                  *mcptools.Hub
	settings             mcpSettings
	mcpSourceHash        string
	runtime              *plugins.Runtime
	apiNames             map[string]bool
	apiKinds             map[string]string
	manager              *Manager
	mediaMu              sync.Mutex
	selectedVoiceID      string
	selectedVoiceProfile string
	media                map[string]companion.GeneratedMedia
	mediaOrigin          map[string]string
	songFollowups        []string
	privateDiagnostics   bool
	// Minted only after a trusted owner actually reads another conversation
	// from a group. The final text is delivered to the owner privately while
	// retaining the real source conversation for authorization checks.
	privateReplyTarget   string
	privateReplyRequired bool
	recoveryRouteErr     error
	// Threads become eligible as task sources only after a successful readonly
	// tool result. A model-supplied thread_id is never enough on its own.
	crossSourceThreads  map[string]bool
	crossSourceMessages map[string][]string
}

func (p *permittedTools) SetTrace(input companion.Input) {
	p.trace = input
	p.songFollowups = nil
	p.privateDiagnostics = false
	p.privateReplyTarget = ""
	p.privateReplyRequired = false
	p.recoveryRouteErr = nil
	p.crossSourceThreads = map[string]bool{}
	p.crossSourceMessages = map[string][]string{}
	if input.RecoveryPrivacy != nil && input.RecoveryPrivacy.Required {
		p.recoveryRouteErr = p.restoreRecoveryPrivacyBinding(*input.RecoveryPrivacy)
	}
}

func (p *permittedTools) ForkTools() companion.ToolAccess {
	// Hub and plugin Runtime are immutable after construction. Mutable per-call
	// trace/media state must never be shared between simultaneous conversations.
	return &permittedTools{hub: p.hub, settings: p.settings, runtime: p.runtime, mcpSourceHash: p.mcpSourceHash,
		privateSurfOrigin: p.privateSurfOrigin,
		outboundReviewer:  p.outboundReviewer,
		apiNames:          p.apiNames, apiKinds: p.apiKinds, manager: p.manager,
		discoveryReader: p.discoveryReader, webPageReader: p.webPageReader, media: map[string]companion.GeneratedMedia{}, mediaOrigin: map[string]string{}, crossSourceThreads: map[string]bool{}, crossSourceMessages: map[string][]string{}}
}

func (p *permittedTools) enabled(name string) bool {
	if p.privateSurfOrigin != nil && (!privateSurfTool(name) || p.privateSurfOrigin.check(context.Background(), p.trace) != nil) {
		return false
	}
	if p.trace.ReportOnly {
		return false
	}
	if name == "service_plugins" || name == "service_plugin_call" || name == "service_plugin_task" {
		return p.servicePluginsAllowed()
	}
	if capability := webServiceCapability(name); capability != "" {
		allowed := p.installedWebCapability(context.Background(), capability) == nil && p.servicePluginsAllowed() && p.serviceWebAllowed(capability)
		if name != "web_audio_search" {
			return allowed
		}
		permissions := p.manager.Config().Companion.Permissions
		return allowed && (permissions.SendVoice || permissions.SendFile)
	}
	if name == "tool_output_read" {
		return p.outputScope() != ""
	}
	if name == "video_inspect" {
		return len(p.videoTools()) > 0
	}
	if strings.HasPrefix(name, "device_") {
		return p.mobileToolEnabled(name)
	}
	if name == "plugin_job_status" || name == "plugin_job_cancel" {
		return p.pluginTaskAllowed()
	}
	if name == "music_rvc_start" || name == "music_sampling_options" || name == "music_local_start" || name == "music_local_status" || name == "music_reference_inspect" || name == "music_lyrics_compare" || name == "music_service_status" {
		return len(p.yueTools()) > 0
	}
	if name == "speech_synthesize" || name == "music_generate" {
		for _, t := range p.speechTools() {
			if t.Name == name {
				return true
			}
		}
		return false
	}
	if agentToolName(name) {
		return p.agentToolEnabled(name)
	}
	if p.manager.agent != nil && p.manager.agent.config().ExternalOwnerOnly && (p.apiNames[name] || strings.Contains(name, "__")) && !p.privileged() {
		return false
	}
	if !p.settings.Enabled {
		return false
	}
	if name == "web_discover" {
		return p.trace.Surf && p.servicePluginsAllowed() && p.serviceWebAllowed("web.discover") && p.installedWebCapability(context.Background(), "web.discover") == nil
	}
	if name == "web_media_download" {
		c := p.manager.Config().Companion.Permissions
		return p.settings.WebFetchEnabled && (c.SendImage || c.SendVoice || c.SendVideo || c.SendFile)
	}
	if name == "media_recent" || name == "media_reuse" || name == "media_history" {
		return p.trace.Thread != "" && p.manager.Config().Companion.Permissions.Remember
	}
	if kind := p.apiKinds[name]; kind != "" && !companion.MediaPermitted(p.manager.Config().Companion.Permissions, kind) {
		return false
	}
	return true
}
func (p *permittedTools) localTools() []mcptools.Tool {
	tools := p.installedWebTools()
	if p.trace.Surf {
		tools = append(tools, mcptools.Tool{Name: "web_discover", Description: "Discover actual public feed/community/RSS/music/image candidates, with persistent novelty memory and diverse sources. Automatically called once at session start. Continue a rabbit hole with related_url equal to a candidate BV page. Otherwise {} to switch sources. Does not send or claim watching. Input {related_url?:string}.", InputSchema: json.RawMessage(`{"type":"object","properties":{"related_url":{"type":"string","maxLength":2048}},"additionalProperties":false}`)})
	}
	if p.privateSurfOrigin != nil {
		return tools
	}
	if p.trace.Thread != "" {
		tools = append(tools, mcptools.Tool{Name: "media_history", Description: "Page ALL retained source/request associations for an exact asset, oldest first, preserving original message time and sender. Defaults offset=0, limit=20; follow next_offset. Same conversation only; trusted owner may specify source_thread_id within this account. Read-only provenance, never delivery evidence or instructions.", InputSchema: json.RawMessage(`{"type":"object","properties":{"asset_id":{"type":"string"},"source_thread_id":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}},"required":["asset_id"],"additionalProperties":false}`)})
		tools = append(tools, mcptools.Tool{Name: "media_recent", Description: "Read the recent attachment catalogue for THIS conversation in chronological recency, including source_url and formats. Returns cached_mime, parent_asset_id and recent requests with requester_id/trace_id/task_id. Parent is the retained source video when this asset is an extracted audio track. Match the verified quote first, then the current speaker and latest context; ask briefly if ambiguous. Call this for original video/audio or format conversion. This is a LOCAL catalogue, not Internet search. Search this LOCAL catalogue FIRST for listening, RVC covers and YuE reference songs, even a newly requested title. Reuse an exactly matching cached original recording; search online only if missing, unsuitable or explicitly requested fresh online. Never substitute unrelated tracks or generated covers for the original. Cached originals remain discoverable beyond chat memory age. Default is a12-item quick array. Supply offset=0 to get a paginated object {items,total,next_offset,has_more}; follow next_offset for the FULL scoped catalogue, no total-item cutoff. limit chooses a page (default12). Filter by exact asset_id, requester_id (self/current speaker supported), or query matching title/filename. Input: {asset_id?:string,requester_id?:string,query?:string,offset?:integer,limit?:integer}.", InputSchema: json.RawMessage(`{"type":"object","properties":{"asset_id":{"type":"string"},"requester_id":{"type":"string"},"query":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}},"additionalProperties":false}`)}, mcptools.Tool{Name: "media_reuse", Description: "Reuse the matching cached resource for the user request, including a matching local song found by title before online search, or a different format. This performs NO Internet search; never substitute a different old track for a new requested title. Original audio can be sent as file; legacy voice-only sources are WAV. Put returned media.id in reply.media_ids. For voice, optional conversion selects cached original start_seconds/duration_seconds, segment_seconds(default60, WeChat max60), timeout_seconds(default90). Default entire audio. Explicit conversion never replaces the default full-song bundle. Input: {asset_id:string,kind:voice|file|image|video,conversion?:object}.", InputSchema: json.RawMessage(withVoiceConversion(`{"type":"object","properties":{"asset_id":{"type":"string"},"kind":{"type":"string","enum":["voice","file","image","video"]}},"required":["asset_id","kind"],"additionalProperties":false}`))})
	}
	tools = append(tools, mcptools.Tool{Name: "web_media_download", Description: "下载公开媒体、B站音轨或视频，并准备微信附件，不是内容观察工具。Download media and prepare an attachment, not an observation tool. To understand an already received video/share use video_inspect with its message_id or cached asset_id; info/frames/audio return actual observations without uploading. Public Kuaishou/chenzhongtech video pages are also supported for video, voice and file. For kind=video, accepts direct video URLs AND anonymously playable Bilibili BV video-page URLs (optional ?p=part) or b23.tv short links; resolves the public progressive MP4 itself, so try the actual BV page instead of searching for guessed mp4 links. Videos longer than 60 seconds are prepared as clickable WeChat share cards with the actual playback page, without downloading the whole video. Short files are limited to 50 MiB. For kind=voice, also accepts these public BV/b23 pages and extracts the existing audio into voice notes, limited to 10 minutes and 25 MiB input; the reusable original is an audio-only M4A file. No paid/logged-in playback. Image/file otherwise need direct files. Does not send until final reply.media_ids. No login, paid formats, DRM or general streaming playlists. For music, DEFAULT preview_seconds=0 sends the COMPLETE audio as voice notes of at most60 seconds. Keep original file cached, but never ask about sending a file; use file only when user asks. Explicit preview requests may set preview_seconds=45. For kind=file on BV pages, audio_only=true gives the full original audio M4A, false gives the original video file. Input: {url:string,kind:voice|video|image|file,preview_seconds?:integer 0..900,audio_only?:boolean}.", InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","maxLength":2048},"kind":{"type":"string","enum":["voice","video","image","file"]},"preview_seconds":{"type":"integer","minimum":0,"maximum":900},"audio_only":{"type":"boolean"}},"required":["url","kind"],"additionalProperties":false}`)})
	tools = append(tools, p.installedExternalHTTPTools(context.Background())...)
	tools = append(tools, p.installedExternalJobTools(context.Background())...)
	tools = append(tools, p.installedExternalPollTools(context.Background())...)
	for _, module := range p.capabilities() {
		tools = append(tools, module.list()...)
	}
	return tools
}
func (p *permittedTools) List(ctx context.Context) ([]mcptools.Tool, error) {
	if p.trace.ReportOnly {
		return nil, nil
	}
	ctx = p.mcpContext(ctx)
	var tools []mcptools.Tool
	var err error

	if p.settings.Enabled && p.privateSurfOrigin == nil {
		tools, err = p.installedExternalMCPTools(ctx)
	}
	tools = append(tools, p.localTools()...)
	out := []mcptools.Tool{}
	indices := map[string]int{}
	for _, tool := range tools {
		if p.enabled(tool.Name) || p.externalMCPAdminListed(ctx, tool.Name) {
			if i, exists := indices[tool.Name]; exists {
				out[i] = tool
			} else {
				indices[tool.Name] = len(out)
				out = append(out, tool)
			}
		}
	}
	return out, err
}
func (p *permittedTools) Call(ctx context.Context, name string, args json.RawMessage) (result string, callErr error) {
	if p.privateSurfOrigin != nil {
		if !privateSurfTool(name) {
			return "", errors.New("surf_read_only_tool_denied")
		}
		if err := p.privateSurfOrigin.check(ctx, p.trace); err != nil {
			return "", err
		}
	}
	defer func() {
		if callErr != nil {
			p.privateDiagnostics = true
		}
	}()
	if strings.HasPrefix(name, "music_") || strings.HasPrefix(name, "workspace_") || name == "wechat_recall" {
		p.privateDiagnostics = true
	}
	defer func() { p.rememberTaskAction(name, args, result, callErr) }()
	ctx = p.mcpContext(ctx)
	started := time.Now()
	var diagnosticMu sync.Mutex
	requests, retries, lastStatus := 0, 0, 0
	lastCode, lastStage := "", ""
	ctx = mcptools.WithWebObserver(ctx, func(d mcptools.WebAttempt) {
		diagnosticMu.Lock()
		defer diagnosticMu.Unlock()
		requests++
		if d.Attempt > 1 {
			retries++
		}
		if d.Code != "" {
			lastCode, lastStage, lastStatus = d.Code, d.Stage, d.HTTPStatus
		}
	})
	downloadBytes := int64(0)
	var webArgs struct {
		URL   string `json:"url"`
		Query string `json:"query"`
	}
	_ = json.Unmarshal(args, &webArgs)
	endpoint := ""
	if u, e := url.Parse(webArgs.URL); e == nil && u.Host != "" {
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		endpoint = u.String()
	}
	origin := "manual_tool"
	if p.trace.TraceID != "" {
		origin = "chat_tool"
	}
	if p.trace.Surf {
		origin = "surf"
	}
	if strings.HasPrefix(name, "web_") {
		p.manager.record(bot.Record{Event: "web_activity", CallID: started.Format(time.RFC3339Nano), Category: "tool", ThreadID: p.trace.Thread, MessageID: p.trace.TraceID, Tool: name, TargetURL: endpoint, Query: webArgs.Query, Origin: origin, Status: "running", Reason: "tool_started"})
	}
	defer func() {
		if callErr == nil && p.trace.Surf && name == "web_search" {
			extra, err := p.rememberSearchCandidates(result)
			if err != nil {
				callErr = err
				result = ""
			} else {
				result += extra
			}
		}
		if callErr == nil && p.trace.Surf && (name == "web_fetch" || name == "web_media_download") {
			if err := p.manager.surfHistory.visit(p.trace, webArgs.URL); err != nil {
				callErr = err
				result = ""
			}
		}
		if callErr == nil && name == "web_media_download" && p.trace.Surf {
			var prepared struct {
				Media []companion.GeneratedMedia `json:"media"`
			}
			_ = json.Unmarshal([]byte(result), &prepared)
			p.mediaMu.Lock()
			if p.mediaOrigin == nil {
				p.mediaOrigin = map[string]string{}
			}
			for _, m := range prepared.Media {
				p.mediaOrigin[m.ID] = webArgs.URL
			}
			p.mediaMu.Unlock()
		}
		if strings.HasPrefix(name, "web_") {
			status, reason := "succeeded", "tool_succeeded"
			category := "tool"
			if callErr != nil {
				status = "failed"
				category = "error"
				reason = mcptools.FailureDescription(callErr)
			}
			var media struct {
				CatalogCacheHit bool              `json:"catalog_cache_hit"`
				Media           []json.RawMessage `json:"media"`
				SourceCatalog   string            `json:"source_catalog"`
			}
			_ = json.Unmarshal([]byte(result), &media)
			if endpoint == "" && media.SourceCatalog != "" {
				endpoint = media.SourceCatalog
			}
			diagnosticMu.Lock()
			var web *mcptools.WebError
			if errors.As(callErr, &web) {
				if lastCode == "" {
					lastCode, lastStage, lastStatus = web.Code, web.Stage, web.Status
				} else if web.Code == "http_status" && web.Status == 0 && lastStatus > 0 {
					detail := *web
					detail.Status = lastStatus
					callErr = &detail
					reason = mcptools.FailureDescription(callErr)
				}
			}
			record := bot.Record{Event: "web_activity", CallID: started.Format(time.RFC3339Nano), Category: category, ThreadID: p.trace.Thread, MessageID: p.trace.TraceID, Tool: name, TargetURL: endpoint, Query: webArgs.Query, Origin: origin, Status: status, Reason: reason, DurationMS: time.Since(started).Milliseconds(), Bytes: downloadBytes, MediaCount: len(media.Media), ToolCacheHit: media.CatalogCacheHit, ToolRequests: requests, ToolRetries: retries, ToolHTTPStatus: lastStatus, ToolCode: lastCode, ToolStage: lastStage}
			diagnosticMu.Unlock()
			p.manager.record(record)
		}
		if callErr != nil && !strings.HasPrefix(name, "web_") {
			reason, code := safeToolFailure(callErr)
			if reason == "" {
				reason = mcptools.FailureDescription(callErr)
			}
			p.manager.record(bot.Record{ThreadID: p.trace.Thread, MessageID: p.trace.TraceID, Event: "tool_call_failed", Category: "error", Tool: name, ToolCode: code, Content: name, Reason: reason, Status: "failed", Origin: "tool"})
		}
		if callErr == nil && !strings.HasPrefix(name, "web_") {
			origin := "tool"
			if name == "media_recent" || name == "media_reuse" || name == "media_history" {
				origin = "local_asset"
			}
			p.manager.record(bot.Record{ThreadID: p.trace.Thread, MessageID: p.trace.TraceID, Event: "tool_call_succeeded", Category: "tool", Tool: name, Content: name, Effect: p.toolEffect(name, args, result), Status: "succeeded", Origin: origin})
		}
	}()
	if p.privateSurfOrigin != nil {
		defer func() {
			if callErr == nil {
				if err := p.privateSurfOrigin.check(ctx, p.trace); err != nil {
					result, callErr = "", err
				}
			}
		}()
	}
	if webServiceCapability(name) != "" || name == "web_discover" {
		capability := webServiceCapability(name)
		if name == "web_discover" {
			capability = "web.discover"
		}
		if err := p.installedWebCapability(ctx, capability); err != nil {
			return "", err
		}
	}
	if !p.enabled(name) {
		return "", &mcptools.WebError{Code: "ability_disabled"}
	}
	// Keep strict named-voice argument diagnostics visible to the Agent.  This
	// precedes generic schema normalization so an unsupported cloud-only field
	// never turns into an opaque schema failure or a durable side-effect claim.
	if err := p.preflightSpeechArgs(name, args); err != nil {
		return "", err
	}
	normalized, validationErr := p.normalizeToolArgs(name, args)
	if validationErr != nil {
		return "", validationErr
	}
	args = normalized
	if webServiceCapability(name) != "" {
		return p.callInstalledWeb(ctx, name, args)
	}
	for _, module := range p.capabilities() {
		if module.match(name) {
			return module.call(ctx, name, args)
		}
	}
	if name == "web_discover" {
		return p.discover(ctx, args)
	}
	if name == "media_history" {
		var in struct {
			AssetID string `json:"asset_id"`
			Source  string `json:"source_thread_id"`
			Offset  int    `json:"offset"`
			Limit   int    `json:"limit"`
		}
		if json.Unmarshal(args, &in) != nil {
			return "", toolInputError("invalid media history arguments")
		}
		if in.Limit == 0 {
			in.Limit = 20
		}
		if in.Source != "" && in.Source != p.trace.Thread {
			if !p.crossAllowed(in.Source, p.trace.Thread) {
				return "", toolInputError("history_source_not_authorized")
			}
			scoped := &permittedTools{manager: p.manager, trace: p.trace}
			scoped.trace.Thread = in.Source
			result, err := scoped.assetHistoryPage(in.AssetID, in.Offset, in.Limit)
			if err == nil {
				p.routeCrossConversationThread(in.Source)
			}
			return result, err
		}
		return p.assetHistoryPage(in.AssetID, in.Offset, in.Limit)
	}
	if name == "media_recent" {
		var input struct {
			AssetID     string `json:"asset_id"`
			RequesterID string `json:"requester_id"`
			Query       string `json:"query"`
			Offset      *int   `json:"offset"`
			Limit       int    `json:"limit"`
		}
		if json.Unmarshal(args, &input) != nil || len(input.AssetID) > 128 || len(input.RequesterID) > 256 {
			return "", errors.New("素材目录筛选参数无效")
		}
		if input.RequesterID == "self" {
			input.RequesterID = p.trace.Sender
			if input.RequesterID == "" {
				return "[]", nil
			}
		}
		if input.Offset != nil || input.Limit > 0 {
			offset := 0
			if input.Offset != nil {
				offset = *input.Offset
			}
			limit := input.Limit
			if limit == 0 {
				limit = 12
			}
			items, total, err := p.manager.findAssetPage(p.trace, input.AssetID, input.RequesterID, input.Query, offset, limit)
			if err != nil {
				return "", err
			}
			next := offset + len(items)
			b, _ := json.Marshal(map[string]any{"items": items, "total": total, "next_offset": next, "has_more": next < total})
			return string(b), nil
		}

		items, err := p.manager.findAssets(p.trace, input.AssetID, input.RequesterID, input.Query)
		if err != nil {
			return "", err
		}
		b, _ := json.Marshal(items)
		return string(b), nil
	}
	if name == "media_reuse" {
		var in struct {
			Conversion *voiceConversion `json:"conversion,omitempty"`
			AssetID    string           `json:"asset_id"`
			Kind       string           `json:"kind"`
		}
		if json.Unmarshal(args, &in) != nil {
			return "", errors.New("invalid media reuse arguments")
		}
		return p.reuseAssetConverted(ctx, in.AssetID, in.Kind, in.Conversion)
	}
	if name == "web_media_download" {
		var input struct {
			URL            string `json:"url"`
			Kind           string `json:"kind"`
			PreviewSeconds *int   `json:"preview_seconds,omitempty"`
			AudioOnly      *bool  `json:"audio_only,omitempty"`
		}
		if json.Unmarshal(args, &input) != nil || strings.TrimSpace(input.URL) == "" || len(input.URL) > 2048 {
			return "", &mcptools.WebError{Code: "url_required"}
		}
		// Enforce the selected attachment permission before any download/upload.
		if toolsLimit(input.Kind) == 0 || !companion.MediaPermitted(p.manager.Config().Companion.Permissions, input.Kind) {
			return "", &mcptools.WebError{Code: "media_permission_denied"}
		}
		preview := 0
		request := companion.UserRequestText(p.trace.Text)
		if input.PreviewSeconds != nil {
			preview = *input.PreviewSeconds
		} else {
			preview = defaultAudioPreview(request)
		}
		if preview < 0 || preview > 900 {
			return "", &mcptools.WebError{Code: "audio_preview_invalid"}
		}
		if input.Kind == "video" && mcptools.IsPublicVideoPage(input.URL) {
			video, err := mcptools.DownloadPublicVideoPage(ctx, input.URL, toolsLimit("video"))
			if err != nil {
				return "", err
			}
			if video.LinkOnly {
				return p.stageVideoShare(video.PageURL, video.Title, video.DurationMS)
			}
			downloadBytes = int64(len(video.Data))
			data, err := preparePublicVideo(ctx, p.manager.dir, video.Data)
			if err != nil {
				return "", err
			}
			filename := video.Title + ".mp4"
			if !safeMediaFilename(filename) {
				filename = "public-video.mp4"
			}
			return p.stageOutputWithAsset(ctx, plugins.Output{Text: "已取得公开可播放的视频文件，尚未发送。来源：" + video.PageURL, Media: []plugins.MediaResult{{Kind: "video", Data: data, MIMEType: "video/mp4", Filename: filename}}}, &assetOriginal{Data: video.Data, MIME: video.ContentType, URL: video.PageURL, Filename: filename})
		}
		if (input.Kind == "voice" || input.Kind == "file") && mcptools.IsPublicVideoPage(input.URL) {
			video, err := mcptools.DownloadPublicVideoAudio(ctx, input.URL, toolsLimit(input.Kind))
			if err != nil {
				return "", err
			}
			downloadBytes = int64(len(video.Data))
			audioOnly := input.Kind == "voice"
			if input.AudioOnly != nil {
				audioOnly = *input.AudioOnly
			}
			if input.Kind == "file" && !audioOnly {
				filename := video.Title + ".mp4"
				if !safeMediaFilename(filename) {
					filename = "public-video.mp4"
				}
				return p.stageOutputWithAsset(ctx, plugins.Output{Text: "原视频文件已取得，尚未发送。", Media: []plugins.MediaResult{{Kind: "file", Data: video.Data, MIMEType: video.ContentType, Filename: filename}}}, &assetOriginal{Data: video.Data, MIME: video.ContentType, URL: video.PageURL, Filename: filename})
			}
			audio, err := extractPublicAudioFile(ctx, p.manager.dir, video.Data)
			if err != nil {
				return "", err
			}
			filename := video.Title + ".m4a"
			if !safeMediaFilename(filename) {
				filename = "public-audio.m4a"
			}
			if input.Kind == "file" {
				return p.stageOutputWithAsset(ctx, plugins.Output{Text: "已提取完整原音轨文件，尚未发送。来源：" + video.PageURL, Media: []plugins.MediaResult{{Kind: "file", Data: audio, MIMEType: "audio/mp4", Filename: filename}}}, &assetOriginal{VoiceComplete: preview == 0, Data: audio, MIME: "audio/mp4", URL: video.PageURL, Filename: filename, Parent: &assetOriginal{Data: video.Data, MIME: video.ContentType, URL: video.PageURL, Filename: video.Title + ".mp4"}})
			}
			output, err := preparePublicAudioPreview(ctx, p.manager.dir, audio, preview)
			if err != nil {
				return "", err
			}
			output.Text += " 视频标题：" + video.Title + "。来源：" + video.PageURL + "。这是视频中原有音轨，不是角色自己演唱；视频标题不保证原唱或完整曲目，须按本条需求核对。"
			for i := range output.Media {
				name := video.Title + ".wav"
				if len(output.Media) > 1 {
					name = fmt.Sprintf("%s-%d.wav", video.Title, i+1)
				}
				if safeMediaFilename(name) {
					output.Media[i].Filename = name
				}
			}
			return p.stageOutputWithAsset(ctx, output, &assetOriginal{VoiceComplete: preview == 0, Data: audio, MIME: "audio/mp4", URL: video.PageURL, Filename: filename, Parent: &assetOriginal{Data: video.Data, MIME: video.ContentType, URL: video.PageURL, Filename: video.Title + ".mp4"}})
		}
		data, typ, err := mcptools.GetPublicMedia(ctx, strings.TrimSpace(input.URL), toolsLimit(input.Kind))
		if err != nil {
			return "", err
		}
		downloadBytes = int64(len(data))
		typ, err = mcptools.ValidateMediaType(input.Kind, typ, data)
		if err != nil {
			return "", err
		}
		original := &assetOriginal{Data: data, MIME: typ, URL: input.URL}
		if input.Kind == "video" {
			seconds, probeErr := publicVideoDuration(ctx, p.manager.dir, data)
			if probeErr != nil {
				return "", probeErr
			}
			if seconds > 60 {
				return p.stageVideoShare(input.URL, assetName(*original), int64(seconds*1000))
			}
			data, err = preparePublicVideo(ctx, p.manager.dir, data)
			if err != nil {
				return "", err
			}
			typ = "video/mp4"
		}
		if input.Kind == "voice" {
			output, err := preparePublicAudioPreview(ctx, p.manager.dir, data, preview)
			if err != nil {
				return "", err
			}
			original.VoiceComplete = preview == 0
			return p.stageOutputWithAsset(ctx, output, original)
		}
		return p.stageOutputWithAsset(ctx, plugins.Output{Text: "公开媒体已下载，尚未发送。", Media: []plugins.MediaResult{{Kind: input.Kind, Data: data, MIMEType: typ}}}, original)
	}
	if p.apiNames[name] {
		if p.runtime.Background(name) || p.externalPollOnly(name) {
			return p.startPluginJob(ctx, name, args)
		}
		output, err := p.callExternalHTTP(ctx, name, args)
		if err != nil {
			return "", err
		}
		return p.stageOutput(ctx, output)
	}
	return p.callExternalMCP(ctx, name, args)
}

func (p *permittedTools) Media(id string) (companion.GeneratedMedia, bool) {
	p.mediaMu.Lock()
	defer p.mediaMu.Unlock()
	out, ok := p.media[id]
	if !ok {
		// Accept an unambiguous asset alias only among attachments prepared in
		// THIS model run. Never resurrect an unstaged historic/cross-thread asset.
		matches := 0
		for _, item := range p.media {
			if item.AssetID != "" && item.AssetID == id {
				out = item
				matches++
			}
		}
		ok = matches == 1
	}
	return out, ok
}
func (p *permittedTools) ResetMedia() {
	p.mediaMu.Lock()
	defer p.mediaMu.Unlock()
	p.media = map[string]companion.GeneratedMedia{}
	p.mediaOrigin = map[string]string{}
	p.selectedVoiceID = ""
	p.selectedVoiceProfile = ""
}
func (p *permittedTools) stageOutput(ctx context.Context, output plugins.Output) (string, error) {
	return p.stageOutputWithAsset(ctx, output, nil)
}
func (p *permittedTools) stageOutputWithAsset(ctx context.Context, output plugins.Output, original *assetOriginal) (string, error) {
	if err := checkOutputAuthority(ctx); err != nil {
		return "", err
	}
	native, ok := p.manager.native.(nativeTools)
	if len(output.Media) > 0 && !ok {
		return "", errors.New("微信附件后台不可用")
	}
	items := []companion.GeneratedMedia{}
	originals := []assetOriginal{}
	uploads := []stagedUpload{}
	permissions := p.manager.Config().Companion.Permissions
	for _, media := range output.Media {
		if !companion.MediaPermitted(permissions, media.Kind) {
			return "", errors.New("生成媒体能力已关闭")
		}
		limit := toolsLimit(media.Kind)
		if limit == 0 {
			return "", errors.New("工具返回的媒体类型不支持")
		}
		data, contentType := media.Data, media.MIMEType
		if len(data) == 0 && media.Base64Data != "" {
			var err error
			if int64(len(media.Base64Data)) > limit*4/3+4 {
				return "", errors.New("内联媒体超过限制")
			}
			data, err = base64.StdEncoding.DecodeString(media.Base64Data)
			if err != nil {
				return "", errors.New("内联媒体编码无效")
			}
		}
		if len(data) == 0 && media.URL != "" {
			var err error
			data, contentType, err = mcptools.GetPublicMedia(ctx, media.URL, limit)
			if err != nil {
				return "", errors.New("生成媒体下载失败")
			}
		}
		if len(data) == 0 || int64(len(data)) > limit {
			return "", errors.New("生成媒体为空或超过限制")
		}
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		extension := ".bin"
		if extensions, _ := mime.ExtensionsByType(strings.Split(contentType, ";")[0]); len(extensions) > 0 {
			extension = extensions[0]
		}
		filename := "generated-" + media.Kind + extension
		if safeMediaFilename(media.Filename) {
			filename = media.Filename
		}
		if err := checkOutputAuthority(ctx); err != nil {
			return "", err
		}
		source, err := native.Upload(ctx, filename, contentType, bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return "", errors.New("生成媒体暂存失败")
		}
		if err = checkOutputAuthority(ctx); err != nil {
			return "", err
		}
		if err = p.manager.tools.upload(stagedUpload{UploadSource: source, Account: p.manager.native.Account(), Kind: media.Kind}); err != nil {
			return "", err
		}
		item := companion.GeneratedMedia{ID: source.ID, Kind: media.Kind, Title: filename}
		if original != nil {
			item.URL = original.URL
			item.VoiceID, item.VoiceEngine, item.VoiceProfile = original.VoiceID, original.VoiceEngine, original.VoiceProfile
			item.SynthesisText = boundedAssetSynthesisText(original.SynthesisText)
			item.SynthesisTextKnown = original.SynthesisTextKnown && item.SynthesisText != ""
			if original.Music != nil {
				item.VoiceProfile = original.Music.VoiceProfile
			}
		}
		originalMedia := original
		if originalMedia == nil {
			originalMedia = &assetOriginal{VoiceComplete: media.Kind == "voice", Data: data, MIME: contentType, Filename: filename}
		}
		originals = append(originals, *originalMedia)
		uploads = append(uploads, stagedUpload{UploadSource: source, Account: p.trace.Account, Kind: media.Kind})
		items = append(items, item)
	}
	if err := checkOutputAuthority(ctx); err != nil {
		return "", err
	}
	if p.trace.Thread != "" && !p.trace.Preview && p.manager.Config().Companion.Permissions.Remember && len(items) > 0 {
		var shared mediaAsset
		var cacheErr error
		if original != nil {
			shared, cacheErr = p.manager.rememberAssetFormats(p.trace, *original, uploads)
			if cacheErr != nil {
				return "", cacheErr
			}
		}
		for i := range items {
			asset := shared
			if original == nil {
				asset, cacheErr = p.manager.rememberAssetFormats(p.trace, originals[i], uploads[i:i+1])
				if cacheErr != nil {
					return "", cacheErr
				}
			}
			items[i].URL, items[i].AssetID = asset.URL, asset.ID
			items[i].ParentAssetID, items[i].CachedMIME, items[i].Title = asset.ParentAssetID, asset.MIME, asset.Title
			items[i].VoiceID, items[i].VoiceEngine, items[i].VoiceProfile = asset.VoiceID, asset.VoiceEngine, asset.VoiceProfile
		}
	}
	p.mediaMu.Lock()
	for _, item := range items {
		p.media[item.ID] = item
	}
	p.mediaMu.Unlock()

	continuous := original != nil && strings.HasPrefix(original.MIME, "audio/") && len(items) > 1
	for _, item := range items {
		continuous = continuous && item.Kind == "voice"
	}
	required := []string{}
	if continuous {
		for _, item := range items {
			required = append(required, item.ID)
		}
	}
	p.manager.record(bot.Record{Event: "plugin_output_ready", Category: "tool", ThreadID: p.trace.Thread, MessageID: p.trace.TraceID, MediaCount: len(items), Status: "succeeded", Origin: "tool"})
	result, _ := json.Marshal(map[string]any{"required_media_ids": required, "text": output.Text, "media": items, "instruction": "媒体已准备好，但尚未发给对方。最终 reply.media_ids 必须取本轮 media[].id，不能使用 asset_id、文件名或job_id。asset_id仅用于以后复用原文件。不要向对方解释工具过程或内部编号。"})
	return string(result), nil
}
func (m *Manager) toolAccess() (*permittedTools, int, error) {
	m.mu.Lock()
	c := cloneMCP(m.mcp)
	m.mu.Unlock()
	steps := c.MaxToolCalls
	if m.agent != nil && m.agent.config().Enabled {
		steps = agentTurnToolLimit
	}
	installedRaw, err := json.Marshal(m.plugins.installed())
	if err != nil {
		return nil, 0, err
	}
	var installed []plugins.Installed
	if err = json.Unmarshal(installedRaw, &installed); err != nil {
		return nil, 0, err
	}
	runtime, err := plugins.New(installed)
	if err != nil {
		return nil, 0, err
	}
	servers, err := runtime.MCPServers()
	if err != nil {
		return nil, 0, err
	}
	allServers := append(c.Servers, servers...)
	hub, err := m.newMCPHub(allServers)
	if err != nil {
		return nil, 0, err
	}
	names := map[string]bool{}
	kinds := map[string]string{}
	for _, plugin := range installed {
		if plugin.Enabled {
			for tool, kind := range plugin.Manifest.ToolMediaKinds {
				kinds[plugin.Manifest.ID+"__"+tool] = kind
			}
			for _, tool := range plugin.Manifest.Tools {
				kinds[plugin.Manifest.ID+"__"+tool.Name] = tool.MediaKind
			}
		}
	}
	for _, tool := range runtime.Tools() {
		names[tool.Name] = true
	}
	return &permittedTools{hub: hub, settings: c, runtime: runtime, mcpSourceHash: externalMCPSourceHash(c, installed), apiNames: names, apiKinds: kinds, manager: m, media: map[string]companion.GeneratedMedia{}}, steps, nil
}
func (s *Server) mcpRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mcp", s.protected(func(w http.ResponseWriter, r *http.Request) {
		m := s.manager
		m.op.Lock()
		defer m.op.Unlock()
		if err := m.refreshExternalConfiguration(r.Context()); err != nil {
			fail(w, 503, "外部连接配置读取失败；原数据未覆盖")
			return
		}
		m.mu.Lock()
		c := redactedMCP(m.mcp)
		m.mu.Unlock()
		respond(w, 200, c)
	}))
	mux.HandleFunc("POST /api/mcp", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var input mcpSettings
		if !decode(w, r, &input) {
			return
		}
		m := s.manager
		m.op.Lock()
		defer m.op.Unlock()
		m.mu.Lock()
		old := cloneMCP(m.mcp)
		m.mu.Unlock()
		for i, server := range input.Servers {
			for _, prev := range old.Servers {
				if prev.ID != server.ID {
					continue
				}
				same := prev.Transport == server.Transport && prev.URL == server.URL && prev.Command == server.Command && reflect.DeepEqual(prev.Args, server.Args)
				if !same && (len(prev.Env) > 0 || len(prev.Headers) > 0) && (len(server.Env) == 0 && len(server.Headers) == 0) {
					fail(w, 400, "更换 MCP 地址或命令时，请重新填写对应密钥，防止旧凭据发往其他服务")
					return
				}
				if same {
					if len(server.Env) == 0 {
						input.Servers[i].Env = prev.Env
					}
					if len(server.Headers) == 0 {
						input.Servers[i].Headers = prev.Headers
					}
				}
			}
		}
		if err := validateMCP(input); err != nil {
			fail(w, 400, err.Error())
			return
		}
		runtime, pluginErr := plugins.New(m.plugins.installed())
		if pluginErr != nil {
			fail(w, 400, "插件配置无效")
			return
		}
		pluginServers, pluginErr := runtime.MCPServers()
		if pluginErr != nil {
			fail(w, 400, "插件 MCP 配置不完整")
			return
		}
		if _, pluginErr = mcptools.New(append(append([]mcptools.ServerConfig{}, input.Servers...), pluginServers...)); pluginErr != nil {
			fail(w, 400, "MCP 服务与已启用插件的 ID 冲突或超过上限")
			return
		}
		data, err := json.Marshal(input)
		if err != nil || len(data) > 64<<10 {
			fail(w, 400, "MCP 配置过大")
			return
		}
		c := m.runtimeConfig()
		restart := c.Enabled && c.Companion.Enabled
		if restart {
			if err = m.stop(); err != nil {
				fail(w, 409, err.Error())
				return
			}
			defer m.start(c)
		}
		if err = m.plugins.repository.SaveSettings(r.Context(), input); err != nil {
			if errors.Is(err, pluginhost.ErrRevision) || errors.Is(err, errExternalConfigurationChanged) {
				fail(w, 409, "外部配置已变化，请重新加载后再保存")
				return
			}
			fail(w, 500, "MCP 配置保存失败")
			return
		}
		m.mu.Lock()
		m.mcp = cloneMCP(input)
		m.mu.Unlock()
		m.log("mcp_configuration_saved")
		respond(w, 200, redactedMCP(input))
	}))
	mux.HandleFunc("GET /api/mcp/tools", s.protected(func(w http.ResponseWriter, r *http.Request) {
		access, _, err := s.manager.toolAccess()
		if err != nil {
			fail(w, 400, "MCP 配置无效")
			return
		}
		if access == nil {
			respond(w, 200, map[string]any{"items": []mcptools.Tool{}})
			return
		}
		defer access.hub.Close()
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		ctx = s.externalMCPAdminContext(ctx, r, "list", "")
		tools, listErr := access.List(ctx)
		out := map[string]any{"items": tools}
		if listErr != nil {
			out["error"] = "有外部服务无法连接；可用工具仍已列出，请检查命令/地址/凭据"
		}
		respond(w, 200, out)
	}))
	mux.HandleFunc("POST /api/mcp/test", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if !decode(w, r, &input) {
			return
		}
		access, _, err := s.manager.toolAccess()
		if err != nil || access == nil {
			fail(w, 400, "工具能力未开启或配置无效")
			return
		}
		defer access.hub.Close()
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
		defer cancel()
		var result string
		if access.apiNames[input.Name] && !access.runtime.Background(input.Name) && !access.externalLegacyHTTP(input.Name) {
			// Explicit authenticated manual API test: no incoming WeChat actor and no
			// destination conversation are invented for this management action.
			access.trace = companion.Input{Account: s.manager.native.Account()}
			ctx = s.externalHTTPTestContext(ctx, r, input.Name)
			result, err = access.callManualExternalHTTP(ctx, input.Name, input.Arguments)
		} else if !access.apiNames[input.Name] && strings.Contains(input.Name, "__") {
			access.trace = companion.Input{Account: s.manager.native.Account()}
			ctx = s.externalMCPAdminContext(ctx, r, "call", input.Name)
			result, err = access.callExternalMCP(ctx, input.Name, input.Arguments)
		} else {
			result, err = access.Call(ctx, input.Name, input.Arguments)
		}
		if err != nil {
			s.manager.log("mcp_manual_call_failed")
			detail := mcptools.FailureDescription(err)
			fail(w, 502, detail)
			return
		}
		s.manager.log("mcp_manual_call_succeeded")
		respond(w, 200, map[string]string{"name": input.Name, "result": result})
	}))
}
