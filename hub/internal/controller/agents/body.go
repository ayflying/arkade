package agents

import (
	api "arkade/hub/api/agents/v1"
	"arkade/hub/internal/httpwire"
	"context"
	"encoding/json"
	"net/http"
)

// Decode only at the controller boundary and only after the legacy admin gate.
// GF remains authoritative for path identity; JSON cannot override AgentID.
func createBody(ctx context.Context, req *api.CreateReq) (*api.CreateReq, error) {
	if httpwire.HasRawBody(ctx) {
		r := httpwire.RestoreRawBody(ctx)
		body := new(api.CreateReq)
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 16<<10)).Decode(body); err != nil {
			return nil, badRequest("请求体格式错误")
		}
		return body, nil
	}
	if decoded, ok := httpwire.DecodedBody[api.CreateReq](ctx); ok {
		return decoded, nil
	}
	return req, nil
}
func patchBody(ctx context.Context, req *api.PatchReq) (*api.PatchReq, error) {
	if httpwire.HasRawBody(ctx) {
		r := httpwire.RestoreRawBody(ctx)
		body := new(api.PatchReq)
		if err := json.NewDecoder(r.Body).Decode(body); err != nil {
			return nil, badRequest("请求格式错误: " + err.Error())
		}
		body.AgentID = req.AgentID
		return body, nil
	}
	if decoded, ok := httpwire.DecodedBody[api.PatchReq](ctx); ok {
		body := *decoded
		body.AgentID = req.AgentID
		return &body, nil
	}
	return req, nil
}
