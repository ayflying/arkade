package external

import (
	"context"

	api "arkade/hub/api/external/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/httpwire"
	compose "arkade/hub/internal/service/compose"
	"arkade/hub/internal/user"
)

func decode(ctx context.Context, target any) error {
	if err := httpwire.DecodeJSON(httpwire.RestoreRawBody(ctx), target); err != nil {
		return controller.Failure("bad_request", "请求格式错误: "+err.Error())
	}
	return nil
}

func (c *Controller) Draft(ctx context.Context, req *api.DraftReq) (*api.DraftRes, error) {
	if _, err := c.requireExternal(ctx, req.AgentID, req.Name, user.PermManage); err != nil {
		return nil, err
	}
	var input api.DraftInput
	if err := decode(ctx, &input); err != nil {
		return nil, err
	}
	if err := compose.ValidateContent(input.Content); err != nil {
		return nil, controller.Failure("bad_request", err.Error())
	}
	if input.ExpectedVersion < 0 || len(input.SourceHash) > 64 {
		return nil, controller.Failure("bad_request", "草稿版本或来源哈希不合法")
	}
	draft, err := c.deps.DB.SaveExternalComposeDraft(ctx, req.AgentID, req.Name, input.Content, input.SourceHash, input.ExpectedVersion)
	if err != nil {
		return nil, c.storeError(err, "草稿已被其他页面修改，请重新读取后再保存")
	}
	secret(ctx)
	return &api.DraftRes{Draft: draft, Message: "草稿已保存到Hub，未修改宿主文件，未重启或重建容器"}, nil
}
