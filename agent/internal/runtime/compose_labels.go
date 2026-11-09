package runtime

// compose_labels.go 把"引擎里所有容器的 compose 标签"暴露给上层，
// 供发现外部编排项目使用。
//
// 为什么不直接在 compose 包里发 HTTP：compose 包不应知道 Docker API 的
// 路径与协议细节，那是 runtime 包的职责。这里做一层窄适配，
// 也让 compose 包可以在测试里用假实现替换。

import (
	"context"

	"arkade/agent/internal/compose"
)

// ListContainerLabels 列出所有容器（含已停止）的标签。
//
// 用 ?all=1 而不是只看运行中的：**已停止的项目也必须被发现**。
// 用户反馈的是"编排列表是空的"，如果只列运行中的，
// 一个刚刚停掉的项目就会从列表里消失——那会让用户以为配置丢了。
func (e *engine) ListContainerLabels(ctx context.Context) ([]compose.ContainerLabels, error) {
	containers, err := e.List(ctx, true)
	if err != nil {
		return nil, err
	}
	out := make([]compose.ContainerLabels, 0, len(containers))
	for _, c := range containers {
		out = append(out, compose.ContainerLabels{
			State:  c.State,
			Labels: c.Labels,
		})
	}
	return out, nil
}
