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
	"slices"
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
	ID      string `json:"id"`
	Account string `json:"account,omitempty"`
	Thread  string `json:"thread"`
	// AuthorizationEpoch is the recall epoch current when the host authorized
	// a normal exploration. Private claim material is never persisted here. A
	// later forget/correction changes it, so a context builder cannot mistake
	// this old row for a current permission.
	AuthorizationEpoch string                   `json:"authorization_epoch,omitempty"`
	ScopeKey           string                   `json:"scope_key,omitempty"`
	Interest           string                   `json:"interest,omitempty"`
	InterestKey        string                   `json:"interest_key,omitempty"`
	Mode               string                   `json:"mode"`
	At                 time.Time                `json:"at"`
	Depth              int                      `json:"depth"`
	Candidates         []mcptools.DiscoveryItem `json:"candidates"`
	Visited            []string                 `json:"visited"`
	// Observations preserve the provenance of what this session actually learned
	// about one public item. Candidates retain title/summary metadata; an
	// observation never copies untrusted page text into surf-history.
	Observations []surfObservation `json:"observations,omitempty"`
	Failures     []string          `json:"failures"`
	ToolFailures []string          `json:"tool_failures,omitempty"`
	Selected     string            `json:"selected,omitempty"`
	State        string            `json:"state"`
	Reason       string            `json:"reason,omitempty"`
	// StoppedAt is an explicit end-of-exploration event. State remains
	// compatible with existing UI/history buckets (for example not_shared),
	// while this field distinguishes a deliberate "not interested / enough for
	// now" decision from a transport or source failure.
	StoppedAt time.Time `json:"stopped_at,omitzero"`
}

// surfObservation is deliberately small, public and source-scoped. Page text
// remains in the verified plugin job; a later context builder must revalidate
// that job before it reads an excerpt back out.
type surfObservation struct {
	CandidateID string `json:"candidate_id,omitempty"`
	// Platform identifies where a candidate came from (the installed web
	// reader, an isolated injected adapter, or a later adapter). It is
	// display/selection metadata only and never authorizes a body read.
	Platform           string    `json:"platform,omitempty"`
	URL                string    `json:"url"`
	Tool               string    `json:"tool"`
	State              string    `json:"state"` // body_read, read_failed, read_unverified, legacy_unproven, media_prepared
	JobID              string    `json:"job_id,omitempty"`
	ContentHash        string    `json:"content_hash,omitempty"`
	Offset             int       `json:"offset,omitempty"`
	ReturnedCharacters int       `json:"returned_characters,omitempty"`
	TotalCharacters    int       `json:"total_characters,omitempty"`
	Complete           bool      `json:"complete,omitempty"`
	Failure            string    `json:"failure,omitempty"`
	At                 time.Time `json:"at"`
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

func relatedVideoPage(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	return (u.Hostname() == "www.bilibili.com" || u.Hostname() == "bilibili.com") && mcptools.IsPublicVideoPage(raw)
}

// Related discovery can start from an earlier bookmark or a visited video in
// this authorized interest. Global seen counts are only for deduplication.
func recentRelatedSurfURL(sessions []surfSession, thread, scopeKey, interestKey string) string {
	if scopeKey == "" || interestKey == "" {
		return ""
	}
	for _, session := range slices.Backward(sessions) {
		if session.Thread != thread || session.ScopeKey != scopeKey || session.InterestKey != interestKey ||
			session.Reason == "surf_not_interested" || session.Reason == "surf_not_suitable" {
			continue
		}
		for _, candidate := range slices.Backward(session.Candidates) {
			if candidate.ID != "" && relatedVideoPage(candidate.URL) &&
				((session.State == "bookmarked" && session.Selected == candidate.ID) || slices.Contains(session.Visited, canonicalSurfURL(candidate.URL))) {
				return candidate.URL
			}
		}
	}
	return ""
}

func (p *permittedTools) discover(ctx context.Context, args json.RawMessage) (string, error) {
	if err := p.installedWebCapability(ctx, "web.discover"); err != nil {
		return "", err
	}
	if !p.servicePluginsAllowed() || !p.serviceWebAllowed("web.discover") {
		return "", errors.New("web_discovery_authorization_required")
	}
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
	account, epoch, scopeKey, interestKey, scopeErr := p.surfExplorationIdentity(ctx, p.trace.SurfTopic)
	if scopeErr != nil {
		return "", scopeErr
	}
	store := p.manager.surfHistory
	store.mu.Lock()
	mode := chooseDiscoveryMode(settings.Weights)
	depth := 0
	var previous *surfSession
	for i := range store.data.Sessions {
		if store.data.Sessions[i].ID == p.trace.TraceID && store.data.Sessions[i].Thread == p.trace.Thread {
			if store.data.Sessions[i].ScopeKey != scopeKey || store.data.Sessions[i].InterestKey != interestKey {
				store.mu.Unlock()
				return "", errors.New("探索授权或兴趣已变化，请开始新的浏览回合")
			}
			previous = &store.data.Sessions[i]
			depth = previous.Depth
		}
	}
	canReadRelated := slices.ContainsFunc(settings.Sources, func(source mcptools.DiscoverySource) bool {
		return source.Enabled && source.Type == "bilibili"
	})
	if in.RelatedURL != "" {
		valid := false
		if previous != nil {
			for _, c := range previous.Candidates {
				valid = valid || canonicalSurfURL(c.URL) == canonicalSurfURL(in.RelatedURL)
			}
		}
		if !valid || !relatedVideoPage(in.RelatedURL) || !canReadRelated || depth >= settings.MaxDepth {
			store.mu.Unlock()
			return "", errors.New("请从本次候选中选择 BV 视频，启用哔哩哔哩来源并检查探索深度")
		}
		depth++
		mode = "related"
	}
	if mode == "related" && in.RelatedURL == "" {
		if previous == nil && canReadRelated && settings.MaxDepth > 0 {
			in.RelatedURL = recentRelatedSurfURL(store.data.Sessions, p.trace.Thread, scopeKey, interestKey)
			if in.RelatedURL != "" {
				depth = 1
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
	for i, s := range sources {
		func(i int, s mcptools.DiscoverySource) {
			start := time.Now()
			r := result{source: s}
			read := p.discoveryReader
			if read == nil {
				read = p.readInstalledDiscovery
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
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	_, _, currentScope, currentInterest, scopeErr := p.surfExplorationIdentity(ctx, p.trace.SurfTopic)
	if scopeErr != nil {
		return "", scopeErr
	}
	if currentScope != scopeKey || currentInterest != interestKey {
		return "", errors.New("读取期间探索授权或来源已变化，请开始新的浏览回合")
	}
	store.mu.Lock()
	now := time.Now().UTC()
	session := surfSession{ID: p.trace.TraceID, Thread: p.trace.Thread, Mode: mode, At: now, Depth: depth, State: "browsing", Candidates: []mcptools.DiscoveryItem{}, Visited: []string{}, Failures: []string{}}
	session.Account, session.AuthorizationEpoch = account, epoch
	session.Interest, session.ScopeKey, session.InterestKey = p.trace.SurfTopic, scopeKey, interestKey
	index := -1
	for i := range store.data.Sessions {
		if store.data.Sessions[i].ID == session.ID && store.data.Sessions[i].Thread == session.Thread {
			if store.data.Sessions[i].ScopeKey != scopeKey || store.data.Sessions[i].InterestKey != interestKey {
				store.mu.Unlock()
				return "", errors.New("探索授权或兴趣已变化，请开始新的浏览回合")
			}
			index = i
			session.Candidates = append(session.Candidates, store.data.Sessions[i].Candidates...)
			session.Visited = append(session.Visited, store.data.Sessions[i].Visited...)
			session.Observations = append(session.Observations, store.data.Sessions[i].Observations...)
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
			// The configured source, never an adapter-supplied display label,
			// identifies the authority that discovered this candidate. Display
			// labels may change, while interaction grants must not.
			x.SourceID = r.source.ID
			x.Source = surfPlatformName(r.source.Name, x.Source)
			publicURL, ok := publicSearchURL(strings.TrimSpace(x.URL))
			if !ok {
				// Custom/legacy discovery readers are not trusted to provide a
				// safe public URL. Do not let private or credential-bearing links
				// become candidates, observations or diagnostic history.
				filtered++
				continue
			}
			x.URL = publicURL
			if x.MediaURL != "" {
				if mediaURL, ok := publicSearchURL(strings.TrimSpace(x.MediaURL)); ok {
					x.MediaURL = mediaURL
				} else {
					x.MediaURL = ""
				}
			}
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
	err := store.saveLocked()
	session.Observations = append([]surfObservation(nil), session.Observations...)
	store.mu.Unlock()
	if err != nil {
		return "", err
	}
	if mode == "search" {
		mode = "search_verification"
	}
	out := map[string]any{"session_id": session.ID, "entry_mode": mode, "depth": depth, "max_depth": settings.MaxDepth, "candidates": selected, "filtered_duplicates": filtered, "source_failures": session.Failures, "instruction": "这是实际读取公开列表/订阅/目录得到的候选，不代表看过视频或听过音频。选值得继续的具体内容，用web_fetch读正文；视频/音乐/图片可用media_url（若有）或BV页执行web_media_download。不要把标题摘要当完整文章；发现适合当前群的新内容就是分享理由，无需等待群友提问；优先读取感兴趣的具体候选或准备媒体。确实没有兴趣或不适合可silent并用reason=surf_not_interested或surf_not_suitable；喜欢但只想收藏可在silent对象加surf_action=bookmark和surf_item_id。分享时给reply加surf_item_id，比较近期主题，不能用同主题换网址凑新鲜。可用web_discover(related_url=候选BV页)继续关联探索，搜索仅用于核实具体疑问；不要退回搜索宽泛兴趣句子或首页。失败源已记录，其他源候选仍可用。"}
	if excerpts := p.recentReadingExcerpts(ctx, session); len(excerpts) > 0 {
		out["current_session_reading_excerpts"] = excerpts
	}

	if p.privateSurfOrigin == nil {
		if previous := p.priorReadingExcerpts(ctx, session); len(previous) > 0 {
			out["prior_authorized_reading_excerpts"] = previous
			out["instruction"] = out["instruction"].(string) + " 既往摘录只记录此前在同一兴趣下实际读到的部分，当前候选的阅读进度单独核对。"
		}
		if comments := p.priorConfirmedInteractions(ctx, session); len(comments) > 0 {
			out["prior_confirmed_interactions"] = comments
		}
		if len(p.surfInteractionTools()) > 0 {
			out["public_interaction"] = map[string]any{"tool": "surf_interaction", "hint": "对已读的授权来源可选择评论；待确认的操作可用 status 查询。"}
			if pending := p.pendingSurfInteractions(ctx); len(pending) > 0 {
				out["pending_public_interactions"] = pending
			}
		}
	}

	if p.privateSurfOrigin != nil {
		out["instruction"] = "这是实际读取公开列表/订阅/目录得到的候选元数据，不代表观看或播放。当前仅供私人浏览，没有发送目标或发送权限。可用web_fetch分页读取候选正文、web_search核实具体疑问，或web_discover继续发现；不得下载、准备或发送素材，也不能读取会话记忆。最终silent；喜欢的候选可用surf_action=bookmark与surf_item_id收藏，其他候选可略过。"
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func (p *permittedTools) ReviewSurf(ctx context.Context, d companion.Decision) (companion.Decision, error) {
	if p.privateSurfOrigin != nil {
		if err := p.privateSurfOrigin.check(ctx, p.trace); err != nil {
			return companion.Decision{}, err
		}
		d.Action, d.Reason, d.Messages, d.MediaIDs = "silent", "surf_browse_only", nil, nil
	}
	if !p.trace.Surf {
		return d, nil
	}
	currentAccount, currentEpoch, currentScope, currentInterest, scopeErr := p.surfExplorationIdentity(ctx, p.trace.SurfTopic)
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
	// A discovered item is public metadata, but a prepared read/media result
	// may be followed by a send. Recheck the same host authorization and recall
	// epoch used while assembling model context immediately before that boundary.
	// Pre-scope persisted rows retain their existing conservative checks; they
	// are never upgraded into scoped history. Every new scoped row must match.
	scoped := session.Account != "" || session.ScopeKey != "" || session.AuthorizationEpoch != "" || session.InterestKey != ""
	if scoped {
		epochRequired := p.privateSurfOrigin == nil
		if scopeErr != nil || session.Account == "" || session.ScopeKey == "" || epochRequired && session.AuthorizationEpoch == "" || session.Account != currentAccount || session.ScopeKey != currentScope || session.InterestKey != currentInterest || epochRequired && session.AuthorizationEpoch != currentEpoch {
			return finishSilent("surf_authorization_changed", "blocked")
		}
	}
	if d.Action == "silent" {
		reason, state := d.Reason, "not_shared"
		switch reason {
		case "surf_not_interested", "surf_not_suitable", "surf_browse_only", "surf_no_verified_source", "surf_duplicate_source", "tool_unavailable_or_budget_exhausted", "tool_budget_exhausted", "invalid_model_decision", "invalid_tool_arguments", "surf_forced_share_unfulfilled", "surf_forced_share_failed":
		case "surf_tool_failed":
			if len(session.ToolFailures) > 0 {
				state = "failed"
			} else {
				reason = "surf_browse_only"
			}
		default:
			reason = "surf_browse_only"
		}
		if reason == "surf_browse_only" && !p.currentSurfBodyRead(ctx, session, "") {
			reason = "surf_candidates_only"
		}
		if len(session.Candidates) == 0 {
			if len(session.Failures) > 0 {
				reason, state = "surf_sources_unavailable", "failed"
			} else {
				reason = "surf_no_novel_candidates"
			}
		}
		if d.SurfAction == "bookmark" && chosen != nil && !p.trace.SurfForceShare {
			reason, state = "surf_bookmarked", "bookmarked"
			for i := range s.data.Items {
				if s.data.Items[i].Item.ID == chosen.ID {
					s.data.Items[i].Bookmarked = true
				}
			}
		}
		if surfExplorationStopped(reason) {
			session.StoppedAt = time.Now().UTC()
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
		verified = p.currentSurfBodyRead(ctx, session, chosen.ID)
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

func surfExplorationStopped(reason string) bool {
	switch reason {
	case "surf_not_interested", "surf_not_suitable", "surf_browse_only", "surf_candidates_only", "surf_no_novel_candidates", "surf_no_verified_source", "surf_duplicate_source", "surf_duplicate_topic", "surf_bookmarked":
		return true
	default:
		return false
	}
}

// currentSurfBodyRead makes persisted provenance useful only while its source
// job remains authorized, unmodified and internally consistent. In particular,
// a pre-migration Visited URL (or a job after permission revocation) cannot
// become a share authorization merely because it still exists on disk.
func (p *permittedTools) currentSurfBodyRead(ctx context.Context, session *surfSession, candidateID string) bool {
	if session == nil || !p.trace.Surf {
		return false
	}
	for _, row := range session.Observations {
		if row.State != "body_read" || row.JobID == "" || row.ContentHash == "" || row.CandidateID == "" {
			continue
		}
		if candidateID != "" && row.CandidateID != candidateID {
			continue
		}
		if _, valid := p.validatedSurfReading(ctx, row); valid {
			return true
		}
	}
	return false
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

func (s *surfSession) addObservation(row surfObservation) {
	row.URL = canonicalSurfURL(row.URL)
	if row.URL == "" {
		return
	}
	// Pagination may revisit a URL, but exact duplicate result records add no
	// evidence. Keep the oldest observation timestamp for an identical read.
	for _, old := range s.Observations {
		// A compatibility alias and service_plugin_task can reread the same
		// completed job. Deduplicate that actual range across call names while
		// still retaining a new pagination range or a new job.
		if row.State == "body_read" && old.State == "body_read" && old.JobID != "" && old.JobID == row.JobID && old.Offset == row.Offset && old.ReturnedCharacters == row.ReturnedCharacters && old.ContentHash == row.ContentHash {
			return
		}
		if old.URL == row.URL && old.Tool == row.Tool && old.State == row.State && old.JobID == row.JobID && old.Offset == row.Offset && old.ContentHash == row.ContentHash && old.Failure == row.Failure {
			return
		}
	}
	// Candidate metadata already records discovery. Retain only a few actual
	// read/failure provenance rows: 8 rows x 2 KiB URL x 120 retained sessions
	// stays small beside the pre-existing candidate ledger and cannot turn the
	// 8 MiB history guard into a permanent save failure.
	if len(s.Observations) >= 8 {
		s.Observations = s.Observations[len(s.Observations)-7:]
	}
	s.Observations = append(s.Observations, row)
}

type surfWebJobEnvelope struct {
	Job struct {
		ID        string          `json:"id"`
		State     string          `json:"state"`
		Plugin    string          `json:"plugin"`
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
		Result    json.RawMessage `json:"result"`
	} `json:"job"`
}

func surfWebJobEnvelopeFromResult(raw string) (surfWebJobEnvelope, bool) {
	var envelope surfWebJobEnvelope
	if json.Unmarshal([]byte(raw), &envelope) == nil && envelope.Job.ID != "" {
		return envelope, true
	}
	if json.Unmarshal([]byte(raw), &envelope.Job) != nil || envelope.Job.ID == "" {
		return surfWebJobEnvelope{}, false
	}
	return envelope, true
}

func surfWebReadResult(raw string) (serviceWebResult, string, bool) {
	envelope, exists := surfWebJobEnvelopeFromResult(raw)
	var read serviceWebResult
	if !exists || envelope.Job.State != "completed" || envelope.Job.Plugin != "web-search" || envelope.Job.Tool != "web.read" || len(envelope.Job.Result) == 0 || json.Unmarshal(envelope.Job.Result, &read) != nil {
		return serviceWebResult{}, "", false
	}
	return read, envelope.Job.ID, read.OK || read.Text != "" || read.URL != ""
}

func surfWebJobID(raw string) string {
	envelope, ok := surfWebJobEnvelopeFromResult(raw)
	if !ok || envelope.Job.Plugin != "web-search" || envelope.Job.Tool != "web.read" {
		return ""
	}
	return envelope.Job.ID
}

func (s *surfHistoryStore) recordReadTask(in companion.Input, result string, callErr error) error {
	envelope, ok := surfWebJobEnvelopeFromResult(result)
	if !ok || envelope.Job.Plugin != "web-search" || envelope.Job.Tool != "web.read" {
		return nil
	}
	var args struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(envelope.Job.Arguments, &args) != nil || args.URL == "" {
		return nil
	}
	switch envelope.Job.State {
	case "completed":
	case "failed":
		callErr = errors.New("web read task failed")
	default:
		return nil
	}
	_, err := s.recordRead(in, "service_plugin_task", args.URL, result, callErr)
	return err
}

// recordRead records exactly the evidence returned by the installed reader.
// A successful call with only title/metadata remains unverified. This prevents
// downstream reflection from changing "I found this" into "I read this".
func (s *surfHistoryStore) recordRead(in companion.Input, tool, rawURL, result string, callErr error) (bool, error) {
	if !in.Surf || rawURL == "" || (tool != "web_fetch" && tool != "service_plugin_call" && tool != "service_plugin_task" && tool != "web_media_download") {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Sessions {
		session := &s.data.Sessions[i]
		if session.ID != in.TraceID || session.Thread != in.Thread {
			continue
		}
		url := canonicalSurfURL(rawURL)
		candidateID := ""
		platform := ""
		for _, candidate := range session.Candidates {
			if canonicalSurfURL(candidate.URL) == url || canonicalSurfURL(candidate.MediaURL) == url {
				candidateID = candidate.ID
				platform = candidate.Source
				break
			}
		}
		// An exploration may only retain evidence for one of its own discovered
		// public candidates. Never persist a raw URL supplied by a model or a
		// plugin error, including a URL containing accidental credentials.
		if candidateID == "" {
			return false, nil
		}
		row := surfObservation{CandidateID: candidateID, Platform: platform, URL: url, Tool: tool, At: time.Now().UTC()}
		if callErr != nil {
			row.JobID = surfWebJobID(result)
			row.State, row.Failure = "read_failed", logText(mcptools.FailureDescription(callErr), 500)
			session.addObservation(row)
			return false, s.saveLocked()
		}
		if tool == "web_media_download" {
			var prepared struct {
				Media []companion.GeneratedMedia `json:"media"`
			}
			if json.Unmarshal([]byte(result), &prepared) != nil || !slices.ContainsFunc(prepared.Media, func(m companion.GeneratedMedia) bool { return m.ID != "" }) {
				row.State = "read_unverified"
				session.addObservation(row)
				return false, s.saveLocked()
			}
			row.State = "media_prepared"
			session.addObservation(row)
			return false, s.saveLocked()
		}
		read, jobID, ok := surfWebReadResult(result)
		if !ok || !read.OK || !read.Untrusted {
			row.State = "read_unverified"
			session.addObservation(row)
			return false, s.saveLocked()
		}
		if read.ReadCoverage == nil {
			row.State = "legacy_unproven"
			session.addObservation(row)
			return false, s.saveLocked()
		}
		// A verified reader may return a public redirect target. Keep the
		// discovered candidate ID for selection, but bind evidence/context to
		// the host-observed final public URL instead of rejecting a valid read.
		finalURL, public := publicSearchURL(strings.TrimSpace(read.URL))
		if !public {
			row.State, row.Failure = "read_unverified", "读取结果未提供可验证的公开来源"
			session.addObservation(row)
			return false, s.saveLocked()
		}
		row.URL = canonicalSurfURL(finalURL)
		row.JobID = jobID
		row.Offset, row.ReturnedCharacters, row.TotalCharacters, row.Complete = read.Offset, read.ReturnedCharacters, read.TotalCharacters, read.Complete
		coverage := read.ReadCoverage
		if strings.TrimSpace(read.Text) == "" || read.ReturnedCharacters < 1 || coverage == nil || coverage.Kind != "page_body" || coverage.StartOffset != row.Offset || coverage.EndOffset != row.Offset+row.ReturnedCharacters || coverage.TotalCharacters != row.TotalCharacters || coverage.Complete != row.Complete || coverage.Excerpt == "" || read.SHA == "" {
			row.State = "read_unverified"
			session.addObservation(row)
			return false, s.saveLocked()
		}
		row.State, row.ContentHash = "body_read", read.SHA
		session.addObservation(row)
		if !slices.Contains(session.Visited, url) && len(session.Visited) < 30 {
			session.Visited = append(session.Visited, url)
			for j := range s.data.Items {
				if s.data.Items[j].Item.ID == candidateID {
					s.data.Items[j].Visits++
					break
				}
			}
		}
		return true, s.saveLocked()
	}
	return false, nil
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
	var envelope struct {
		Job struct {
			State  string           `json:"state"`
			Result serviceWebResult `json:"result"`
		} `json:"job"`
	}
	if json.Unmarshal([]byte(result), &envelope) != nil || envelope.Job.State != "completed" || !envelope.Job.Result.OK || !envelope.Job.Result.Untrusted {
		return "", nil
	}
	added := []mcptools.DiscoveryItem{}
	for _, item := range envelope.Job.Result.Items {
		raw := strings.TrimSpace(item.URL)
		if _, ok := publicSearchURL(raw); !ok {
			continue
		}
		title := strings.TrimSpace(item.Title)
		if title == "" {
			continue
		}
		kind := "text"
		if mcptools.IsPublicVideoPage(raw) {
			kind = "video"
			if p.trace.SurfPreferredKind == "voice" {
				kind = "voice"
			}
		}
		x := mcptools.DiscoveryItem{Title: logText(title, 400), Summary: logText(item.Summary, 700), URL: raw, Source: "公开搜索", Kind: kind}
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
