package httpwire

import (
	"context"
	"io"
	"net/http"

	"github.com/gogf/gf/v2/net/ghttp"
)

type rawBodyKey struct{}
type rawRequestBody struct {
	body   io.ReadCloser
	length int64
}

// HideRawBody prevents GF's generic request conversion from consuming a stream or
// reparsing strict JSON. Native controllers restore it before domain validation.
func HideRawBody(req *http.Request) *http.Request {
	ctx := context.WithValue(req.Context(), rawBodyKey{}, rawRequestBody{req.Body, req.ContentLength})
	cloned := req.WithContext(ctx)
	cloned.Body = http.NoBody
	cloned.ContentLength = 0
	return cloned
}

func HasRawBody(ctx context.Context) bool {
	_, ok := ctx.Value(rawBodyKey{}).(rawRequestBody)
	return ok
}

// RestoreRawBody is idempotent and preserves the original content length/headers.
// Call it only at the native controller boundary, after GF has bound path/query.
func RestoreRawBody(ctx context.Context) *http.Request {
	gf := ghttp.RequestFromCtx(ctx)
	if gf == nil {
		return nil
	}
	req := gf.Request
	if raw, ok := ctx.Value(rawBodyKey{}).(rawRequestBody); ok {
		req.Body = raw.body
		req.ContentLength = raw.length
	}
	return req
}
