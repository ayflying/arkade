package external

// retry.go：手动重试某个外部项目的自动接管。
//
// ============================ 为什么必须有这个入口 ============================
//
// 自动发现改造后，两类结论是**不再自动重扫**的：
//   - 确定性阻塞（多配置文件、工作目录不合规、符号链接…）：重扫永远同样失败；
//   - 环境性失败重试次数用尽（缺镜像、Agent 长期离线…）。
//
// 这两种情况都不该无限创建 helper 容器，但用户把问题修好之后
// （换了配置位置、拉好了镜像、重启了引擎）必须有个"再来一次"的按钮。
// 没有这个入口，被跳过的项目就只能靠删了再建，或者等 Hub 重启——
// 那是把一个正常操作变成运维动作。
//
// 权限用 manage（与"查看配置/导入"一致）：重试会触发一次配置读取，
// 属于对目标机器的操作，只读用户不应能触发。

import (
	"context"

	api "arkade/hub/api/external/v1"
	"arkade/hub/internal/user"
)

func (c *Controller) RetryAdopt(ctx context.Context, req *api.RetryAdoptReq) (*api.RetryAdoptRes, error) {
	// requireExternal 已校验权限、项目名合法性，以及"该外部项目确实存在"。
	// 后者很重要：给一个不存在的项目重试只会白建一个 helper 容器。
	if _, err := c.requireExternal(ctx, req.AgentID, req.Name, user.PermManage); err != nil {
		return nil, err
	}
	if c.deps.AutoAdoptRetry == nil {
		return nil, c.storeError(nil, "自动发现未启用")
	}
	if err := c.deps.AutoAdoptRetry(ctx, req.AgentID, req.Name); err != nil {
		return nil, c.storeError(err, "未找到该项目的扫描记录")
	}
	secret(ctx)
	return &api.RetryAdoptRes{
		Message: "已清除该项目的自动发现结论，下一轮后台扫描（约 30 秒内）会重新读取配置",
	}, nil
}
