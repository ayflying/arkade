package store

// agent_tokens.go 管理「服务器专属接入密钥」的摘要（以及未接入期间的密文）。
//
// 安全模型（这是本文件存在的全部理由）：
//
//	认证只依赖 SHA256 摘要：Hub 用摘要反查身份，从不靠明文比对。
//	明文另存一份**加密后**的副本，且只在记录仍处于 pending（从未接入）时存在；
//	第一次握手成功立即抹掉。之后即使整库连同应用代码被拖走，
//	攻击者也拿不到能直接用的密钥——解密还需要独立的主密钥文件。
//
// 为什么不always只存摘要：用户在「添加服务器」后往往过一阵才部署 Agent，
// 期间需要反复查看那把密钥和编排示例。只给一次就得靠"重置"才能再看，
// 而重置会让已经开始连接的 Agent 掉线，语义上是错的。
// 代价被限制在"未接入"这段窗口内，且不是明文。
//
// 与旧的"全局共享 token"的关系：
//   - 共享 token 明文在配置文件里（或环境变量 AGENT_TOKEN），所有机器共用一把；
//   - 专属 token 一机一把，按摘要精确解析出它绑定的 agent_id。
// 两者并存，但**共享 token 不允许认领已经拥有专属凭据的记录**，
// 否则"专属"就形同虚设（见 verifyAgentToken 的说明）。

import (
	"context"
	"errors"
	"time"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/errors/gerror"

	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/do"
)

// ErrAgentTokenNotFound 表示该摘要没有对应的凭据。
var ErrAgentTokenNotFound = errors.New("接入凭据不存在")

// AgentToken 是凭据的元信息。
//
// 注意：**没有任何表示明文的字段**。这不是遗漏——
// 结构体里没有地方放明文，就不可能在代码里不小心把它传出去。
type AgentToken struct {
	// TokenHash 是 SHA256 的十六进制串。
	TokenHash string
	AgentID   string
	CreatedAt time.Time
}

// SetAgentTokenCipher 写入「尚未接入」期间的密钥密文。
//
// 只该在创建/重置密钥时调用：这些时刻明文刚生成，用户很可能稍后还要
// 再抄一次编排示例。第一次握手成功后必须调用 ClearAgentTokenCipher 抹掉。
func (d *DB) SetAgentTokenCipher(ctx context.Context, agentID, cipher string) error {
	if agentID == "" {
		return errors.New("agent_id 不能为空")
	}
	c := dao.AgentTokens.Columns()
	res, err := d.gdb.Model(dao.AgentTokens.Table()).Ctx(ctx).
		Data(do.AgentTokens{TokenCipher: cipher}).
		Where(c.AgentId, agentID).
		Update()
	if err != nil {
		return gerror.Wrap(err, "写入密钥密文失败")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 凭据行不存在意味着这把密钥根本无法用于认证，
		// 静默成功会让调用方以为"还能再查看"，进而误导用户。
		return ErrNotFound
	}
	return nil
}

// AgentTokenCipher 读取密钥密文。空字符串表示已无可查看的明文。
//
// 查不到凭据行返回 ErrAgentTokenNotFound，让调用方能区分
// "记录没有专属凭据"与"数据库出错"。
func (d *DB) AgentTokenCipher(ctx context.Context, agentID string) (string, error) {
	if agentID == "" {
		return "", ErrAgentTokenNotFound
	}
	c := dao.AgentTokens.Columns()
	one, err := d.gdb.Model(dao.AgentTokens.Table()).Ctx(ctx).
		Fields(c.TokenCipher).
		Where(c.AgentId, agentID).
		One()
	if err != nil {
		return "", gerror.Wrap(err, "查询密钥密文失败")
	}
	if one.IsEmpty() {
		return "", ErrAgentTokenNotFound
	}
	return one[c.TokenCipher].String(), nil
}

// ClearAgentTokenCipher 抹掉密钥密文。
//
// 调用时机是"这台服务器第一次成功握手"——从这一刻起身份已经绑定，
// 再保留可解密的明文就只是纯粹的风险。没有密文时是空操作。
func (d *DB) ClearAgentTokenCipher(ctx context.Context, agentID string) error {
	if agentID == "" {
		return nil
	}
	if _, err := d.gdb.Model(dao.AgentTokens.Table()).Ctx(ctx).
		Data(do.AgentTokens{TokenCipher: ""}).
		Where(dao.AgentTokens.Columns().AgentId, agentID).
		Update(); err != nil {
		return gerror.Wrap(err, "清除密钥密文失败")
	}
	return nil
}

// SaveAgentToken 写入或替换某个 Agent 的凭据摘要。
//
// 语义是"替换"而不是"新增"：一个 Agent 在同一时刻只能有一把有效密钥。
// 实现上用先删后插，因为 agent_id 上的唯一索引决定了旧行必须先消失，
// 否则插入会撞唯一约束。
//
// 整个操作在一个事务里：先删后插若只成功一半，
// 会留下"该 Agent 没有任何凭据"的状态——那样它就退化成可被
// 共享 token 认领的记录，是一次权限降级。
func (d *DB) SaveAgentToken(ctx context.Context, tokenHash, agentID string, at time.Time) error {
	if tokenHash == "" || agentID == "" {
		return errors.New("凭据摘要与 agent_id 均不能为空")
	}

	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		c := dao.AgentTokens.Columns()

		// 先删该 Agent 的旧凭据（重置语义）。
		if _, err := tx.Model(dao.AgentTokens.Table()).Ctx(ctx).
			Where(c.AgentId, agentID).
			Delete(); err != nil {
			return gerror.Wrap(err, "删除旧凭据失败")
		}
		// 再删可能存在的同摘要行（防御性：若同一摘要因某种原因绑到了
		// 别的 Agent，留着会造成歧义）。
		if _, err := tx.Model(dao.AgentTokens.Table()).Ctx(ctx).
			Where(c.TokenHash, tokenHash).
			Delete(); err != nil {
			return gerror.Wrap(err, "清理冲突凭据失败")
		}
		if _, err := tx.Model(dao.AgentTokens.Table()).Ctx(ctx).Data(do.AgentTokens{
			TokenHash: tokenHash,
			AgentId:   agentID,
			CreatedAt: at.Unix(),
		}).Insert(); err != nil {
			return gerror.Wrap(err, "写入凭据失败")
		}
		return nil
	})
}

// LookupAgentByTokenHash 按摘要解析出它绑定的 agent_id。
//
// 查不到返回 ErrAgentTokenNotFound——调用方据此区分
// "不是专属密钥"（可能是共享 token）与"数据库出错"。
// 这个区分很重要：前者走共享 token 的旧路径，后者必须 fail closed。
func (d *DB) LookupAgentByTokenHash(ctx context.Context, tokenHash string) (string, error) {
	if tokenHash == "" {
		return "", ErrAgentTokenNotFound
	}
	c := dao.AgentTokens.Columns()
	one, err := d.gdb.Model(dao.AgentTokens.Table()).Ctx(ctx).
		Fields(c.AgentId).
		Where(c.TokenHash, tokenHash).
		One()
	if err != nil {
		return "", gerror.Wrap(err, "查询凭据失败")
	}
	if one.IsEmpty() {
		return "", ErrAgentTokenNotFound
	}
	return one[c.AgentId].String(), nil
}

// DeleteAgentToken 删除某个 Agent 的凭据（重置与删除记录时调用）。
//
// 删完立即生效：下一次握手按摘要查不到，旧密钥即刻作废。
// 不做"软删除保留一段时间"——那样旧密钥在保留期内仍然可用，
// 与"重置立即失效"的承诺不符。
func (d *DB) DeleteAgentToken(ctx context.Context, agentID string) error {
	if agentID == "" {
		return nil
	}
	if _, err := d.gdb.Model(dao.AgentTokens.Table()).Ctx(ctx).
		Where(dao.AgentTokens.Columns().AgentId, agentID).
		Delete(); err != nil {
		return gerror.Wrap(err, "删除凭据失败")
	}
	return nil
}

// HasAgentToken 判断某个 Agent 是否已配置专属凭据。
//
// 用于「共享 token 不得认领有专属凭据的记录」这条规则。
// 查询失败时返回 error 而不是 false：false 会让调用方放行，
// 那正是我们想避免的权限降级，所以必须让调用方 fail closed。
func (d *DB) HasAgentToken(ctx context.Context, agentID string) (bool, error) {
	if agentID == "" {
		return false, nil
	}
	n, err := d.gdb.Model(dao.AgentTokens.Table()).Ctx(ctx).
		Where(dao.AgentTokens.Columns().AgentId, agentID).
		Count()
	if err != nil {
		return false, gerror.Wrap(err, "查询凭据存在性失败")
	}
	return n > 0, nil
}
