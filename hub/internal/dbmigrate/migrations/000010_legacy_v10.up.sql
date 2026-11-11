-- 迁移 v10：编排异步操作幂等元数据
-- 本文件由旧 migrate_sql.go 自动导出，内容与之逐字一致。
-- 禁止修改：改内容会让老库与新库结构不一致，且难以被发现。

CREATE TABLE compose_operation_requests (
			agent_id TEXT NOT NULL, request_id TEXT NOT NULL,
			name TEXT NOT NULL, action TEXT NOT NULL, fingerprint TEXT NOT NULL,
			task_id TEXT NOT NULL DEFAULT '', stack_id TEXT NOT NULL,
			stack_version BIGINT NOT NULL, config_hash TEXT NOT NULL,
			created_at BIGINT NOT NULL, finalized INTEGER NOT NULL DEFAULT 0,
			final_error TEXT NOT NULL DEFAULT '', record_removed INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (agent_id, request_id));

CREATE INDEX idx_compose_operations_task ON compose_operation_requests(agent_id, name, task_id);

CREATE INDEX idx_compose_operations_expiry ON compose_operation_requests(finalized, created_at);
