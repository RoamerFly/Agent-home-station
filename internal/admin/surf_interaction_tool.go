package admin

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"wechat-bot/internal/mcptools"
)

// surfInteractionOrigin is the host-checked boundary between an exploration
// read and a later public action. It deliberately carries configured-source
// identity rather than trusting a provider's display name or a model URL.
// The durable interaction ledger owns the request key and submission states.
type surfInteractionOrigin surfInteractionRequest

// surfInteractionAdapter is deliberately host-owned. An installed read-only
// web adapter cannot become a social writer merely by returning a URL. Tests
// use a synthetic adapter and production adapters must be registered by the
// host after an explicit source/action grant exists.
type surfInteractionAdapter interface {
	// requestKey is minted by the durable host ledger and must be echoed by
	// the platform receipt. Adapters must never derive a replacement key.
	Submit(context.Context, string, surfInteractionRequest) (surfInteractionReceipt, error)
	Lookup(context.Context, string) (surfInteractionReceipt, error)
	Allows(platform, platformAccount string) bool
}

func configuredSurfInteractionGrant(settings surfSettings, sourceID, action string) (surfInteractionGrant, bool) {
	for _, grant := range settings.InteractionGrants {
		if !grant.Enabled || grant.SourceID != sourceID {
			continue
		}
		for _, allowed := range grant.Actions {
			if allowed == action {
				return grant, true
			}
		}
	}
	return surfInteractionGrant{}, false
}

func surfInteractionGrantStamp(account, thread, scope, interest string, revision int, source mcptools.DiscoverySource, grant surfInteractionGrant) string {
	b, _ := json.Marshal([]any{"surf-interaction-grant-v1", account, thread, scope, interest, revision, source, grant})
	return agentID(string(b))
}

// surfInteractionSource verifies the exact current exploration scope, current
// configured source and an actual verified page-body read. Discovery metadata,
// titles and private autonomous browsing are intentionally insufficient.
func (p *permittedTools) surfInteractionSource(ctx context.Context, candidateID string) (surfInteractionOrigin, error) {
	if p == nil || p.manager == nil || p.privateSurfOrigin != nil || !p.trace.Surf || p.trace.Preview || p.trace.ReportOnly || p.trace.Sender != "" || strings.TrimSpace(candidateID) == "" {
		return surfInteractionOrigin{}, errors.New("surf_interaction_authorization_required")
	}
	account, epoch, scope, interest, err := p.surfExplorationIdentity(ctx, p.trace.SurfTopic)
	if err != nil || account == "" || scope == "" || interest == "" {
		return surfInteractionOrigin{}, errors.New("surf_interaction_authorization_required")
	}
	store := p.manager.surfHistory
	store.mu.Lock()
	var session surfSession
	found := false
	for _, row := range store.data.Sessions {
		if row.ID == p.trace.TraceID && row.Thread == p.trace.Thread {
			session = row
			session.Observations = slices.Clone(row.Observations)
			session.Candidates = slices.Clone(row.Candidates)
			found = true
			break
		}
	}
	store.mu.Unlock()
	if !found || session.Account != account || session.Thread != p.trace.Thread || session.ScopeKey != scope || session.InterestKey != interest || session.AuthorizationEpoch != epoch {
		return surfInteractionOrigin{}, errors.New("surf_interaction_authorization_changed")
	}
	var candidate *mcptools.DiscoveryItem
	for i := range session.Candidates {
		if session.Candidates[i].ID == candidateID {
			candidate = &session.Candidates[i]
			break
		}
	}
	if candidate == nil || candidate.SourceID == "" {
		// Old rows did not retain configured source identity. They remain
		// browseable, but cannot become authority for a state-changing action.
		return surfInteractionOrigin{}, errors.New("surf_interaction_source_unavailable")
	}

	// A source-side action can only follow the exact current candidate body
	// observation. Validate the plugin job again, including its range/hash.
	var observed surfObservation
	readOK := false
	for _, row := range session.Observations {
		if row.CandidateID != candidate.ID || row.State != "body_read" || row.JobID == "" || row.ContentHash == "" {
			continue
		}
		if _, valid := p.validatedSurfReading(ctx, row); valid {
			observed, readOK = row, true
			break
		}
	}
	if !readOK {
		return surfInteractionOrigin{}, errors.New("surf_interaction_requires_verified_read")
	}

	// The source configuration and revision are part of the grant. A removed,
	// disabled or edited source invalidates a pending action even when an old
	// plugin job still exists on disk.
	p.manager.mu.Lock()
	revision := p.manager.surf.SettingsRevision
	settings := p.manager.surf.Settings
	var source *mcptools.DiscoverySource
	if discovery := p.manager.surf.Settings.Discovery; discovery != nil {
		for i := range discovery.Sources {
			item := discovery.Sources[i]
			if item.ID == candidate.SourceID && item.Enabled {
				source = &item
				break
			}
		}
	}
	grant, granted := configuredSurfInteractionGrant(settings, candidate.SourceID, "comment")
	p.manager.mu.Unlock()
	if source == nil || !granted {
		return surfInteractionOrigin{}, errors.New("surf_interaction_source_authorization_changed")
	}
	grantStamp := surfInteractionGrantStamp(account, p.trace.Thread, scope, interest, revision, *source, grant)
	return surfInteractionOrigin{
		Account: account, Thread: p.trace.Thread, SessionID: session.ID, CandidateID: candidate.ID,
		ScopeKey: scope, AuthorizationEpoch: epoch, InterestKey: interest,
		Platform: grant.Platform, PlatformAccount: grant.PlatformAccount, GrantID: grant.SourceID, Grant: grantStamp, SettingsRevision: revision,
		SourceURL: observed.URL, SourceJobID: observed.JobID, SourceContentHash: observed.ContentHash,
		SourceStart: observed.Offset, SourceEnd: observed.Offset + observed.ReturnedCharacters,
	}, nil
}

func (o surfInteractionOrigin) request(text string) surfInteractionRequest {
	r := surfInteractionRequest(o)
	r.Action, r.Text = "comment", strings.TrimSpace(text)
	return r
}

// surfInteractionGrantCurrent validates an older durable request without
// requiring the original trace. It is used before exposing a confirmed action
// as a life experience and before looking up an unknown submission after a
// restart. Regranting a source increments SettingsRevision, so an old request
// cannot become current merely because the same source ID was enabled again.
func (p *permittedTools) surfInteractionGrantCurrent(request surfInteractionRequest) bool {
	if p == nil || p.manager == nil || p.privateSurfOrigin != nil || !p.trace.Surf || p.trace.Preview || p.trace.ReportOnly || p.trace.Sender != "" || request.Action != "comment" || request.Account != p.trace.Account || request.Thread != p.trace.Thread {
		return false
	}
	account, epoch, scope, interest, err := p.surfExplorationIdentity(context.Background(), p.trace.SurfTopic)
	if err != nil || account != request.Account || epoch != request.AuthorizationEpoch || scope != request.ScopeKey || interest != request.InterestKey {
		return false
	}
	p.manager.mu.Lock()
	revision := p.manager.surf.SettingsRevision
	settings := p.manager.surf.Settings
	var source *mcptools.DiscoverySource
	if settings.Discovery != nil {
		for i := range settings.Discovery.Sources {
			item := settings.Discovery.Sources[i]
			if item.ID == request.GrantID && item.Enabled {
				source = &item
				break
			}
		}
	}
	grant, granted := configuredSurfInteractionGrant(settings, request.GrantID, request.Action)
	p.manager.mu.Unlock()
	if source == nil || !granted || request.Platform != grant.Platform || request.PlatformAccount != grant.PlatformAccount || revision != request.SettingsRevision || request.Grant != surfInteractionGrantStamp(account, p.trace.Thread, scope, interest, revision, *source, grant) {
		return false
	}
	read, valid := p.authorizedSurfReadJob(context.Background(), request.SourceJobID, request.SourceURL, request.SourceContentHash)
	return valid && read.ReadCoverage != nil && read.ReadCoverage.StartOffset == request.SourceStart && read.ReadCoverage.EndOffset == request.SourceEnd && read.SHA == request.SourceContentHash
}

// surfInteractionGenerationStamp makes the interaction-grant state visible to
// a generation snapshot without changing ordinary user-memory authorization.
// A pending or confirmed action is still revalidated individually before it is
// shown to the model.
func (p *permittedTools) surfInteractionGenerationStamp() string {
	if p == nil || p.manager == nil || p.privateSurfOrigin != nil || !p.trace.Surf || p.trace.Preview || p.trace.ReportOnly || p.trace.Sender != "" || normalizeSurfInterest(p.trace.SurfTopic) == "" {
		return ""
	}
	p.manager.mu.Lock()
	revision, settings := p.manager.surf.SettingsRevision, p.manager.surf.Settings
	p.manager.mu.Unlock()
	if len(settings.InteractionGrants) == 0 {
		return ""
	}
	account, _, scope, interest, err := p.surfExplorationIdentity(context.Background(), p.trace.SurfTopic)
	if err != nil || interest == "" {
		return ""
	}
	b, _ := json.Marshal([]any{"surf-interaction-generation-v1", account, p.trace.Thread, scope, interest, revision, settings.InteractionGrants})
	return agentID(string(b))
}

func (p *permittedTools) surfInteractionTools() []mcptools.Tool {
	if p == nil || p.manager == nil || p.manager.surfInteractions == nil || p.surfInteractionAdapter == nil || p.privateSurfOrigin != nil || !p.trace.Surf || p.trace.Preview || p.trace.ReportOnly || p.trace.Sender != "" {
		return nil
	}
	p.manager.mu.Lock()
	settings := p.manager.surf.Settings
	p.manager.mu.Unlock()
	if settings.Discovery == nil || !slices.ContainsFunc(settings.Discovery.Sources, func(source mcptools.DiscoverySource) bool {
		grant, ok := configuredSurfInteractionGrant(settings, source.ID, "comment")
		return source.Enabled && ok && p.surfInteractionAdapter.Allows(grant.Platform, grant.PlatformAccount)
	}) {
		return nil
	}
	return []mcptools.Tool{agentTool("surf_interaction", "Comment on a public item only through the explicitly host-authorized external platform account bound to a verified current public reading. Only publish public-safe text. Keep private conversations and credentials out of the comment. action=comment creates at most one durable request; status checks an existing request_key, or lists recent unresolved keys when omitted. Input {action:comment|status,candidate_id?:string,text?:string,request_key?:string}.", `{"type":"object","properties":{"action":{"type":"string","enum":["comment","status"]},"candidate_id":{"type":"string","maxLength":512},"text":{"type":"string","minLength":1,"maxLength":4000},"request_key":{"type":"string","maxLength":256}},"required":["action"],"additionalProperties":false}`)}
}

type surfInteractionToolInput struct {
	Action      string `json:"action"`
	CandidateID string `json:"candidate_id"`
	Text        string `json:"text"`
	RequestKey  string `json:"request_key"`
}

func surfInteractionToolOutput(row surfInteraction, detail string) string {
	out := map[string]any{"request_key": row.RequestKey, "state": row.State, "operation_id": row.ReceiptID, "candidate_id": row.Request.CandidateID, "platform": row.Request.Platform, "platform_account": row.Request.PlatformAccount}
	if detail != "" {
		out["detail"] = detail
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// Only lookup handles from the current authorized interest can be resumed.
func (p *permittedTools) pendingSurfInteractions(ctx context.Context) []map[string]string {
	account, epoch, scope, interest, err := p.surfExplorationIdentity(ctx, p.trace.SurfTopic)
	if err != nil || interest == "" {
		return nil
	}
	var out []map[string]string
	for _, row := range p.manager.surfInteractions.pending(account, p.trace.Thread, scope, interest, 12) {
		r := row.Request
		if r.AuthorizationEpoch == epoch && p.surfInteractionAdapter.Allows(r.Platform, r.PlatformAccount) && p.surfInteractionGrantCurrent(r) {
			out = append(out, map[string]string{"request_key": row.RequestKey, "state": row.State, "candidate_id": r.CandidateID, "platform": r.Platform, "platform_account": r.PlatformAccount})
		}
	}
	return out
}

func (p *permittedTools) callSurfInteraction(ctx context.Context, _ string, raw json.RawMessage) (string, error) {
	var in surfInteractionToolInput
	if json.Unmarshal(raw, &in) != nil {
		return "", toolInputError("冲浪互动参数无效")
	}
	if p == nil || p.manager == nil || p.manager.surfInteractions == nil || p.surfInteractionAdapter == nil {
		return "", errors.New("surf_interaction_unavailable")
	}
	switch in.Action {
	case "comment":
		origin, err := p.surfInteractionSource(ctx, in.CandidateID)
		if err != nil {
			return "", err
		}
		if !p.surfInteractionAdapter.Allows(origin.Platform, origin.PlatformAccount) {
			return "", errors.New("surf_interaction_platform_account_mismatch")
		}
		row, submit, err := p.manager.surfInteractions.claim(origin.request(in.Text))
		if err != nil {
			return "", err
		}
		if !submit {
			return surfInteractionToolOutput(row, ""), nil
		}
		if ctx.Err() != nil || !p.surfInteractionAdapter.Allows(row.Request.Platform, row.Request.PlatformAccount) || !p.surfInteractionGrantCurrent(row.Request) {
			blocked, err := p.manager.surfInteractions.blockUnsubmitted(row.RequestKey)
			if err != nil {
				return surfInteractionToolOutput(row, "已阻止提交，停止状态写入失败"), nil
			}
			return surfInteractionToolOutput(blocked, "提交前任务取消或授权发生变化"), nil
		}
		receipt, err := p.surfInteractionAdapter.Submit(ctx, row.RequestKey, row.Request)
		if err != nil {
			return surfInteractionToolOutput(row, "提交结果未知，请按原请求编号查询"), nil
		}
		return p.checkedInteractionReceipt(row, receipt)
	case "status":
		if strings.TrimSpace(in.RequestKey) == "" {
			b, _ := json.Marshal(map[string]any{"pending": p.pendingSurfInteractions(ctx)})
			return string(b), nil
		}
		row, ok := p.manager.surfInteractions.read(in.RequestKey, p.trace.Account)
		if !ok || !p.surfInteractionGrantCurrent(row.Request) {
			return "", errors.New("surf_interaction_authorization_required")
		}
		if row.State != "submitted_unknown" {
			return surfInteractionToolOutput(row, ""), nil
		}
		if !p.surfInteractionAdapter.Allows(row.Request.Platform, row.Request.PlatformAccount) {
			return "", errors.New("surf_interaction_platform_account_mismatch")
		}
		receipt, err := p.surfInteractionAdapter.Lookup(ctx, row.RequestKey)
		if err != nil {
			return surfInteractionToolOutput(row, "本次未取得平台回执，可稍后按原编号再查"), nil
		}
		return p.checkedInteractionReceipt(row, receipt)
	default:
		return "", toolInputError("action必须为comment或status")
	}
}

// Preserve the durable lookup handle even if a platform returns an invalid
// receipt or saving a valid response fails.
func (p *permittedTools) checkedInteractionReceipt(row surfInteraction, receipt surfInteractionReceipt) (string, error) {
	settled, err := p.manager.surfInteractions.settle(row.RequestKey, receipt)
	if err != nil {
		return surfInteractionToolOutput(row, "回执未能核验或保存，请保留原编号继续查询"), nil
	}
	return surfInteractionToolOutput(settled, ""), nil
}
