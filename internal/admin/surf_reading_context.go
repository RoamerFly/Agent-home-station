package admin

import (
	"context"
	"encoding/json"
	"slices"

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
		recent = append(recent, surfReadingExcerpt{row.CandidateID, row.URL, c.StartOffset, c.EndOffset, c.TotalCharacters, logText(c.Excerpt, 300)})
		if len(recent) == 3 {
			break
		}
	}
	slices.Reverse(recent)
	return recent
}

type surfReadingExcerpt struct {
	CandidateID string `json:"candidate_id"`
	URL         string `json:"url"`
	Start       int    `json:"start_offset"`
	End         int    `json:"end_offset_exclusive"`
	Total       int    `json:"total_characters"`
	Excerpt     string `json:"external_text_excerpt"`
}
