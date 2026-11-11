package httpx

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"arkade/hub/internal/httpwire"
	"github.com/gogf/gf/v2/errors/gerror"
)

// Management endpoints historically return the JSON decoder's exact diagnostic.
// Keep this separate from the login guard, whose intentionally generic errors differ.
func sanitizeModuleBody(req *http.Request, target any) (*http.Request, error) {
	stripQuery(req)
	stripFormContentType(req)
	if target == nil {
		return req, nil
	}
	var raw []byte
	if req.Body != nil {
		var err error
		raw, err = io.ReadAll(io.LimitReader(req.Body, MaxBodyBytes+1))
		_ = req.Body.Close()
		if err != nil {
			return req, gerror.New("请求格式错误: " + err.Error())
		}
	}
	req.Body = io.NopCloser(bytes.NewReader(raw))
	if err := httpwire.DecodeJSON(req, target); err != nil {
		req.Body = io.NopCloser(bytes.NewReader(raw))
		return req, gerror.New("请求格式错误: " + err.Error())
	}
	// Legacy DecodeJSON consumes only the first JSON value. Do not expose trailing
	// values to the framework's second parse; controllers consume the strict decode.
	var first json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&first); err != nil {
		return req, gerror.New("请求格式错误: " + err.Error())
	}
	req.Body = io.NopCloser(bytes.NewReader(first))
	req.ContentLength = int64(len(first))
	return req, nil
}
