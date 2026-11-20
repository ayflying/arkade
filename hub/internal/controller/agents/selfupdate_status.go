package agents

import (
	"context"
	"regexp"
	"strings"

	api "arkade/hub/api/agents/v1"
	"arkade/hub/internal/agentlink"
	"arkade/protocol/envelope"
)

var selfUpdateRequestPattern = regexp.MustCompile(`^(?:[0-9a-f]{32}|t[0-9]{1,20})$`)

func (c *Controller) SelfUpdateStatus(ctx context.Context, req *api.SelfUpdateStatusReq) (*api.SelfUpdateStatusRes, error) {
	if err := c.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if !selfUpdateRequestPattern.MatchString(req.RequestID) {
		return nil, badRequest("更新请求 ID 不合法")
	}
	ops := agentlink.NewOps(c.d.Registry)
	name := envelope.SelfUpdateHelperName(req.RequestID)
	out, cerr := ops.InspectContainer(ctx, req.AgentID, name)
	// 连接重建时查询可能暂不可用；没有可信终态，不推断失败。
	if cerr != nil || out.ID == "" || out.Name != name {
		return &api.SelfUpdateStatusRes{State: "pending"}, nil
	}
	switch out.State {
	case "running", "restarting", "paused":
		return &api.SelfUpdateStatusRes{State: "running"}, nil
	case "exited", "dead":
		if out.State == "exited" && out.ExitCode == 0 {
			return &api.SelfUpdateStatusRes{State: "succeeded"}, nil
		}
		// 旧助手日志也可能含敏感信息；仅用于分类，不转发原始日志。
		logs, _ := ops.Logs(ctx, req.AgentID, out.ID, 50)
		return &api.SelfUpdateStatusRes{State: "failed", Message: selfUpdateFailureMessage(logs.Lines)}, nil
	default:
		return &api.SelfUpdateStatusRes{State: "pending"}, nil
	}
}

func selfUpdateFailureMessage(lines []string) string {
	logs := strings.ToLower(strings.Join(lines, "\n"))
	switch {
	case strings.Contains(logs, ".env") && strings.Contains(logs, "permission denied"):
		return "更新助手无法读取部署目录的 .env，请检查运行权限；无需放宽文件的公开权限"
	case strings.Contains(logs, strings.ToLower(envelope.SelfUpdateManagedPrepareFailed)):
		return "更新助手无法准备纳管编排的临时配置，Hub 未完成更新"
	case strings.Contains(logs, strings.ToLower(envelope.SelfUpdateManagedComposeFailed)):
		return "Docker Compose 未能用纳管配置完成镜像拉取或容器重建"
	case strings.Contains(logs, "manifest unknown"), strings.Contains(logs, "pull access denied"):
		return "更新助手无法拉取声明的镜像，请检查仓库权限和镜像是否发布"
	default:
		return "更新助手异常退出，请在容器列表查看本次更新助手的日志"
	}
}
