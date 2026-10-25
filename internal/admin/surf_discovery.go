package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

type discoverySettings struct {
	Sources      []mcptools.DiscoverySource `json:"sources"`
	Weights      map[string]int             `json:"weights"`
	CooldownDays int                        `json:"cooldown_days"`
	MaxSources   int                        `json:"max_sources"`
	MaxDepth     int                        `json:"max_depth"`
}

func defaultDiscovery() *discoverySettings {
	return &discoverySettings{Sources: mcptools.DefaultDiscoverySources(), Weights: map[string]int{"recommendations": 30, "community": 25, "subscriptions": 20, "related": 20, "search": 5}, CooldownDays: 7, MaxSources: 3, MaxDepth: 2}
}
func validateDiscovery(d *discoverySettings) error {
	if d == nil {
		return nil
	}
	if len(d.Sources) < 1 || len(d.Sources) > 12 || d.CooldownDays < 1 || d.CooldownDays > 90 || d.MaxSources < 1 || d.MaxSources > 4 || d.MaxDepth < 0 || d.MaxDepth > 3 {
		return errors.New("请检查内容源、重复主题冷却和单次探索预算")
	}
	seen := map[string]bool{}
	active := 0
	for _, s := range d.Sources {
		if seen[s.ID] || mcptools.ValidateDiscoverySource(s) != nil {
			return errors.New("内容源名称、类型或公开网址无效，ID不可重复")
		}
		seen[s.ID] = true
		if s.Enabled {
			active++
		}
	}
	if active == 0 {
		return errors.New("至少启用一个内容源")
	}
	sum := 0
	for k, v := range d.Weights {
		if !strings.Contains(" recommendations community subscriptions related search ", " "+k+" ") || v < 0 || v > 100 {
			return errors.New("发现方式权重应为0–100")
		}
		sum += v
	}
	if sum == 0 {
		return errors.New("至少一种发现方式的权重应大于0")
	}
	return nil
}

type surfSeen struct {
	Item       mcptools.DiscoveryItem `json:"item"`
	Seen       time.Time              `json:"seen"`
	Visits     int                    `json:"visits"`
	Bookmarked bool                   `json:"bookmarked,omitempty"`
	Shared     map[string]time.Time   `json:"shared,omitempty"`
}
type surfSession struct {
	ID           string                   `json:"id"`
	Thread       string                   `json:"thread"`
	Mode         string                   `json:"mode"`
	At           time.Time                `json:"at"`
	Depth        int                      `json:"depth"`
	Candidates   []mcptools.DiscoveryItem `json:"candidates"`
	Visited      []string                 `json:"visited"`
	Failures     []string                 `json:"failures"`
	ToolFailures []string                 `json:"tool_failures,omitempty"`
	Selected     string                   `json:"selected,omitempty"`
	State        string                   `json:"state"`
	Reason       string                   `json:"reason,omitempty"`
}
type surfHistory struct {
	Version  int           `json:"version"`
	Items    []surfSeen    `json:"items"`
	Sessions []surfSession `json:"sessions"`
}
type surfHistoryStore struct {
	mu   sync.Mutex
	path string
	data surfHistory
}

func newSurfHistory(dir string) (*surfHistoryStore, error) {
	s := &surfHistoryStore{path: filepath.Join(dir, "surf-history.json"), data: surfHistory{Version: 1, Items: []surfSeen{}, Sessions: []surfSession{}}}
	b, e := os.ReadFile(s.path)
	if errors.Is(e, os.ErrNotExist) {
		return s, nil
	}
	if e != nil || len(b) > 8<<20 || json.Unmarshal(b, &s.data) != nil || s.data.Version != 1 || len(s.data.Items) > 1500 || len(s.data.Sessions) > 120 {
		return nil, errors.New("冲浪历史不可读，原记录已保留")
	}
	return s, nil
}
func (s *surfHistoryStore) saveLocked() error {
	if len(s.data.Items) > 1500 {
		s.data.Items = s.data.Items[len(s.data.Items)-1500:]
	}
	if len(s.data.Sessions) > 120 {
		s.data.Sessions = s.data.Sessions[len(s.data.Sessions)-120:]
	}
	b, e := json.Marshal(s.data)
	if e != nil || len(b) > 8<<20 {
		return errors.New("冲浪历史超过上限")
	}
	return writePrivate(s.path, b)
}
func canonicalSurfURL(raw string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return ""
	}
	u.Fragment = ""
	q := u.Query()
	for k := range q {
		if strings.HasPrefix(k, "utm_") || k == "spm_id_from" || k == "share_source" {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	u.Host = strings.ToLower(u.Host)
	return strings.TrimSuffix(u.String(), "/")
}
func surfIdentity(x mcptools.DiscoveryItem) string {
	raw := x.URL
	if x.MediaURL != "" && (x.Kind == "voice" || x.Kind == "image") {
		raw = x.MediaURL
	}
	h := sha256.Sum256([]byte(canonicalSurfURL(raw)))
	return "find-" + hex.EncodeToString(h[:12])
}
func surfTokens(text string) map[string]bool {
	text = strings.ToLower(text)
	words := strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	out := map[string]bool{}
	for _, word := range words {
		r := []rune(word)
		latin := true
		for _, c := range r {
			latin = latin && c < 128
		}
		if latin {
			if len(r) > 2 {
				out[word] = true
			}
			continue
		}
		for i := 0; i+1 < len(r); i++ {
			out[string(r[i:i+2])] = true
		}
	}
	return out
}
func surfSimilarity(a, b string) float64 {
	aa, bb := surfTokens(a), surfTokens(b)
	if len(aa) == 0 || len(bb) == 0 {
		return 0
	}
	intersection := 0
	for k := range aa {
		if bb[k] {
			intersection++
		}
	}
	return float64(intersection) / float64(len(aa)+len(bb)-intersection)
}
func sameSurfTopic(a, b string) bool {
	if surfSimilarity(a, b) >= .40 {
		return true
	}
	// Compare quoted work/event titles separately from variable chat openings.
	parts := func(s string) []string {
		out := []string{s}
		for _, m := range surfQuotedTitles.FindAllStringSubmatch(s, 8) {
			if len([]rune(m[1])) >= 4 {
				out = append(out, m[1])
			}
		}
		return out
	}
	for _, x := range parts(a) {
		for _, y := range parts(b) {
			if len([]rune(x)) >= 4 && len([]rune(y)) >= 4 && (strings.Contains(x, y) || strings.Contains(y, x) || surfSimilarity(x, y) >= .50) {
				return true
			}
		}
	}
	return false
}

var surfQuotedTitles = regexp.MustCompile(`[“「《"]([^”」》"]{4,160})[”」》"]`)
var surfPublicLinks = regexp.MustCompile(`https?://[^\s<>"']+`)

// Import only recorded accepted surf sends, retaining their real timestamps.
// Reading old history must never initiate a send or refresh its age to today.
func (s *surfHistoryStore) importAccepted(logs []LogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, entry := range logs {
		if entry.Event != "message_sent" || entry.Status != "accepted" || entry.ThreadID == "" || (entry.Origin != "surf" && !strings.HasPrefix(entry.MessageID, "surf-")) {
			continue
		}
		stamp, e := time.Parse(time.RFC3339Nano, entry.Time)
		if e != nil {
			continue
		}
		for _, raw := range surfPublicLinks.FindAllString(entry.Content, 5) {
			raw = strings.TrimRight(raw, "，。；.,;)]}")
			source := mcptools.DiscoverySource{ID: "history", Name: "历史分享", Type: "rss", URL: raw, Kind: "text", Enabled: true}
			if mcptools.ValidateDiscoverySource(source) != nil {
				continue
			}
			item := mcptools.DiscoveryItem{Title: logText(entry.Content, 400), Summary: logText(entry.Content, 1000), URL: raw, Kind: "text", Source: "历史已接受分享"}
			item.ID = surfIdentity(item)
			index := -1
			for i, x := range s.data.Items {
				if x.Item.ID == item.ID {
					index = i
					break
				}
			}
			if index < 0 {
				s.data.Items = append(s.data.Items, surfSeen{Item: item, Seen: stamp, Visits: 1, Shared: map[string]time.Time{entry.ThreadID: stamp}})
				changed = true
			} else {
				x := &s.data.Items[index]
				if x.Shared == nil {
					x.Shared = map[string]time.Time{}
				}
				if x.Shared[entry.ThreadID].Before(stamp) {
					x.Shared[entry.ThreadID] = stamp
					changed = true
				}
			}
		}
	}
	if changed {
		return s.saveLocked()
	}
	return nil
}

func chooseDiscoveryMode(weights map[string]int) string {
	keys := []string{"recommendations", "community", "subscriptions", "related", "search"}
	total := 0
	for _, k := range keys {
		total += weights[k]
	}
	if total <= 0 {
		return "recommendations"
	}
	n, e := rand.Int(rand.Reader, big.NewInt(int64(total)))
	if e != nil {
		return "recommendations"
	}
	v := int(n.Int64())
	for _, k := range keys {
		v -= weights[k]
		if v < 0 {
			return k
		}
	}
	return "recommendations"
}

func (p *permittedTools) discover(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		RelatedURL string `json:"related_url"`
	}
	if json.Unmarshal(args, &in) != nil {
		return "", errors.New("invalid discovery arguments")
	}
	p.manager.mu.Lock()
	settings := p.manager.surf.Settings.Discovery
	copyBytes, _ := json.Marshal(settings)
	p.manager.mu.Unlock()
	if settings == nil {
		settings = defaultDiscovery()
	} else {
		var cloned discoverySettings
		_ = json.Unmarshal(copyBytes, &cloned)
		settings = &cloned
	}
	store := p.manager.surfHistory
	store.mu.Lock()
	mode := chooseDiscoveryMode(settings.Weights)
	depth := 0
	var previous *surfSession
	for i := range store.data.Sessions {
		if store.data.Sessions[i].ID == p.trace.TraceID && store.data.Sessions[i].Thread == p.trace.Thread {
			previous = &store.data.Sessions[i]
			depth = previous.Depth
		}
	}
	if in.RelatedURL != "" {
		valid := false
		if previous != nil {
			for _, c := range previous.Candidates {
				valid = valid || canonicalSurfURL(c.URL) == canonicalSurfURL(in.RelatedURL)
			}
		}
		if !valid || depth >= settings.MaxDepth {
			store.mu.Unlock()
			return "", errors.New("关联探索仅限本次候选且不能超过深度预算")
		}
		depth++
		mode = "related"
	}
	if mode == "related" && in.RelatedURL == "" {
		for i := len(store.data.Items) - 1; i >= 0; i-- {
			x := store.data.Items[i]
			if settings.MaxDepth > 0 && (x.Bookmarked || x.Visits > 0) && mcptools.IsPublicVideoPage(x.Item.URL) {
				in.RelatedURL = x.Item.URL
				depth = 1
				break
			}
		}
		if in.RelatedURL == "" {
			mode = "recommendations"
		}
	}
	// Persistent seen counts rotate samples; source failures back off for one session.
	oldCounts := map[string]int{}
	for _, s := range store.data.Sessions {
		if time.Since(s.At) < 30*time.Minute {
			for _, failure := range s.Failures {
				for _, source := range settings.Sources {
					if strings.HasPrefix(failure, source.Name+"：") {
						oldCounts[source.Name] += 500
					}
				}
			}
		}
		for _, c := range s.Candidates {
			oldCounts[c.Source]++
		}
	}
	store.mu.Unlock()
	sources := []mcptools.DiscoverySource{}
	for _, s := range settings.Sources {
		if s.Enabled {
			sources = append(sources, s)
		}
	}
	for i := len(sources) - 1; i > 0; i-- {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if n != nil {
			sources[i], sources[n.Int64()] = sources[n.Int64()], sources[i]
		}
	}
	sort.SliceStable(sources, func(i, j int) bool {
		score := func(s mcptools.DiscoverySource) int {
			n := oldCounts[s.Name]
			if s.Kind == p.trace.SurfPreferredKind {
				n -= 100
			}
			if mode == "subscriptions" && s.Type == "rss" {
				n -= 50
			}
			if (mode == "community" || mode == "recommendations") && s.Type == "bilibili" {
				n -= 50
			}
			if in.RelatedURL != "" && s.Type == "bilibili" {
				n -= 100
			}
			return n
		}
		return score(sources[i]) < score(sources[j])
	})
	if in.RelatedURL != "" {
		for _, s := range sources {
			if s.Type == "bilibili" {
				sources = []mcptools.DiscoverySource{s}
				break
			}
		}
	}
	if len(sources) > settings.MaxSources {
		sources = sources[:settings.MaxSources]
	}
	type result struct {
		source mcptools.DiscoverySource
		items  []mcptools.DiscoveryItem
		err    error
	}
	results := make([]result, len(sources))
	var wg sync.WaitGroup
	for i, s := range sources {
		wg.Add(1)
		go func(i int, s mcptools.DiscoverySource) {
			defer wg.Done()
			start := time.Now()
			r := result{source: s}
			read := p.discoveryReader
			if read == nil {
				read = mcptools.DiscoverPublic
			}
			r.items, r.err = read(ctx, s, in.RelatedURL)
			results[i] = r
			status, reason := "succeeded", "取得公开内容候选（未观看或播放）"
			if r.err != nil {
				status = "failed"
				reason = mcptools.FailureDescription(r.err)
			}
			p.manager.record(bot.Record{Event: "surf_source", Category: "tool", ThreadID: p.trace.Thread, MessageID: p.trace.TraceID, Tool: "web_discover", TargetURL: s.URL, Content: s.Name, Origin: "surf", Status: status, Reason: reason, DurationMS: time.Since(start).Milliseconds()})
		}(i, s)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	now := time.Now().UTC()
	session := surfSession{ID: p.trace.TraceID, Thread: p.trace.Thread, Mode: mode, At: now, Depth: depth, State: "browsing", Candidates: []mcptools.DiscoveryItem{}, Visited: []string{}, Failures: []string{}}
	index := -1
	for i := range store.data.Sessions {
		if store.data.Sessions[i].ID == session.ID && store.data.Sessions[i].Thread == session.Thread {
			index = i
			session.Candidates = append(session.Candidates, store.data.Sessions[i].Candidates...)
			session.Visited = append(session.Visited, store.data.Sessions[i].Visited...)
		}
	}
	filtered := 0
	pool := []mcptools.DiscoveryItem{}
	for _, r := range results {
		if r.err != nil {
			session.Failures = append(session.Failures, r.source.Name+"："+mcptools.FailureDescription(r.err))
			continue
		}
		mcptools.ShuffleDiscovery(r.items)
		for _, x := range r.items {
			x.ID = surfIdentity(x)
			duplicate := false
			for _, old := range store.data.Items {
				if old.Item.ID == x.ID && now.Sub(old.Seen) < 24*time.Hour {
					duplicate = true
				}
				if t := old.Shared[p.trace.Thread]; !t.IsZero() && now.Sub(t) < time.Duration(settings.CooldownDays)*24*time.Hour && (old.Item.ID == x.ID || sameSurfTopic(x.Title, old.Item.Title)) {
					duplicate = true
				}
			}
			for _, old := range pool {
				duplicate = duplicate || x.ID == old.ID || sameSurfTopic(x.Title, old.Title)
			}
			if duplicate {
				filtered++
				continue
			}
			pool = append(pool, x)
			if len(pool) >= 30 {
				break
			}
		}
	}
	// Greedy diversity selection: one round of each source/kind before repeats.
	selected := []mcptools.DiscoveryItem{}
	counts := map[string]int{}
	for len(pool) > 0 && len(selected) < 8 {
		sort.SliceStable(pool, func(i, j int) bool {
			return counts[pool[i].Source]+counts[pool[i].Kind] < counts[pool[j].Source]+counts[pool[j].Kind]
		})
		x := pool[0]
		pool = pool[1:]
		selected = append(selected, x)
		counts[x.Source]++
		counts[x.Kind]++
		session.Candidates = append(session.Candidates, x)
		store.data.Items = append(store.data.Items, surfSeen{Item: x, Seen: now, Shared: map[string]time.Time{}})
	}
	if len(session.Candidates) > 24 {
		session.Candidates = session.Candidates[len(session.Candidates)-24:]
	}
	if index >= 0 {
		store.data.Sessions[index] = session
	} else {
		store.data.Sessions = append(store.data.Sessions, session)
	}
	if err := store.saveLocked(); err != nil {
		return "", err
	}
	if mode == "search" {
		mode = "search_verification"
	}
	out := map[string]any{"session_id": session.ID, "entry_mode": mode, "depth": depth, "max_depth": settings.MaxDepth, "candidates": selected, "filtered_duplicates": filtered, "source_failures": session.Failures, "instruction": "这是实际读取公开列表/订阅/目录得到的候选，不代表看过视频或听过音频。选值得继续的具体内容，用web_fetch读正文；视频/音乐/图片可用media_url（若有）或BV页执行web_media_download。不要把标题摘要当完整文章；发现适合当前群的新内容就是分享理由，无需等待群友提问；优先读取感兴趣的具体候选或准备媒体。确实没有兴趣或不适合可silent并用reason=surf_not_interested或surf_not_suitable；喜欢但只想收藏可在silent对象加surf_action=bookmark和surf_item_id。分享时给reply加surf_item_id，比较近期主题，不能用同主题换网址凑新鲜。可用web_discover(related_url=候选BV页)继续关联探索，搜索仅用于核实具体疑问；不要退回搜索宽泛兴趣句子或首页。失败源已记录，其他源候选仍可用。"}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func (p *permittedTools) ReviewSurf(ctx context.Context, d companion.Decision) (companion.Decision, error) {
	if !p.trace.Surf {
		return d, nil
	}
	s := p.manager.surfHistory
	s.mu.Lock()
	defer s.mu.Unlock()
	var session *surfSession
	for i := range s.data.Sessions {
		if s.data.Sessions[i].ID == p.trace.TraceID && s.data.Sessions[i].Thread == p.trace.Thread {
			session = &s.data.Sessions[i]
		}
	}
	if session == nil {
		if d.Action == "reply" {
			return companion.Decision{Action: "silent", Reason: "surf_no_discovery_session"}, nil
		}
		return d, nil
	}
	var chosen *mcptools.DiscoveryItem
	for i := range session.Candidates {
		x := &session.Candidates[i]
		if x.ID == d.SurfItemID {
			chosen = x
			break
		}
	}
	finishSilent := func(reason, state string) (companion.Decision, error) {
		session.State, session.Reason = state, reason
		if chosen != nil {
			session.Selected = chosen.ID
		}
		return companion.Decision{Action: "silent", Reason: reason, SurfAction: d.SurfAction, SurfItemID: session.Selected}, s.saveLocked()
	}
	if d.Action == "silent" {
		reason, state := d.Reason, "not_shared"
		switch reason {
		case "surf_not_interested", "surf_not_suitable", "surf_browse_only", "surf_no_verified_source", "surf_duplicate_source", "tool_unavailable_or_budget_exhausted", "tool_budget_exhausted", "invalid_model_decision", "invalid_tool_arguments":
		case "surf_tool_failed":
			if len(session.ToolFailures) > 0 {
				state = "failed"
			} else {
				reason = "surf_browse_only"
			}
		default:
			reason = "surf_browse_only"
		}
		if reason == "surf_browse_only" && len(session.Visited) == 0 {
			reason = "surf_candidates_only"
		}
		if len(session.Candidates) == 0 {
			if len(session.Failures) > 0 {
				reason, state = "surf_sources_unavailable", "failed"
			} else {
				reason = "surf_no_novel_candidates"
			}
		}
		if d.SurfAction == "bookmark" && chosen != nil {
			reason, state = "surf_bookmarked", "bookmarked"
			for i := range s.data.Items {
				if s.data.Items[i].Item.ID == chosen.ID {
					s.data.Items[i].Bookmarked = true
				}
			}
		}
		return finishSilent(reason, state)
	}
	if chosen == nil {
		// Some models omit the optional candidate field while selecting a real
		// prepared attachment. Infer only an unambiguous exact source match.
		p.mediaMu.Lock()
		for i := range session.Candidates {
			x := &session.Candidates[i]
			match := false
			for _, id := range d.MediaIDs {
				raw := p.mediaOrigin[id]
				match = match || raw != "" && (canonicalSurfURL(raw) == canonicalSurfURL(x.URL) || canonicalSurfURL(raw) == canonicalSurfURL(x.MediaURL))
			}
			if match {
				if chosen != nil && chosen.ID != x.ID {
					chosen = nil
					break
				}
				chosen = x
			}
		}
		p.mediaMu.Unlock()
		if chosen == nil {
			return finishSilent("surf_candidate_not_selected", "not_shared")
		}
		d.SurfItemID = chosen.ID
	}
	verified := false
	if len(d.MediaIDs) > 0 {
		verified = true
		p.mediaMu.Lock()
		for _, id := range d.MediaIDs {
			origin := p.mediaOrigin[id]
			verified = verified && origin != "" && (canonicalSurfURL(origin) == canonicalSurfURL(chosen.URL) || canonicalSurfURL(origin) == canonicalSurfURL(chosen.MediaURL))
		}
		p.mediaMu.Unlock()
	} else {
		for _, raw := range session.Visited {
			verified = verified || canonicalSurfURL(raw) == canonicalSurfURL(chosen.URL)
		}
	}
	if !verified {
		return finishSilent("surf_candidate_not_verified", "not_shared")
	}
	text := chosen.Title + " " + strings.Join(d.Messages, " ")
	p.manager.mu.Lock()
	days := 7
	if p.manager.surf.Settings.Discovery != nil {
		days = p.manager.surf.Settings.Discovery.CooldownDays
	}
	p.manager.mu.Unlock()
	for _, old := range s.data.Items {
		if t := old.Shared[p.trace.Thread]; !t.IsZero() && time.Since(t) < time.Duration(days)*24*time.Hour && (old.Item.ID == chosen.ID || sameSurfTopic(chosen.Title, old.Item.Title) || sameSurfTopic(text, old.Item.Summary)) {
			return finishSilent("surf_duplicate_topic", "not_shared")
		}
	}
	// Existing conversation history also catches pre-migration repeated topics.
	for _, old := range p.manager.memory.Context(p.trace, p.manager.Config().Companion, time.Now()).Messages {
		if old.Role == "assistant" && old.Media == nil && sameSurfTopic(text, old.Text) {
			return finishSilent("surf_duplicate_topic", "not_shared")
		}
	}
	session.Selected = chosen.ID
	session.State = "prepared"
	session.Reason = "surf_content_prepared"
	if err := s.saveLocked(); err != nil {
		return companion.Decision{}, err
	}
	return d, nil
}
func (s *surfHistoryStore) recordActivity(r bot.Record) error {
	if !strings.HasPrefix(r.MessageID, "surf-") {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Sessions {
		session := &s.data.Sessions[i]
		if session.ID != r.MessageID || session.Thread != r.ThreadID {
			continue
		}
		if r.Event == "web_activity" && r.Status == "succeeded" && r.TargetURL != "" {
			if len(session.Visited) < 30 {
				session.Visited = append(session.Visited, r.TargetURL)
			}
			for j := range s.data.Items {
				if canonicalSurfURL(s.data.Items[j].Item.URL) == canonicalSurfURL(r.TargetURL) {
					s.data.Items[j].Visits++
				}
			}
		}
		if r.Status == "failed" && (r.Event == "web_activity" || r.Event == "tool_call_failed") && len(session.ToolFailures) < 30 {
			// Store only the existing sanitized tool name and public error code,
			// never provider output or raw error bodies in the decision summary.
			failure := logText(r.Tool+" / "+r.Reason, 500)
			if len(session.ToolFailures) == 0 || session.ToolFailures[len(session.ToolFailures)-1] != failure {
				session.ToolFailures = append(session.ToolFailures, failure)
			}
		}
		if r.Event == "message_sent" && r.Status == "accepted" && session.Selected != "" {
			session.State = "shared"
			session.Reason = "surf_share_accepted"
			for j := range s.data.Items {
				item := &s.data.Items[j]
				if item.Item.ID == session.Selected {
					if item.Shared == nil {
						item.Shared = map[string]time.Time{}
					}
					item.Shared[r.ThreadID] = time.Now().UTC()
				}
			}
		}
		if r.Event == "character_decision" && r.Status == "silent" && session.State != "bookmarked" {
			if session.State != "failed" {
				session.State = "not_shared"
			}
			session.Reason = r.Reason
		}
		if r.Event == "model_failed" || r.Event == "send_failed" {
			session.State = "failed"
			session.Reason = r.Reason
		}
		if r.Event == "utterance_plan_stopped" || r.Event == "utterance_plan_revised" && r.Reason == "plan_cancel" {
			session.State = "not_shared"
			session.Reason = r.Reason
		}
		if r.Event == "character_decision" && r.Status == "blocked" {
			session.State = "blocked"
			session.Reason = r.Reason
		}
		return s.saveLocked()
	}
	return nil
}
func (s *surfHistoryStore) snapshot() surfHistory {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(s.data)
	var out surfHistory
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *surfHistoryStore) visit(in companion.Input, raw string) error {
	if !in.Surf || raw == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Sessions {
		session := &s.data.Sessions[i]
		if session.ID == in.TraceID && session.Thread == in.Thread {
			raw = canonicalSurfURL(raw)
			for _, old := range session.Visited {
				if canonicalSurfURL(old) == raw {
					return nil
				}
			}
			if len(session.Visited) < 30 {
				session.Visited = append(session.Visited, raw)
			}
			return s.saveLocked()
		}
	}
	return nil
}

func (p *permittedTools) rememberSearchCandidates(result string) (string, error) {
	store := p.manager.surfHistory
	store.mu.Lock()
	defer store.mu.Unlock()
	var session *surfSession
	for i := range store.data.Sessions {
		if store.data.Sessions[i].ID == p.trace.TraceID && store.data.Sessions[i].Thread == p.trace.Thread {
			session = &store.data.Sessions[i]
		}
	}
	if session == nil {
		return "", nil
	}
	lines := strings.Split(result, "\n")
	added := []mcptools.DiscoveryItem{}
	for i, line := range lines {
		raw := strings.TrimSpace(line)
		if !(strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://")) || i == 0 || i+1 >= len(lines) {
			continue
		}
		raw = strings.TrimSpace(raw)
		title := strings.TrimSpace(lines[i-1])
		if title == "" || strings.Contains(title, "Search provider:") {
			continue
		}
		kind := "text"
		if mcptools.IsPublicVideoPage(raw) {
			kind = "video"
			if p.trace.SurfPreferredKind == "voice" {
				kind = "voice"
			}
		}
		x := mcptools.DiscoveryItem{Title: logText(title, 400), Summary: logText(lines[i+1], 700), URL: raw, Source: "公开搜索", Kind: kind}
		x.ID = surfIdentity(x)
		seen := false
		for _, old := range session.Candidates {
			seen = seen || old.ID == x.ID
		}
		if seen {
			continue
		}
		session.Candidates = append(session.Candidates, x)
		store.data.Items = append(store.data.Items, surfSeen{Item: x, Seen: time.Now().UTC(), Shared: map[string]time.Time{}})
		added = append(added, x)
		if len(added) >= 6 {
			break
		}
	}
	if len(session.Candidates) > 24 {
		session.Candidates = session.Candidates[len(session.Candidates)-24:]
	}
	if err := store.saveLocked(); err != nil {
		return "", err
	}
	if len(added) == 0 {
		return "", nil
	}
	b, _ := json.Marshal(added)
	return "\n本次搜索的新候选已加入当前浏览会话（仍须读取或准备媒体，不能只发摘要）：\n" + string(b), nil
}
