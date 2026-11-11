-- 迁移 v6：Agent 专属接入凭据与预创建标记
-- 本文件由旧 migrate_sql.go 自动导出，内容与之逐字一致。
-- 禁止修改：改内容会让老库与新库结构不一致，且难以被发现。

CREATE TABLE IF NOT EXISTS agent_tokens (
				token_hash TEXT NOT NULL PRIMARY KEY,
				agent_id TEXT NOT NULL,
				created_at INTEGER NOT NULL
			);

CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_tokens_agent
				ON agent_tokens (agent_id);

ALTER TABLE agents ADD COLUMN preprovisioned INTEGER NOT NULL DEFAULT 0;
