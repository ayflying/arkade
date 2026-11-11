package external

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	api "arkade/hub/api/external/v1"
	"arkade/hub/internal/controller"
	compose "arkade/hub/internal/service/compose"
	"arkade/hub/internal/user"
)

func (c *Controller) Copy(ctx context.Context, req *api.CopyReq) (*api.CopyRes, error) {
	if _, err := c.requireExternal(ctx, req.AgentID, req.Name, user.PermManage); err != nil {
		return nil, err
	}
	var input api.CopyInput
	if err := decode(ctx, &input); err != nil {
		return nil, err
	}
	if err := c.access.Require(ctx, input.TargetAgent, user.PermManage); err != nil {
		return nil, err
	}
	if err := compose.ValidateName(input.TargetName); err != nil {
		return nil, controller.Failure("bad_request", err.Error())
	}
	if !input.Confirmation {
		return nil, controller.Failure("bad_request", "请确认本次只复制配置，不含数据、镜像或构建文件，目标不会启动")
	}
	if err := compose.ValidateContent(input.Content); err != nil {
		return nil, controller.Failure("bad_request", err.Error())
	}
	if _, err := c.deps.DB.GetAgent(ctx, input.TargetAgent); err != nil {
		return nil, c.storeError(err, "目标服务器不存在")
	}
	list, cerr := c.deps.Ops.ComposeList(ctx, input.TargetAgent)
	if cerr != nil {
		return nil, controller.ConnectionError(cerr)
	}
	for _, existing := range append(list.External, list.Projects...) {
		if existing.Name == input.TargetName {
			return nil, controller.Failure("conflict", "目标引擎已有同名项目，请更换名称，未覆盖")
		}
	}
	rec := newRecord(input.TargetAgent, input.TargetName, input.Content, "")
	if err := c.deps.DB.CreateComposeStack(ctx, rec); err != nil {
		return nil, c.storeError(err, "目标已存在同名项目，未覆盖")
	}
	secret(ctx)
	return &api.CopyRes{Stack: compose.StackViewOf(rec, nil), Scope: []string{"config"}, DataIncluded: false, Started: false, SourcePreserved: true,
		Message: "仅配置已复制到目标Hub记录；未复制环境文件、构建文件、镜像或卷数据，未下发或启动目标，源项目保留不变"}, nil
}

func newID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic("生成随机 ID 失败: " + err.Error())
	}
	return "stk-" + hex.EncodeToString(buf)
}
