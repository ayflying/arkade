package store

import (
	"context"
	"strings"

	"github.com/gogf/gf/v2/errors/gerror"
)

// 删除对象（容器 / 编排 / 服务器）时的监控数据清理。
//
// ## 为什么要求"不留残留"
// 监控数据分三张表：样本曲线（monitor_samples）、告警事件（monitor_incidents）、
// 通知投递记录（monitor_deliveries）。对象被删除后，这三张表里针对它的行既没有
// 页面能看到，又会一直占用保留期，属于纯粹的垃圾数据。
//
// 迁移 000002 的注释原本写着"删除服务器必须保留监控历史"，那是指告警记录作为
// 运维凭据的价值。2026-11-20 按用户要求改为一起清理：既然对象都没了，指向它的
// 告警也就没有追溯对象了。迁移文件本身不能再改（已应用的迁移校验和会失配），
// 以这里的实现为准。
//
// ## 为什么不用"从快照里消失"来推断删除
// 采集范围是按已接管项目收窄的：编排被删时容器从快照里消失是"范围变化"造成的，
// 与"容器真的被删掉"无法区分。所以三个入口都由删除动作显式调用。

// PurgeMonitorAgentData 清理一台服务器的全部监控数据（删除服务器时调用）。
func (d *DB) PurgeMonitorAgentData(ctx context.Context, agentID string) error {
	if agentID == "" {
		return nil
	}
	const scope = `agent_id = $1`
	if err := d.purgeMonitorIncidents(ctx, scope, agentID); err != nil {
		return err
	}
	return d.purgeMonitorSamples(ctx, `DELETE FROM monitor_samples WHERE `+scope, agentID)
}

// PurgeMonitorProjectData 清理某个编排项目的全部监控数据（删除编排时调用）。
//
// 样本表有 project 列，编排名就是 Compose 项目名，可以直接精确匹配。
// 告警事件表没有项目列，只能借样本表反查该项目的容器 ID——所以必须先删告警
// 再删样本，顺序反了就再也找不到这些容器了。
func (d *DB) PurgeMonitorProjectData(ctx context.Context, agentID, project string) error {
	if agentID == "" || project == "" {
		return nil
	}
	const incidentScope = `agent_id = $1 AND container_id IN (
    SELECT DISTINCT container_id FROM monitor_samples WHERE agent_id = $1 AND project = $2)`
	if err := d.purgeMonitorIncidents(ctx, incidentScope, agentID, project); err != nil {
		return err
	}
	return d.purgeMonitorSamples(ctx, `DELETE FROM monitor_samples WHERE agent_id = $1 AND project = $2`, agentID, project)
}

// PurgeMonitorContainerData 清理单个容器的全部监控数据（删除容器时调用）。
//
// 删除接口收到的引用可能是完整 ID、12 位短 ID，也可能是容器名（Agent 会原样
// 回显请求里的引用），所以样本按三种形态都匹配一次：容器名在宿主机内唯一，
// 短 ID 前缀也只会命中同一个容器。告警事件表没有名称列，只能按 ID 清，
// 名字命中的告警会随保留期到期消失。
func (d *DB) PurgeMonitorContainerData(ctx context.Context, agentID, containerRef string) error {
	ref := strings.TrimSpace(containerRef)
	if agentID == "" || ref == "" {
		return nil
	}
	pattern := escapeLikePattern(ref)
	const incidentScope = `agent_id = $1 AND (container_id = $2 OR container_id LIKE $3 || '%')`
	if err := d.purgeMonitorIncidents(ctx, incidentScope, agentID, ref, pattern); err != nil {
		return err
	}
	const sampleScope = `agent_id = $1
  AND (container_id = $2 OR container_id LIKE $3 || '%' OR container_name = $2)`
	return d.purgeMonitorSamples(ctx, `DELETE FROM monitor_samples WHERE `+sampleScope, agentID, ref, pattern)
}

// purgeMonitorIncidents 删除符合 incidentWhere 的告警事件及其投递记录。
//
// incidentWhere 只由本文件的常量拼接而成，不含外部输入，占位符用 $1..$n。
// 先删投递再删事件：monitor_deliveries.incident_id 没有外键约束，
// 反过来的话会留下一批指向已不存在事件的记录。
func (d *DB) purgeMonitorIncidents(ctx context.Context, incidentWhere string, args ...any) error {
	deliveries := `DELETE FROM monitor_deliveries WHERE incident_id IN (SELECT id FROM monitor_incidents WHERE ` + incidentWhere + `)`
	if _, err := d.gdb.DoExec(ctx, nil, deliveries, args...); err != nil {
		return gerror.Wrap(err, "清理通知投递记录失败")
	}
	if _, err := d.gdb.DoExec(ctx, nil, `DELETE FROM monitor_incidents WHERE `+incidentWhere, args...); err != nil {
		return gerror.Wrap(err, "清理告警事件失败")
	}
	return nil
}

func (d *DB) purgeMonitorSamples(ctx context.Context, query string, args ...any) error {
	if _, err := d.gdb.DoExec(ctx, nil, query, args...); err != nil {
		return gerror.Wrap(err, "清理容器资源历史失败")
	}
	return nil
}

// escapeLikePattern 转义 LIKE 通配符。容器名里理论上可以出现 _ 或 %，
// 不转义会让一次删除变成"删掉一批"。
func escapeLikePattern(v string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(v)
}
