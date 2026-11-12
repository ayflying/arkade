package store

import (
	"context"
	"time"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/errors/gerror"

	"arkade/hub/internal/dao"
)

const (
	LocalAgentID   = "agent-local"
	LocalAgentName = "本机"
)

// EnsureLocalAgent 仅在首次配置时预建同机服务器；摘要由调用方按专属密钥规则计算。
// 固定 ID、预建标记与相同凭据共同确认既有绑定，不能仅凭名称认领其他服务器。
// 重启只读检查，绝不恢复名称、启用状态或旧密钥。环境变量轮换与网页重置无法
// 安全区分，所以一律报错，由管理员显式协调部署凭据，不自动覆盖数据库。
func (d *DB) EnsureLocalAgent(ctx context.Context, tokenHash string) error {
	if tokenHash == "" {
		return gerror.New("同机 Agent 凭据摘要不能为空")
	}
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		var (
			ac = dao.Agents.Columns()
			tc = dao.AgentTokens.Columns()
		)
		rec, err := tx.Model(dao.Agents.Table()).Ctx(ctx).Where(ac.Id, LocalAgentID).One()
		if err != nil {
			return gerror.Wrap(err, "查询同机 Agent 失败")
		}
		if !rec.IsEmpty() {
			// 不要求原始名称：网页重命名是管理员的持久决定。
			if rec[ac.Preprovisioned].Int() != 1 {
				return gerror.New("同机 Agent 固定 ID 已被非预建记录占用，拒绝接管")
			}
			token, err := tx.Model(dao.AgentTokens.Table()).Ctx(ctx).Where(tc.AgentId, LocalAgentID).One()
			if err != nil {
				return gerror.Wrap(err, "查询同机 Agent 凭据失败")
			}
			if token.IsEmpty() || token[tc.TokenHash].String() != tokenHash {
				return gerror.New("同机 Agent 凭据不一致或已撤销：拒绝自动轮换，请协调 ARKADE_LOCAL_AGENT_TOKEN 与网页重置后的专属密钥，或移除此配置")
			}
			return nil
		}
		// 凭据也可能已属于其他服务器（甚至是孤儿行），禁止迁移或抢占。
		n, err := tx.Model(dao.AgentTokens.Table()).Ctx(ctx).
			Where(tc.TokenHash, tokenHash).WhereOr(tc.AgentId, LocalAgentID).Count()
		if err != nil {
			return gerror.Wrap(err, "检查同机 Agent 凭据冲突失败")
		}
		if n != 0 {
			return gerror.New("同机 Agent 凭据或固定 ID 已被占用，拒绝接管")
		}
		now := time.Now().UTC()
		return createPreprovisionedAgentTx(ctx, tx, AgentRecord{
			ID: LocalAgentID, Name: LocalAgentName, DisplayName: LocalAgentName,
			Labels: map[string]string{}, CreatedAt: now.Unix(), UpdatedAt: now.Unix(),
		}, tokenHash, now)
	})
}
