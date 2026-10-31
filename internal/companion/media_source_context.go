package companion

import "context"

type mediaSourceContextKey struct{}

// WithMediaSource carries a host-resolved original event into media adapters.
// It does not grant permission: adapters must recheck scope/version before use.
func WithMediaSource(ctx context.Context, source SourceRef) context.Context {
	return context.WithValue(ctx, mediaSourceContextKey{}, source)
}

func MediaSourceFromContext(ctx context.Context) (SourceRef, bool) {
	source, ok := ctx.Value(mediaSourceContextKey{}).(SourceRef)
	return source, ok && source.EventID != "" && source.Revision > 0
}
