package admin

import "context"

type outputAuthorityKey struct{}

// The core owns media encoding and upload; the caller retains its live
// authorization across those slow operations. Never persist this callback.
func withOutputAuthority(ctx context.Context, check func(context.Context) error) context.Context {
	return context.WithValue(ctx, outputAuthorityKey{}, check)
}

func checkOutputAuthority(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if check, ok := ctx.Value(outputAuthorityKey{}).(func(context.Context) error); ok {
		return check(ctx)
	}
	return nil
}
