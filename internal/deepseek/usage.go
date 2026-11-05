package deepseek

import (
	"encoding/json"
	"time"
)

// Usage only contains provider-reported counts, never prompts or credentials.
// Each HTTP attempt is observed, including billed responses later rejected by
// decision validation. Missing usage is unknown, not zero-token consumption.
type Usage struct {
	Model    string    `json:"model,omitempty"` // Actual model requested for this HTTP attempt.
	Purpose  string    `json:"purpose,omitempty"`
	Time     time.Time `json:"time"`
	Reported bool      `json:"reported"`
	Input    int64     `json:"input"`
	Output   int64     `json:"output"`
	CacheHit int64     `json:"cache_hit"`
}

func parseUsage(body []byte, stamp time.Time) Usage {
	u := Usage{Time: stamp}
	var response struct {
		Usage *struct {
			Prompt     *int64 `json:"prompt_tokens"`
			Completion *int64 `json:"completion_tokens"`
			Hit        *int64 `json:"prompt_cache_hit_tokens"`
			Details    struct {
				Cached int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &response) != nil || response.Usage == nil {
		return u
	}
	r := response.Usage
	if r.Prompt == nil || r.Completion == nil {
		return u
	}
	hit := r.Details.Cached
	if r.Hit != nil {
		hit = *r.Hit
	}
	if *r.Prompt < 0 || *r.Prompt > 1_000_000_000 || *r.Completion < 0 || *r.Completion > 1_000_000_000 || hit < 0 || hit > *r.Prompt {
		return u
	}
	u.Reported = true
	u.Input = *r.Prompt
	u.Output = *r.Completion
	u.CacheHit = hit
	return u
}

// ReportedUsage decodes transport evidence for a host-owned plugin model call.
// Plugin output is not a billing source. Missing/invalid counts remain unknown.
func ReportedUsage(body []byte, stamp time.Time) Usage { return parseUsage(body, stamp) }
