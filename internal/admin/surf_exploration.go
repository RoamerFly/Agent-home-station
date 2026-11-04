package admin

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// surfExplorationIdentity binds a saved exploration to the exact conversation
// scope in which the host authorized it. ScopeKey is deliberately per current
// authorization/recall epoch; InterestKey is stable across traces in that same
// account and conversation, and is only a lookup hint. A caller must still
// revalidate the saved read before putting page text back into model context.
//
// Private browsing is never a reusable experience source. Its authorization
// includes the non-persisted scheduler claim and it receives no InterestKey.
func (p *permittedTools) surfExplorationIdentity(ctx context.Context, interest string) (account, epoch, scopeKey, interestKey string, err error) {
	if p == nil || p.manager == nil || !p.trace.Surf || strings.TrimSpace(p.trace.Account) == "" || strings.TrimSpace(p.trace.Thread) == "" {
		return "", "", "", "", errors.New("surf_exploration_scope_required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if p.privateSurfOrigin != nil {
		if err := p.privateSurfOrigin.check(ctx, p.trace); err != nil {
			return "", "", "", "", err
		}
		auth := p.privateSurfAuthorization()
		if auth == "" {
			return "", "", "", "", errors.New("surf_exploration_authorization_required")
		}
		privateEpoch, err := p.serviceEvidenceEpoch(ctx)
		if err != nil {
			return "", "", "", "", err
		}
		// serviceEvidenceEpoch for a private claim contains the raw claim. Bind
		// it into the opaque hash but never persist or return it.
		return p.trace.Account, "", agentID("surf-scope-v1", p.trace.Account, p.trace.Thread, auth, privateEpoch), "", nil
	}
	if p.manager.native.Account() != p.trace.Account || !p.manager.surfAllowed(p.trace.Thread) {
		return "", "", "", "", errors.New("surf_exploration_authorization_required")
	}
	p.manager.mu.Lock()
	revision := p.manager.surf.SettingsRevision
	remember := p.manager.config.Companion.Permissions.Remember
	p.manager.mu.Unlock()
	// serviceAuthorization is the existing tool-use authorization stamp. It
	// remains non-empty for a surf worker even though its memory grant is empty.
	// Do not turn the optional memory grant into a requirement for browsing.
	authorization := p.serviceAuthorization()
	if authorization == "" {
		return "", "", "", "", errors.New("surf_exploration_authorization_required")
	}
	epoch, err = p.serviceEvidenceEpoch(ctx)
	if err != nil {
		return "", "", "", "", err
	}
	account = p.trace.Account
	scopeKey = agentID("surf-scope-v1", account, p.trace.Thread, authorization, strconv.Itoa(revision), epoch)
	if normalized := normalizeSurfInterest(interest); normalized != "" && remember {
		interestKey = agentID("surf-interest-v1", account, p.trace.Thread, normalized)
	}
	return account, epoch, scopeKey, interestKey, nil
}

func normalizeSurfInterest(raw string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(raw)), " "))
}

// surfPlatformName gives every candidate a stable display/provenance label.
// Adapters may provide a provider-specific source name, but a blank value must
// not make the candidate anonymous: the configured source is the authority
// that made it eligible for this exploration.
func surfPlatformName(sourceName, itemSource string) string {
	if value := strings.TrimSpace(itemSource); value != "" {
		return value
	}
	return strings.TrimSpace(sourceName)
}
