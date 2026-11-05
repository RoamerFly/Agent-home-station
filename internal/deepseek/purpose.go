package deepseek

import "context"

type purposeKey struct{}
type usageObserverKey struct{}

func WithPurpose(ctx context.Context, purpose string) context.Context {
	return context.WithValue(ctx, purposeKey{}, purpose)
}
func WithUsageObserver(ctx context.Context, fn func(Usage)) context.Context {
	return context.WithValue(ctx, usageObserverKey{}, fn)
}
