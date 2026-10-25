package bot

import "errors"

// Options are owner-controlled; model decisions cannot change delivery policy.
type Options struct {
	AcknowledgeTools  bool `json:"acknowledge_tools"`
	ReadImages        bool `json:"read_images"`
	ReadVideos        bool `json:"read_videos"`
	QuoteReplies      bool `json:"quote_replies"`
	RandomDelay       bool `json:"random_delay"`
	DelayMinSeconds   int  `json:"delay_min_seconds"`
	DelayMaxSeconds   int  `json:"delay_max_seconds"`
	RecoverBacklog    bool `json:"recover_backlog"`
	BacklogHours      int  `json:"backlog_hours"`
	RetryUncertain    bool `json:"retry_uncertain"`
	RetryDelaySeconds int  `json:"retry_delay_seconds"`
}

func DefaultOptions() Options {
	return Options{AcknowledgeTools: true, ReadImages: true, ReadVideos: true, QuoteReplies: true, RandomDelay: true, DelayMinSeconds: 3, DelayMaxSeconds: 15, RecoverBacklog: true, BacklogHours: 24, RetryUncertain: true, RetryDelaySeconds: 120}
}
func (o Options) Validate() error {
	if o.DelayMinSeconds < 0 || o.DelayMaxSeconds < o.DelayMinSeconds || o.DelayMaxSeconds > 180 || o.BacklogHours < 1 || o.BacklogHours > 168 || o.RetryDelaySeconds < 30 || o.RetryDelaySeconds > 3600 {
		return errors.New("请检查发送延迟、重启回补与重发等待范围")
	}
	return nil
}
