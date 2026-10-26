package admin

import (
	"context"
	"math/rand/v2"
	"time"
)

func (p *permittedTools) deliveryDelay(ctx context.Context) error {
	c := p.manager.Config().Behavior
	delay := c.DelayMinSeconds
	if delay < 1 {
		delay = 1
	}
	max := c.DelayMaxSeconds
	if max < delay {
		max = delay
	}
	delay += rand.IntN(max - delay + 1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Duration(delay) * time.Second):
		return nil
	}
}
