package store

// agents_preprovision.go 实现「预创建 Agent 服务器记录」。
//
// 用户需求：在服务器列表里"添加服务器"，Hub 预建一条记录并下发一把
// 专属接入密钥；那台机器上的 Agent 用这把密钥连上来，就绑定到这条记录。
//
// 这与旧流程（先跑 Agent、Hub 自动建记录）互补而非替代：
// 旧记录由共享 token 照常接入，新记录必须持有专属密钥。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/errors/gerror"

	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/do"
)

// StatusPending 表示记录已创建但从未有 Agent 连上来过。
//
// 为什么不复用 offline：offline 的含义是"曾经连上、现在断开"，
// 用户看到 offline 会去查为什么掉线（查网络、查进程）；
// 而 pending 的正确处置是"还没部署 Agent"。
// 把两者混成一个状态，用户会误判成故障机器。
const StatusPending = "pending"

// ErrAgentNameTaken 表示已存在同名（规范化后）的服务器记录。
var ErrAgentNameTaken = errors.New("同名服务器已存在")

// NewAgentID 生成预创建记录的 ID。
//
// 用随机串而不是自增：预创建发生在管理员操作时，
// 而真正的 Agent 可能几天后才连上来，随机 ID 不依赖任何顺序或计数器，
// 也不会与旧流程按主机名生成的 ID 撞号。
func NewAgentID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", gerror.Wrap(err, "生成随机 ID 失败")
	}
	return "agent-" + hex.EncodeToString(b), nil
}

// CreatePreprovisionedAgent 在事务内创建预建记录并写入凭据摘要。
//
// **必须在一个事务里完成两件事**（建记录 + 写凭据）：
// 若分开做，"记录已建但凭据没写"会留下一条既没有密钥、
// 又不是旧流程记录的孤儿行——它既不能被专属密钥接入，
// 又因为 preprovisioned=1 而拒绝共享 token，变成谁也连不上的死记录。
//
// 同名检查也在事务内：先查后插在同一事务里，才能挡住并发创建。
// TxModel 的 PostgreSQL transaction advisory lock 保护先查后插；
// 不给 agents.name 加全局唯一约束：自动发现的主机名可能重复，
// 而手动预登记要求归一化名称不重复。约束因此只加在
// “本次预登记不得产生新重名”这一业务入口。
func (d *DB) CreatePreprovisionedAgent(ctx context.Context, rec AgentRecord, tokenHash string, at time.Time) error {
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		return createPreprovisionedAgentTx(ctx, tx, rec, tokenHash, at)
	})
}

// createPreprovisionedAgentTx 复用同一条原子预建路径，调用方必须已持有 TxModel 事务锁。
func createPreprovisionedAgentTx(ctx context.Context, tx gdb.TX, rec AgentRecord, tokenHash string, at time.Time) error {
	labelsRaw, err := json.Marshal(rec.Labels)
	if err != nil {
		return gerror.Wrap(err, "序列化 Agent 标签失败")
	}
	// 事务内判重（规范化后比较，见 normalizAgentName）。
	dup, err := agentNameExistsTx(ctx, tx, rec.Name, "")
	if err != nil {
		return err
	}
	if dup {
		return ErrAgentNameTaken
	}

	if _, err := tx.Model(dao.Agents.Table()).Ctx(ctx).Data(do.Agents{
		Id:             rec.ID,
		Name:           rec.Name,
		DisplayName:    rec.DisplayName,
		Note:           rec.Note,
		Status:         StatusPending,
		Version:        "",
		Runtime:        "",
		RuntimeVersion: "",
		LastSeen:       0,
		Labels:         string(labelsRaw),
		CreatedAt:      rec.CreatedAt,
		UpdatedAt:      rec.UpdatedAt,
		Disabled:       0,
		Preprovisioned: 1,
	}).Insert(); err != nil {
		return gerror.Wrap(err, "写入预建服务器失败")
	}

	if _, err := tx.Model(dao.AgentTokens.Table()).Ctx(ctx).Data(do.AgentTokens{
		TokenHash: tokenHash,
		AgentId:   rec.ID,
		CreatedAt: at.Unix(),
	}).Insert(); err != nil {
		return gerror.Wrap(err, "写入接入凭据失败")
	}
	return nil
}

// ResetAgentTokenHash 替换某条记录的凭据摘要。
//
// 语义要求"旧的立即失效"：所以先删后插，并依赖 agent_id 上的唯一索引
// 保证同时只有一个摘要。
// 记录不存在时返回 ErrNotFound（调用方据此 404）。
func (d *DB) ResetAgentTokenHash(ctx context.Context, agentID, tokenHash string, at time.Time) error {
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		exists, err := tx.Model(dao.Agents.Table()).Ctx(ctx).
			Where(dao.Agents.Columns().Id, agentID).
			Count()
		if err != nil {
			return gerror.Wrap(err, "查询服务器失败")
		}
		if exists == 0 {
			return ErrNotFound
		}

		// 先删该 Agent 的所有旧摘要，再插入新的。
		// 不用 UPDATE 是因为也可能出现"该 Agent 还没有凭据行"的情况
		// （例如预建时写凭据失败后人工补建），先删后插两种都覆盖。
		if _, err := tx.Model(dao.AgentTokens.Table()).Ctx(ctx).
			Where(dao.AgentTokens.Columns().AgentId, agentID).
			Delete(); err != nil {
			return gerror.Wrap(err, "删除旧凭据失败")
		}
		if _, err := tx.Model(dao.AgentTokens.Table()).Ctx(ctx).Data(do.AgentTokens{
			TokenHash: tokenHash,
			AgentId:   agentID,
			CreatedAt: at.Unix(),
		}).Insert(); err != nil {
			return gerror.Wrap(err, "写入新凭据失败")
		}
		return nil
	})
}

// agentNameExistsTx 在事务内判断是否已有同名记录。
//
// excludeID 用于"改自己的名字"时把自己排除掉。
//
// 比较用规范化后的结果：历史数据里可能有大小写差异或首尾空格，
// 那条记录在界面上看起来就叫这个名字，用户再次输入同名时
// 应该在 UI 层就被拦住，而不是等到两条几乎一样的记录并排出现才发现。
//
// ============================ 为什么这里仍需全表比对 ============================
// 归一化（去空白 + 折叠空白 + 小写）是在 Go 里做的，SQL 的
// LOWER/TRIM 不能完全复现"折叠连续空白"，所以不能把条件下推到 SQL。
// 记录数是"服务器数量"级别（几十到几百），全表读一遍可接受。
func agentNameExistsTx(ctx context.Context, tx gdb.TX, name, excludeID string) (bool, error) {
	want := normalizAgentName(name)
	if want == "" {
		return false, nil
	}

	m := tx.Model(dao.Agents.Table()).Ctx(ctx).
		Fields(dao.Agents.Columns().Id, dao.Agents.Columns().Name)
	if excludeID != "" {
		m = m.WhereNot(dao.Agents.Columns().Id, excludeID)
	}
	recs, err := m.All()
	if err != nil {
		return false, gerror.Wrap(err, "查询同名服务器失败")
	}

	for _, rec := range recs {
		if normalizAgentName(rec[dao.Agents.Columns().Name].String()) == want {
			return true, nil
		}
	}
	return false, nil
}

// normalizAgentName 归一化名称用于比较（不影响存储值）。
//
// 只做去空白 + 折叠连续空白 + 小写。故意不做更激进的归一
// （比如去掉所有标点）：那会让「prod-1」和「prod_1」被视为同名，
// 而它们在 Compose 项目名语境下是完全不同的东西。
func normalizAgentName(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}
