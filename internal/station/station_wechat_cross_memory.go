package station

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/mcptools"
)

const stationWechatCrossToolName = "wechat_cross_memory"

type stationWechatCrossProof struct {
	LinkedReceipt                                       string `json:"linked_receipt,omitempty"`
	Thread, Digest, Instance, Epoch, Event              string
	EventRevision, GrantRevision                        int
	Station                                             bool `json:"station,omitempty"`
	Douyin                                              bool `json:"douyin,omitempty"`
	StationInstance, StationEpoch, StationScope         string
	DouyinKind, DouyinScope, DouyinBinding, DouyinEpoch string
	DouyinRevision                                      uint64
	Archived                                            bool `json:"archived,omitempty"`
	ArchiveFingerprint                                  string
	ArchiveVisibility                                   string
	ArchiveGrant                                        time.Time
}
type stationWechatCrossRef struct {
	Proof   stationWechatCrossProof
	ID      string
	Version int
}
type stationWechatCrossPage struct {
	Proof                        stationWechatCrossProof
	Query, Since, Before, Cursor string
}
type stationWechatCrossTurn struct {
	Required    bool
	Target      string
	Proofs      []stationWechatCrossProof
	Used        []stationWechatCrossProof
	Refs        map[string]stationWechatCrossRef
	Pages       map[string]stationWechatCrossPage
	Douyin      *localToolSet
	DouyinProof stationWechatCrossProof
}
type stationWechatCrossInput struct {
	Action       string `json:"action"`
	Source       string `json:"source"`
	Thread       string `json:"thread_id"`
	Query        string `json:"query"`
	Since        string `json:"since"`
	Before       string `json:"before"`
	Cursor       string `json:"cursor"`
	Reference    string `json:"reference"`
	Limit        *int   `json:"limit"`
	Offset       *int   `json:"offset"`
	Length       *int   `json:"length"`
	SourceOffset *int   `json:"source_offset"`
	SourceLimit  *int   `json:"source_limit"`
	SourceID     string `json:"source_id"`
	WorkID       string `json:"work_id"`
	Dataset      string `json:"dataset"`
	SourceIndex  *int   `json:"source_index"`
}

func stationWechatCrossTool() mcptools.Tool {
	return mcptools.NewTool(stationWechatCrossToolName, "仅在本人明确要求时查询获准记忆。sources列同账号会话、工作站与获准抖音来源；通过已核验的私聊引用续办时，只列原回复实际使用的来源。search指定query；查微信时填thread_id，封存档案从archive_sources选择并填dataset=archive，查工作站时填source=station且省略thread_id。查抖音时填source=douyin及source_id，可用work_id过滤。抖音只检索本机已留存资料。默认5条摘要。read凭本轮reference按字节或来源目录分页，封存档案可填source_index读取对应的原始消息。搜索续页只填cursor与可选limit。结果只私下回传本人。", `{"type":"object","properties":{"action":{"type":"string","enum":["sources","search","read"]},"source":{"type":"string","enum":["wechat","station","douyin"]},"source_id":{"type":"string"},"work_id":{"type":"string"},"thread_id":{"type":"string"},"dataset":{"type":"string","enum":["archive"]},"source_index":{"type":"integer","minimum":0},"query":{"type":"string"},"since":{"type":"string"},"before":{"type":"string"},"cursor":{"type":"string"},"reference":{"type":"string"},"limit":{"type":"integer","minimum":1},"offset":{"type":"integer","minimum":0},"length":{"type":"integer","minimum":1},"source_offset":{"type":"integer","minimum":0},"source_limit":{"type":"integer","minimum":1}},"required":["action"],"additionalProperties":false}`)
}

// Only configuration metadata is considered for discovery. No source text or
// cross-conversation query is loaded into an ordinary first model request.
func (t *stationAttachmentTools) crossAuthorization() string {
	if t.trace.PrivateQuote != "" {
		if t.linkedQuoteCandidate() {
			return "wechat-quote-" + localCheckpointKey(t.trace.Account, t.trace.Thread, t.trace.TraceID, t.trace.PrivateQuote)
		}
		return ""
	}
	wechat, station, douyin := t.crossDirectionAuthorization(false), t.crossDirectionAuthorization(true), t.crossFileAuthorization(stationWechatToDouyinGrantsFile)
	if station == "" && douyin == "" {
		return wechat
	}
	return "cross-" + localCheckpointKey(wechat, station, douyin)
}

func (t *stationAttachmentTools) crossDirectionAuthorization(stationSource bool) string {
	filename := stationWechatCrossGrantsFile
	if stationSource {
		filename = stationWechatToStationGrantsFile
	}
	return t.crossFileAuthorization(filename)
}

func (t *stationAttachmentTools) crossFileAuthorization(filename string) string {
	stationSource := filename == stationWechatToStationGrantsFile
	douyinSource := filename == stationWechatToDouyinGrantsFile
	if t.crossHost == nil || t.crossHost.ownerReader == nil || t.store == nil || t.blocked == nil || t.blocked() || t.trace.Preview || t.trace.Surf || t.trace.ReportOnly || t.trace.Channel != "wechat" || t.trace.Mode != "direct" || !t.trace.Directed {
		return ""
	}
	if stationSource && (t.crossHost.stationConversation == nil || !mentionsStationMemory(t.trace.OriginalText)) {
		return ""
	}
	if douyinSource && (t.crossHost.localChat == nil || !mentionsDouyinMemory(t.trace.OriginalText)) {
		return ""
	}
	owner, err := t.crossHost.ownerReader()
	if err != nil {
		return ""
	}
	w, err := t.crossHost.worker(t.crossConnection, false)
	if err != nil || w == nil {
		return ""
	}
	g, b, p, err := w.wechatFileAuthority(t.trace.Account, filename)
	if err != nil || b.Binding == nil || g.OwnerRevision != b.Revision || b.Binding.Sender != t.trace.Sender || b.Binding.Fish != owner.FishID || b.Binding.Owner != owner.ID || g.Fish != owner.FishID || g.Owner != owner.ID || g.Origins[t.trace.Thread].IsZero() || !g.Origins[t.trace.Thread].Equal(p.Grants[t.trace.Thread]) {
		return ""
	}
	if stationSource {
		return "station-" + localCheckpointKey(g.Account, g.Revision, b.Revision, owner, t.trace.Thread, t.trace.Sender, g.Origins[t.trace.Thread], p.Grants[b.Binding.Sender])
	}
	if douyinSource {
		if len(g.AppSources) == 0 {
			return ""
		}
		return "douyin-" + localCheckpointKey(g.Account, g.Revision, b.Revision, owner, t.trace.Thread, t.trace.Sender, g.Origins[t.trace.Thread], p.Grants[b.Binding.Sender], g.AppSources)
	}
	current := map[string]any{}
	for thread, stamp := range g.Sources {
		if thread != t.trace.Thread && stamp.Equal(p.HistoryGrants[thread]) {
			current[thread] = stamp
		}
	}
	if len(current) == 0 {
		return ""
	}
	return "cross-" + localCheckpointKey(g.Account, g.Revision, b.Revision, owner, t.trace.Thread, t.trace.Sender, g.Origins[t.trace.Thread], p.Grants[b.Binding.Sender], current)
}

func mentionsStationMemory(original *string) bool {
	if original == nil {
		return false
	}
	text := strings.ToLower(*original)
	return strings.Contains(text, "工作站") || strings.Contains(text, "本机聊天") || strings.Contains(text, "本地聊天") || strings.Contains(text, "station")
}

func (t *stationAttachmentTools) crossProof(ctx context.Context, old stationWechatCrossProof) (stationWechatCrossProof, error) {
	return t.crossScopeProof(ctx, old, false)
}

func (t *stationAttachmentTools) crossScopeProof(ctx context.Context, old stationWechatCrossProof, forward bool) (stationWechatCrossProof, error) {
	if t.trace.PrivateQuote != "" {
		if forward {
			return stationWechatCrossProof{}, apphost.ErrObservationForbidden
		}
		return t.linkedCrossProof(ctx, old)
	}
	deny := func() (stationWechatCrossProof, error) {
		return stationWechatCrossProof{}, apphost.ErrObservationForbidden
	}
	filename := stationWechatCrossGrantsFile
	if old.Station {
		filename = stationWechatToStationGrantsFile
	}
	if old.Douyin {
		filename = stationWechatToDouyinGrantsFile
	}
	if old.Station && old.Douyin || old.Archived && (old.Station || old.Douyin || forward) || t.crossFileAuthorization(filename) == "" || ctx.Err() != nil {
		return deny()
	}
	owner, err := t.crossHost.ownerReader()
	if err != nil {
		return deny()
	}
	access, err := t.crossHost.verifyWechatMemoryFile(ctx, t.crossConnection, t.trace, old.Thread, owner, forward, filename)
	if err != nil {
		return deny()
	}
	source, ok := t.store.SourceForInput(t.trace)
	if !ok || source.EventID == "" || source.Revision < 1 {
		return deny()
	}
	visible, err := t.store.NativeRecallAllowed(ctx, t.trace)
	if err != nil || !visible {
		return deny()
	}
	epoch, err := t.store.RecallEpoch(ctx)
	if err != nil {
		return deny()
	}
	next := stationWechatCrossProof{Thread: old.Thread, Digest: access.Digest, Instance: t.store.SourceInstanceID(), Epoch: epoch, Event: source.EventID, EventRevision: source.Revision, GrantRevision: access.GrantRevision, Station: old.Station, Douyin: old.Douyin, Archived: old.Archived}
	if old.Archived {
		worker, err := t.crossHost.worker(t.crossConnection, false)
		if err != nil || worker == nil || !t.store.LegacyHistoryAllowed(t.trace.Account, old.Thread) {
			return deny()
		}
		_, _, policy, err := worker.crossAuthority(t.trace.Account)
		if err != nil {
			return deny()
		}
		next.ArchiveGrant = policy.HistoryGrants[old.Thread]
		archive, err := readStationLegacyMemory(worker.dir, t.trace.Account, old.Thread, next.ArchiveGrant)
		if err != nil {
			return deny()
		}
		next.ArchiveFingerprint = localCheckpointKey(archive)
		records, _, err := t.crossArchiveRecords(ctx, next)
		if err != nil {
			return deny()
		}
		next.ArchiveVisibility = localCheckpointKey(records)
	}
	if old.Station {
		store, scope, err := t.stationMemoryStore()
		if err != nil {
			return deny()
		}
		next.StationInstance, next.StationScope = store.SourceInstanceID(), scope
		next.StationEpoch, err = store.RecallEpoch(ctx)
		if err != nil {
			return deny()
		}
	}
	if old.Douyin && old.DouyinScope != "" {
		if err := t.checkWechatDouyinStoredProof(ctx, owner, old); err != nil {
			return deny()
		}
		next.DouyinKind, next.DouyinScope, next.DouyinBinding, next.DouyinEpoch, next.DouyinRevision =
			old.DouyinKind, old.DouyinScope, old.DouyinBinding, old.DouyinEpoch, old.DouyinRevision
	}
	if old.Digest != "" && old != next {
		return deny()
	}
	fresh, err := t.crossHost.ownerReader()
	if err != nil || fresh != owner || t.blocked() {
		return deny()
	}
	if !forward && t.cross != nil {
		t.cross.Target = access.PrivateTarget
	}
	return next, nil
}

func (t *stationAttachmentTools) stationMemoryStore() (*companion.Store, string, error) {
	owner, err := t.crossHost.ownerReader()
	if err != nil {
		return nil, "", err
	}
	store, in, err := t.crossHost.stationConversation()
	if err != nil || store == nil || in.Channel != companion.LocalChannel || in.Account != owner.FishID || in.Thread != owner.ID || in.Sender != owner.ID {
		return nil, "", apphost.ErrObservationForbidden
	}
	return store, companion.ScopeID(in.Channel, in.Account, in.Thread), nil
}

func (t *stationAttachmentTools) crossMemoryReader(proof stationWechatCrossProof) (localMemoryTools, error) {
	if !proof.Station {
		return localMemoryTools{store: t.store, scope: companion.ScopeID("wechat", t.trace.Account, proof.Thread)}, nil
	}
	store, scope, err := t.stationMemoryStore()
	if err != nil || scope != proof.StationScope || store.SourceInstanceID() != proof.StationInstance {
		return localMemoryTools{}, apphost.ErrObservationForbidden
	}
	return localMemoryTools{store: store, scope: scope}, nil
}

func (t *stationAttachmentTools) checkCrossResults(ctx context.Context) error {
	if t.cross == nil {
		return nil
	}
	for _, proof := range t.cross.Proofs {
		if _, err := t.crossProof(ctx, proof); err != nil {
			return err
		}
	}
	if t.cross.Douyin != nil {
		return t.cross.Douyin.checkDouyinMemoryResults(ctx)
	}
	return nil
}

func (t *stationAttachmentTools) callCrossMemory(ctx context.Context, raw json.RawMessage) (string, error) {
	var in stationWechatCrossInput
	if strictLocalMemoryDecode(raw, &in) != nil || t.crossAuthorization() == "" {
		return "", apphost.ErrObservationForbidden
	}
	if t.cross == nil {
		t.cross = &stationWechatCrossTurn{Required: true, Refs: map[string]stationWechatCrossRef{}, Pages: map[string]stationWechatCrossPage{}}
	}
	if t.isDouyinMemoryCall(in) {
		return t.callWechatDouyinMemory(ctx, in)
	}
	if in.SourceID != "" || in.WorkID != "" {
		return "", apphost.ErrObservationInvalid
	}
	noPages := in.Reference == "" && in.Offset == nil && in.Length == nil && in.SourceOffset == nil && in.SourceLimit == nil && in.SourceIndex == nil
	switch in.Action {
	case "sources":
		if !noPages || in.Source != "" || in.Dataset != "" || in.Thread != "" || in.Query != "" || in.Since != "" || in.Before != "" || in.Cursor != "" || in.Limit != nil {
			return "", errors.New("来源目录不接受查询或分页参数")
		}
		if t.trace.PrivateQuote != "" {
			return t.linkedCrossSources(ctx)
		}
		w, err := t.crossHost.worker(t.crossConnection, false)
		if err != nil || w == nil {
			return "", apphost.ErrUnavailable
		}
		threads := []string{}
		if t.crossDirectionAuthorization(false) != "" {
			g, _, p, err := w.crossAuthority(t.trace.Account)
			if err != nil {
				return "", err
			}
			for thread, stamp := range g.Sources {
				if thread != t.trace.Thread && stamp.Equal(p.HistoryGrants[thread]) {
					threads = append(threads, thread)
				}
			}
		}
		stationAvailable := t.crossDirectionAuthorization(true) != ""
		if stationAvailable {
			proof, err := t.crossProof(ctx, stationWechatCrossProof{Station: true})
			if err != nil {
				return "", err
			}
			t.cross.Proofs = append(t.cross.Proofs, proof)
		}
		slices.Sort(threads)
		for _, thread := range threads {
			proof, err := t.crossProof(ctx, stationWechatCrossProof{Thread: thread})
			if err != nil {
				return "", err
			}
			t.cross.Proofs = append(t.cross.Proofs, proof)
		}
		if err := t.checkCrossResults(ctx); err != nil {
			return "", err
		}
		douyinSources := []localDouyinMemorySource{}
		if t.crossFileAuthorization(stationWechatToDouyinGrantsFile) != "" {
			result, err := t.callWechatDouyinMemory(ctx, stationWechatCrossInput{Action: "sources", Source: "douyin"})
			if err != nil {
				return "", err
			}
			var listing struct{ Sources []localDouyinMemorySource }
			if json.Unmarshal([]byte(result), &listing) != nil {
				return "", apphost.ErrUnavailable
			}
			douyinSources = listing.Sources
		}
		archived, err := t.crossArchiveSources(w, threads)
		if err != nil {
			return "", err
		}
		if err := t.checkCrossResults(ctx); err != nil {
			return "", err
		}
		return localMemoryJSON(map[string]any{"account_id": t.trace.Account, "sources": threads, "archive_sources": archived, "station_available": stationAvailable, "douyin_sources": douyinSources, "delivery": "owner_private_only"})
	case "read":
		if in.Reference == "" || in.Source != "" || in.Dataset != "" || in.Thread != "" || in.Query != "" || in.Since != "" || in.Before != "" || in.Cursor != "" || in.Limit != nil {
			return "", errors.New("请凭本轮引用读取")
		}
		ref, ok := t.cross.Refs[in.Reference]
		if !ok {
			return "", apphost.ErrObservationForbidden
		}
		if _, err := t.crossProof(ctx, ref.Proof); err != nil {
			return "", err
		}
		if ref.Proof.Archived {
			return t.readCrossArchive(ctx, ref, in)
		}
		if in.SourceIndex != nil {
			return "", apphost.ErrObservationInvalid
		}
		args, _ := json.Marshal(localMemoryReadInput{ID: ref.ID, Expected: &ref.Version, Offset: in.Offset, Length: in.Length, SourceOffset: in.SourceOffset, SourceLimit: in.SourceLimit})
		reader, err := t.crossMemoryReader(ref.Proof)
		if err != nil {
			return "", err
		}
		result, err := reader.Call(ctx, "memory_read", args)
		if err != nil {
			return "", err
		}
		if err := t.checkCrossResults(ctx); err != nil {
			return "", err
		}
		return t.finishCrossMemory(ctx, ref.Proof, false, 1, result)
	case "search":
		if !noPages || in.Source != "" && in.Source != "wechat" && in.Source != "station" || in.Dataset != "" && (in.Dataset != "archive" || in.Source == "station") {
			return "", errors.New("搜索不接受读取参数")
		}
		limit := 5
		if in.Limit != nil {
			limit = *in.Limit
		}
		if limit < 1 {
			return "", errors.New("每页数量须为正数")
		}
		page := stationWechatCrossPage{Proof: stationWechatCrossProof{Thread: in.Thread, Station: in.Source == "station", Archived: in.Dataset == "archive"}, Query: in.Query, Since: in.Since, Before: in.Before}
		if in.Cursor != "" {
			var ok bool
			page, ok = t.cross.Pages[in.Cursor]
			if !ok || in.Source != "" || in.Dataset != "" || in.Thread != "" || in.Query != "" || in.Since != "" || in.Before != "" {
				return "", companion.ErrMemoryConflict
			}
		}
		if page.Proof.Station && page.Proof.Thread != "" || !page.Proof.Station && !identity.ValidPathID(page.Proof.Thread) || page.Query == "" || len(page.Query) > 1000 {
			return "", errors.New("请指定来源会话和查询词")
		}
		proof, err := t.crossProof(ctx, page.Proof)
		if err != nil {
			return "", err
		}
		if proof.Archived {
			return t.searchCrossArchive(ctx, page, proof, limit)
		}
		reader, err := t.crossMemoryReader(proof)
		if err != nil {
			return "", err
		}
		found, err := reader.store.SearchMemory(ctx, companion.MemoryQuery{Scopes: []string{reader.scope}, Query: page.Query, Since: page.Since, Before: page.Before, Cursor: page.Cursor, Limit: limit})
		if err != nil {
			return "", err
		}
		items := []map[string]any{}
		sourceKind := "wechat"
		if proof.Station {
			sourceKind = "station"
		}
		for _, hit := range found.Items {
			if hit.Entry == nil {
				continue
			}
			e := hit.Entry
			key := "reference-" + identity.RandomToken()
			t.cross.Refs[key] = stationWechatCrossRef{Proof: proof, ID: e.ID, Version: e.Version}
			summary := []rune(e.Summary)
			if len(summary) > 240 {
				summary = summary[:240]
			}
			items = append(items, map[string]any{"reference": key, "summary": string(summary), "kind": e.Kind, "revision": e.Version, "source": sourceKind, "thread_id": proof.Thread, "score": hit.Score})
		}
		next := ""
		if found.HasMore {
			next = "cursor-" + identity.RandomToken()
			page.Proof, page.Cursor = proof, found.NextCursor
			t.cross.Pages[next] = page
		}
		t.cross.Proofs = append(t.cross.Proofs, proof)
		if err := t.checkCrossResults(ctx); err != nil {
			return "", err
		}
		result, err := localMemoryJSON(map[string]any{"items": items, "source": sourceKind, "thread_id": proof.Thread, "next_cursor": next, "effective_limit": found.EffectiveLimit, "coverage": found.Coverage, "data_role": "external_memory_evidence", "delivery": "owner_private_only"})
		if err != nil {
			return "", err
		}
		return t.finishCrossMemory(ctx, proof, true, len(items), result)
	}
	return "", errors.New("请选择sources、search或read")
}

func (t *stationAttachmentTools) finishCrossMemory(ctx context.Context, proof stationWechatCrossProof, search bool, count int, result string) (string, error) {
	if err := t.auditCrossMemory(proof, search, count, result); err != nil {
		return "", err
	}
	if err := t.checkCrossResults(ctx); err != nil {
		return "", err
	}
	if count > 0 && !proof.Douyin && !slices.Contains(t.cross.Used, proof) {
		t.cross.Used = append(t.cross.Used, proof)
	}
	return result, nil
}

func (t *stationAttachmentTools) PrivateReplyRequired() bool {
	return t.cross != nil && t.cross.Required
}

func (t *stationAttachmentTools) PrivateReplyUnavailableNotice() string {
	if t.PrivateReplyRequired() && t.trace.IsGroup && t.PrivateReplyTarget() == "" {
		return "当前无法私下回传，请先核对本人的私聊授权和连接。"
	}
	return ""
}
func (t *stationAttachmentTools) PrivateReplyTarget() string {
	if t.cross == nil {
		return ""
	}
	return t.cross.Target
}

func (t *stationAttachmentTools) auditCrossMemory(proof stationWechatCrossProof, search bool, count int, result string) error {
	w, err := t.crossHost.worker(t.crossConnection, false)
	if err != nil || w == nil {
		return apphost.ErrUnavailable
	}
	owner, err := t.crossHost.ownerReader()
	if err != nil {
		return err
	}
	direction, thread := "wechat_to_wechat", proof.Thread
	keyKind := "wechat-cross"
	if proof.Archived {
		direction, keyKind = "wechat_to_wechat_archive", "wechat-cross-archive"
	}
	if proof.Station {
		direction, thread = "wechat_to_station", proof.StationScope
		keyKind = direction
	}
	if proof.Douyin {
		direction, thread = "wechat_to_douyin", "douyin"
		keyKind = direction
		if t.cross != nil && t.cross.Douyin != nil {
			scopes := []string{}
			for sourceProof := range t.cross.Douyin.douyinMemoryProofs {
				scopes = append(scopes, sourceProof.Source.Scope)
			}
			slices.Sort(scopes)
			if len(scopes) > 0 {
				thread = strings.Join(slices.Compact(scopes), ".")
			}
		}
	}
	key := localCheckpointKey(keyKind, t.trace.Account, t.trace.Thread, t.trace.TraceID, thread, proof.GrantRevision)
	return writeWechatMemoryAudit(w, localWechatMemoryAudit{Direction: direction, OriginThread: t.trace.Thread, Account: t.trace.Account, Thread: thread, Fish: owner.FishID, Owner: owner.ID, Request: key, Revision: proof.GrantRevision}, search, count, result)
}
