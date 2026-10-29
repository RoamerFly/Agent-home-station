package companion

import "errors"

type MemorySettings struct {
	Enabled          bool  `json:"enabled"`
	BatchMessages    int   `json:"batch_messages"`
	IdleSeconds      int   `json:"idle_seconds"`
	DailyTokens      int64 `json:"daily_tokens"`
	RecentTokens     int   `json:"recent_tokens"`
	RecallTokens     int   `json:"recall_tokens"`
	ContinuityTokens int   `json:"continuity_tokens"`
	ContextWindow    int   `json:"context_window_tokens"`
	OutputReserve    int   `json:"output_reserve_tokens"`
}

func MemoryDefaults() MemorySettings {
	return MemorySettings{Enabled: true, BatchMessages: 12, IdleSeconds: 300, DailyTokens: 100000, RecentTokens: 4096, RecallTokens: 4096, ContinuityTokens: 1024, OutputReserve: 4096}
}
func (c Config) MemoryConfig() MemorySettings {
	if c.Memory == nil {
		return MemoryDefaults()
	}
	return *c.Memory
}
func (e *Engine) memorySettings() MemorySettings {
	if e.MemoryOptions != nil {
		return e.MemoryOptions()
	}
	return e.Config.MemoryConfig()
}
func (c MemorySettings) Validate() error {
	if c.BatchMessages < 1 || c.IdleSeconds < 0 || c.DailyTokens < 0 || c.RecentTokens < 1 || c.RecallTokens < 1 || c.ContinuityTokens < 0 || c.ContinuityTokens > c.RecallTokens || c.ContextWindow < 0 || c.OutputReserve < 0 {
		return errors.New("invalid memory maintenance or context budget")
	}
	return nil
}
