package httpwire

import "context"

type decodedBodyKey struct{}

// WithDecodedBody preserves the strict JSON decode, including explicit null fields.
// Framework binding still owns routing and method dispatch.
func WithDecodedBody(ctx context.Context, body any) context.Context {
	return context.WithValue(ctx, decodedBodyKey{}, body)
}

func DecodedBody[T any](ctx context.Context) (*T, bool) {
	value, ok := ctx.Value(decodedBodyKey{}).(*T)
	return value, ok
}
