package station

import (
	"context"
	"errors"
	"time"
)

func (m *localDouyinWebLogin) autoCommentLikeStep(ctx context.Context) error {
	_, err := m.commentLikeStep(ctx, "auto")
	return err
}

func (m *localDouyinWebLogin) autoCommentLikeBackground() {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-timer.C:
		}
		runCtx, cancel := context.WithTimeout(m.ctx, 3*time.Minute)
		didBatch, err := m.commentLikeStep(runCtx, "batch")
		if !didBatch && err == nil {
			_, err = m.commentLikeStep(runCtx, "auto")
		}
		cancel()
		wait := 12 * time.Second
		if err != nil && !errors.Is(err, errLocalDouyinNoticeGrant) {
			wait = 45 * time.Second
		}
		timer.Reset(wait)
	}
}
