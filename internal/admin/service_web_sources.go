package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
)

type webDiscoveryArguments struct {
	Source  mcptools.DiscoverySource `json:"source"`
	Related string                   `json:"related_url,omitempty"`
	Offset  *int                     `json:"offset,omitempty"`
	Limit   *int                     `json:"limit,omitempty"`
}

// Source selection remains host policy; parsing public feed bytes belongs to
// the installed plugin. A generic service call cannot invent a subscription,
// switch a source URL, or use a related page outside the retained browse scope.
func (p *permittedTools) webDiscoveryRequest(raw json.RawMessage) (mcptools.DiscoverySource, string, error) {
	var in webDiscoveryArguments
	if strictServicePluginJSON(raw, &in) != nil || !p.trace.Surf || mcptools.ValidateDiscoverySource(in.Source) != nil || in.Offset != nil && *in.Offset < 0 || in.Limit != nil && *in.Limit < 1 {
		return in.Source, "", errors.New("web_discovery_source_unavailable")
	}
	p.manager.mu.Lock()
	settings := p.manager.surf.Settings.Discovery
	configured := false
	if settings == nil {
		settings = defaultDiscovery()
	}
	for _, source := range settings.Sources {
		configured = configured || source.Enabled && reflect.DeepEqual(source, in.Source)
	}
	maxDepth := settings.MaxDepth
	p.manager.mu.Unlock()
	if !configured {
		return in.Source, "", errors.New("web_discovery_source_changed")
	}
	rawURL := in.Source.URL
	if in.Related != "" {
		u, parseErr := url.Parse(in.Related)
		_, public := publicSearchURL(in.Related)
		if in.Source.Type != "bilibili" || !public || parseErr != nil || (u.Hostname() != "www.bilibili.com" && u.Hostname() != "bilibili.com") || !mcptools.IsPublicVideoPage(in.Related) || maxDepth < 1 {
			return in.Source, "", errors.New("web_discovery_related_unavailable")
		}
		store := p.manager.surfHistory
		store.mu.Lock()
		allowed := false
		for _, session := range store.data.Sessions {
			if session.ID == p.trace.TraceID && session.Thread == p.trace.Thread && session.Depth < maxDepth {
				for _, candidate := range session.Candidates {
					allowed = allowed || canonicalSurfURL(candidate.URL) == canonicalSurfURL(in.Related)
				}
			}
		}
		for _, item := range store.data.Items {
			allowed = allowed || (item.Bookmarked || item.Visits > 0) && canonicalSurfURL(item.Item.URL) == canonicalSurfURL(in.Related)
		}
		store.mu.Unlock()
		if !allowed {
			return in.Source, "", errors.New("web_discovery_related_unavailable")
		}
		id := strings.Trim(strings.TrimPrefix(u.Path, "/video/"), "/")
		rawURL = "https://api.bilibili.com/x/web-interface/archive/related?bvid=" + url.QueryEscape(id)
	}
	return in.Source, rawURL, nil
}

func (s servicePluginPolicy) validatePublicWebRead(ctx context.Context, j pluginhost.Job, rawURL string, maximum, timeout int64) error {
	var args struct {
		URL     string `json:"url"`
		Query   string `json:"query"`
		Offset  int    `json:"offset"`
		Max     *int64 `json:"max_bytes"`
		Timeout *int64 `json:"timeout_seconds"`
	}
	if json.Unmarshal(j.Arguments, &args) != nil {
		return errors.New("web_read_source_changed")
	}
	expectedMax, expectedTimeout := int64(2<<20), int64(20)
	if args.Max != nil {
		expectedMax = *args.Max
	}
	if args.Timeout != nil {
		expectedTimeout = *args.Timeout
	}
	allowed := false
	switch j.Tool {
	case "web.read":
		allowed = args.URL != "" && args.URL == rawURL
	case "web.search":
		var selection struct {
			Source string `json:"source"`
		}
		if json.Unmarshal(j.Arguments, &selection) != nil {
			return errors.New("web_read_source_changed")
		}
		if selection.Source == "bilibili" {
			u, err := url.Parse(rawURL)
			expected := url.Values{"keyword": {strings.TrimSpace(args.Query)}, "search_type": {"video"}, "page_size": {"20"}, "page": {strconv.Itoa(args.Offset/20 + 1)}}
			if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || args.Offset < 0 || strings.TrimSpace(args.Query) == "" {
				return errors.New("web_read_source_changed")
			}
			term := strings.TrimSpace(args.Query) + " site:bilibili.com/video"
			switch u.Host + u.EscapedPath() {
			case "api.bilibili.com/x/web-interface/search/type":
				allowed = u.Query().Encode() == expected.Encode()
			case "www.bing.com/search":
				allowed = u.Query().Encode() == (url.Values{"q": {term}, "format": {"rss"}}).Encode()
			case "www.so.com/s":
				allowed = u.Query().Encode() == (url.Values{"q": {term}}).Encode()
			}
			break
		}
		store, err := s.p.manager.packageStore()
		if err != nil {
			return err
		}
		config, err := store.ConfigurationForExecution(ctx, j.Plugin)
		if err != nil {
			return err
		}
		var backend string
		_ = json.Unmarshal(config.Values["search_backend"], &backend)
		if backend != "" && backend != "public" {
			return errors.New("web_public_backend_not_selected")
		}
		query := strings.TrimSpace(args.Query)
		u, err := url.Parse(rawURL)
		if err == nil && query != "" && u.Scheme == "https" && u.User == nil && u.Fragment == "" {
			if u.Host == "www.bing.com" && u.Path == "/search" {
				allowed = u.Query().Encode() == (url.Values{"q": {query}, "format": {"rss"}}).Encode()
			} else if u.Host == "www.so.com" && u.Path == "/s" {
				allowed = u.Query().Encode() == (url.Values{"q": {query}}).Encode()
			}
		}
	case "web.discover":
		_, expectedURL, err := s.p.webDiscoveryRequest(j.Arguments)
		if err != nil {
			return err
		}
		allowed = rawURL == expectedURL
		expectedMax, expectedTimeout = 2<<20, 12
	}
	if !allowed || maximum != expectedMax || timeout != expectedTimeout {
		return errors.New("web_read_source_or_limits_changed")
	}
	return nil
}

func (p *permittedTools) validateWebDiscoveryResult(args json.RawMessage, result serviceWebResult, observed map[string]bool) error {
	source, _, err := p.webDiscoveryRequest(args)
	if err != nil {
		return err
	}
	var in webDiscoveryArguments
	_ = json.Unmarshal(args, &in)
	offset, limit := 0, 40
	if in.Offset != nil {
		offset = *in.Offset
	}
	if in.Limit != nil {
		limit = *in.Limit
	}
	if !result.MetadataOnly || result.Text != "" || !observed[result.Source] || result.Offset != offset || result.RequestedLimit != limit || result.TotalItems < offset || len(result.Candidates) > limit || len(result.Candidates) != min(result.TotalItems-offset, limit) {
		return errors.New("web_discovery_result_unverified")
	}
	end := offset + len(result.Candidates)
	if result.Complete != (end == result.TotalItems) || end < result.TotalItems && (result.NextOffset == nil || *result.NextOffset != end) || end == result.TotalItems && result.NextOffset != nil {
		return errors.New("web_discovery_continuation_changed")
	}
	for _, candidate := range result.Candidates {
		if _, ok := publicSearchURL(candidate.URL); !ok || candidate.Title == "" || candidate.Source != source.Name {
			return errors.New("web_discovery_candidate_invalid")
		}
		if candidate.MediaURL != "" {
			if _, ok := publicSearchURL(candidate.MediaURL); !ok {
				return errors.New("web_discovery_media_invalid")
			}
		}
		switch candidate.Kind {
		case "text", "image", "voice", "video":
		default:
			return errors.New("web_discovery_kind_invalid")
		}
	}
	return nil
}
