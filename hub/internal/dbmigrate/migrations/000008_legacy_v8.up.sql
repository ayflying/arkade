-- 迁移 v8：外部编排独立草稿
-- 本文件由旧 migrate_sql.go 自动导出，内容与之逐字一致。
-- 禁止修改：改内容会让老库与新库结构不一致，且难以被发现。

CREATE TABLE external_compose_drafts (
				agent_id TEXT NOT NULL, name TEXT NOT NULL, content TEXT NOT NULL,
				source_hash TEXT NOT NULL, version INTEGER NOT NULL, updated_at BIGINT NOT NULL,
				PRIMARY KEY (agent_id, name));
