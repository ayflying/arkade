-- 迁移 v2：新增容器编排表
-- 本文件由旧 migrate_sql.go 自动导出，内容与之逐字一致。
-- 禁止修改：改内容会让老库与新库结构不一致，且难以被发现。

CREATE TABLE IF NOT EXISTS compose_stacks (
				id          TEXT NOT NULL PRIMARY KEY,
				agent_id    TEXT NOT NULL,
				name        TEXT NOT NULL,
				content     TEXT NOT NULL,
				config_hash TEXT NOT NULL,
				version     INTEGER NOT NULL DEFAULT 1,
				created_at  INTEGER NOT NULL,
				updated_at  INTEGER NOT NULL
			);

CREATE UNIQUE INDEX IF NOT EXISTS idx_compose_stacks_agent_name ON compose_stacks(agent_id, name);

CREATE INDEX IF NOT EXISTS idx_compose_stacks_agent ON compose_stacks(agent_id);
