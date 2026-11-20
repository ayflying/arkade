package store

// compose_projects.go 提供"这台服务器上 Arkade 接管了哪些编排项目"。
//
// 本文件存在的唯一原因是监控范围：监控只采集已接管的项目容器，因此 Hub 在
// 下发采集请求前需要知道每台 Agent 的项目名集合。
//
// 判定口径（与自动发现文档一致）：**成功接管就是 compose_stacks 里有一行**。
// 不要把 auto_adopt_states 当成接管证据——那张表只记录"别再重扫"的失败/跳过，
// 两处各记一份会让"谁才算已接管"出现两个互相矛盾的答案。

import (
	"context"

	"github.com/gogf/gf/v2/errors/gerror"

	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/entity"
)

// ListAdoptedComposeProjects 返回 agent_id → 项目名切片（已排序）。
// 多次调用之间不缓存：调用方每个采集周期只查一次，且读的是已接管清单，
// 缓存会让"刚接管的项目下一轮还没被监控"这种问题变成需要重启才自愈。
func (d *DB) ListAdoptedComposeProjects(ctx context.Context) (map[string][]string, error) {
	c := dao.ComposeStacks.Columns()
	var rows []entity.ComposeStacks
	if err := scanAll(ctx, d.gdb.Model(dao.ComposeStacks.Table()).
		Fields(c.AgentId, c.Name).OrderAsc(c.AgentId).OrderAsc(c.Name), &rows); err != nil {
		return nil, gerror.Wrap(err, "读取已接管编排项目失败")
	}
	out := make(map[string][]string)
	for _, e := range rows {
		if e.AgentId == "" || e.Name == "" {
			continue
		}
		out[e.AgentId] = append(out[e.AgentId], e.Name)
	}
	return out, nil
}
