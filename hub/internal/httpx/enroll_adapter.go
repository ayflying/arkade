package httpx

// enroll_adapter.go 把 store 的凭据操作适配成 agentlink 需要的能力。
//
// 为什么需要这层适配而不是让 agentlink 直接依赖 store：
//
//  1. agentlink 是"连接层"，不该知道凭据存在哪张表、怎么迁移；
//  2. 两个包的哨兵错误不同（store.ErrAgentTokenNotFound vs
//     agentlink.ErrTokenNotEnrolled），必须在一处显式翻译。
//     若让调用方各自 errors.Is 两个包，将来改一个哨兵就会漏改一处，
//     而漏改的表现是"认证突然对所有人失败"或"失败被当成放行"。
//
// 这个适配器的错误映射方向是安全关键的：
// **"不存在" → 可回落；其他所有错误 → 向上抛（fail closed）。**

import (
	"context"
	"errors"
	"time"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/store"
)

// credentialQueryTimeout 是单次凭据查询的预算。
//
// 远小于握手 10 秒预算：数据库卡顿时应该尽快失败并给出明确原因，
// 而不是让 Agent 侧一直看到"连不上"。
const credentialQueryTimeout = 3 * time.Second

// NewCredentialStore 构造凭据适配器。
func NewCredentialStore(db *store.DB) agentlink.CredentialStore {
	return &credentialStoreAdapter{db: db}
}

type credentialStoreAdapter struct {
	db *store.DB
}

// LookupAgentByTokenHash 把摘要解析成 agent_id。
//
// store.ErrAgentTokenNotFound → agentlink.ErrTokenNotEnrolled：
// 这是"这把密钥不是专属密钥"，可以回落到共享 token 路径，不是故障。
// 其他错误原样上传，调用方会 fail closed。
func (a *credentialStoreAdapter) LookupAgentByTokenHash(hash string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), credentialQueryTimeout)
	defer cancel()
	id, err := a.db.LookupAgentByTokenHash(ctx, hash)
	if err != nil {
		if errors.Is(err, store.ErrAgentTokenNotFound) {
			return "", agentlink.ErrTokenNotEnrolled
		}
		return "", err
	}
	return id, nil
}

// HasAgentToken 判断某条记录是否已有专属凭据。
func (a *credentialStoreAdapter) HasAgentToken(agentID string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), credentialQueryTimeout)
	defer cancel()
	return a.db.HasAgentToken(ctx, agentID)
}
