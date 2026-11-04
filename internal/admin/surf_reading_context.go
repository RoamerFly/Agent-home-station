package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/pluginhost"
)

// validatedSurfReading is the one authority for reading persisted surf
// evidence. A row is only provenance; the plugin job is rechecked for current
// authorization, source epoch, page hash and byte/rune range before external
// text can return to the model or authorize a share.
func (p *permittedTools) validatedSurfReading(ctx context.Context, row surfObservation) (serviceWebResult, bool) {
	if !p.trace.Surf || row.State != "body_read" || row.CandidateID == "" || row.JobID == "" || row.ContentHash == "" {
		return serviceWebResult{}, false
	}
	store, err := p.manager.packageStore()
	if err != nil {
		return serviceWebResult{}, false
	}
	job, err := store.Job(ctx, row.JobID)
	if err != nil || job.State != "completed" || job.Plugin != "web-search" || job.Tool != "web.read" || (servicePluginPolicy{p: p}).Check(ctx, job, pluginhost.Capability{}) != nil {
		return serviceWebResult{}, false
	}
	var read serviceWebResult
	if json.Unmarshal(job.Result, &read) != nil || !read.OK || !read.Untrusted || read.ReadCoverage == nil || canonicalSurfURL(read.URL) != row.URL || read.SHA != row.ContentHash {
		return serviceWebResult{}, false
	}
	coverage := read.ReadCoverage
	if coverage.Kind != "page_body" || coverage.Excerpt == "" || coverage.StartOffset != row.Offset || coverage.EndOffset != row.Offset+row.ReturnedCharacters || coverage.TotalCharacters != row.TotalCharacters || coverage.Complete != row.Complete {
		return serviceWebResult{}, false
	}
	return read, true
}

// recentReadingExcerpts revisits only reads in this exploration. The history
// stores provenance; the plugin job remains the authority for its content.
func (p *permittedTools) recentReadingExcerpts(ctx context.Context, session surfSession) []surfReadingExcerpt {
	if session.ID != p.trace.TraceID || session.Thread != p.trace.Thread || !p.trace.Surf {
		return nil
	}
	var recent []surfReadingExcerpt
	for _, row := range slices.Backward(session.Observations) {
		if row.State != "body_read" || row.CandidateID == "" || row.JobID == "" {
			continue
		}
		read, ok := p.validatedSurfReading(ctx, row)
		if !ok {
			continue
		}
		c := read.ReadCoverage
		recent = append(recent, surfReadingExcerpt{
			CandidateID: row.CandidateID, URL: row.URL, Platform: row.Platform,
			Start: c.StartOffset, End: c.EndOffset, Total: c.TotalCharacters,
			ObservationPart: readingObservationPart(c),
		})
		if len(recent) == 3 {
			break
		}
	}
	slices.Reverse(recent)
	return recent
}

// priorReadingExcerpts recalls a small amount of actual page text from an
// earlier exploration of the same authorized interest. Candidate titles,
// discovery summaries, failed reads and private-browse claims are deliberately
// not recallable: they are not evidence that a page was read in this
// conversation. The package job is revalidated against the current account,
// conversation and memory epoch before its excerpt can return.
func (p *permittedTools) priorReadingExcerpts(ctx context.Context, session surfSession) []surfReadingExcerpt {
	if !p.reusableSurfReadingScope(ctx, session) {
		return nil
	}
	// Do not snapshot the whole history (it can contain thousands of candidate
	// metadata rows). Hold the history lock only long enough to copy a bounded
	// set of matching provenance rows, then perform the comparatively expensive
	// plugin-job authorization checks without the lock.
	rows := p.priorSurfReadingRows(session)
	prior := []surfReadingExcerpt{}
	for _, row := range rows {
		read, ok := p.validatedSurfReading(ctx, row)
		if !ok {
			continue
		}
		// A clear, correction, revoked authorization or configuration change can
		// race a job read. Re-derive the current scope before returning text.
		if !p.reusableSurfReadingScope(ctx, session) {
			return nil
		}
		coverage := read.ReadCoverage
		prior = append(prior, surfReadingExcerpt{
			CandidateID:     row.CandidateID,
			URL:             row.URL,
			Platform:        row.Platform,
			Start:           coverage.StartOffset,
			End:             coverage.EndOffset,
			Total:           coverage.TotalCharacters,
			ObservationPart: readingObservationPart(coverage),
		})
		if len(prior) == 3 {
			slices.Reverse(prior)
			return prior
		}
	}
	slices.Reverse(prior)
	return prior
}

// priorSurfReadingRows performs the cheap history filter under lock. Twelve
// candidate rows is enough to find the three context excerpts while bounding
// work even when old plugin jobs have been deleted or revoked.
func (p *permittedTools) priorSurfReadingRows(session surfSession) []surfObservation {
	if p == nil || p.manager == nil || p.manager.surfHistory == nil {
		return nil
	}
	const maxChecks = 12
	rows := make([]surfObservation, 0, maxChecks)
	seen := map[string]bool{}
	store := p.manager.surfHistory
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, previous := range slices.Backward(store.data.Sessions) {
		if previous.ID == session.ID || previous.Account != session.Account || previous.Thread != session.Thread || previous.ScopeKey != session.ScopeKey || previous.InterestKey != session.InterestKey {
			continue
		}
		for _, row := range slices.Backward(previous.Observations) {
			if row.State != "body_read" || row.CandidateID == "" || row.JobID == "" || row.ContentHash == "" {
				continue
			}
			key := row.JobID + "\x00" + row.URL + "\x00" + fmt.Sprintf("%d:%d", row.Offset, row.ReturnedCharacters)
			if seen[key] {
				continue
			}
			seen[key] = true
			rows = append(rows, row)
			if len(rows) == maxChecks {
				return rows
			}
		}
	}
	return rows
}

func (p *permittedTools) reusableSurfReadingScope(ctx context.Context, session surfSession) bool {
	// privateSurfOrigin represents one non-persistent scheduler claim. Its
	// history must never authorize a later claim, even if the prompt happens to
	// be similar.
	if p == nil || p.manager == nil || p.privateSurfOrigin != nil || !p.trace.Surf || session.ID != p.trace.TraceID || session.Account != p.trace.Account || session.Thread != p.trace.Thread || session.ScopeKey == "" || session.InterestKey == "" {
		return false
	}
	// The current task's declared topic, not a previous session's free-text
	// value, determines whether an old reading belongs in this exploration.
	// This makes a new interest line a hard recall boundary.
	interest := strings.TrimSpace(p.trace.SurfTopic)
	if interest == "" {
		return false
	}
	account, epoch, scopeKey, interestKey, err := p.surfExplorationIdentity(ctx, interest)
	return err == nil && account == session.Account && epoch == session.AuthorizationEpoch && scopeKey == session.ScopeKey && interestKey == session.InterestKey
}

func readingObservationPart(coverage *serviceWebReadCoverage) companion.ObservationPart {
	return companion.ObservationPart{
		Kind:    companion.ObservationPageBody,
		Range:   fmt.Sprintf("characters %d-%d of %d", coverage.StartOffset, coverage.EndOffset, coverage.TotalCharacters),
		Excerpt: logText(coverage.Excerpt, 300),
	}
}

type surfReadingExcerpt struct {
	CandidateID string `json:"candidate_id"`
	URL         string `json:"url"`
	Platform    string `json:"platform,omitempty"`
	Start       int    `json:"start_offset"`
	End         int    `json:"end_offset_exclusive"`
	Total       int    `json:"total_characters"`
	companion.ObservationPart
}
