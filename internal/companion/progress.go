package companion

import "context"

type progressKey struct{}
type mediaIntentKey struct{}
type MediaIntentHandler func(context.Context, string) error

func WithMediaIntent(ctx context.Context, handler MediaIntentHandler) context.Context {
	return context.WithValue(ctx, mediaIntentKey{}, handler)
}
func emitMediaIntent(ctx context.Context, intent string) error {
	if fn, ok := ctx.Value(mediaIntentKey{}).(MediaIntentHandler); ok && fn != nil {
		return fn(ctx, intent)
	}
	return nil
}

type ProgressHandler func(context.Context, string) (bool, error)

// Progress is scoped to one incoming task, so previews and simultaneous future
// conversations cannot inherit a real delivery callback from an Engine field.
func WithProgress(ctx context.Context, handler ProgressHandler) context.Context {
	return context.WithValue(ctx, progressKey{}, handler)
}
func emitProgress(ctx context.Context, text string) (bool, error) {
	if fn, ok := ctx.Value(progressKey{}).(ProgressHandler); ok && fn != nil {
		return fn(ctx, text)
	}
	return false, nil
}
