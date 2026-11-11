-- 迁移 v5：操作审计日志表
-- 本文件由旧 migrate_sql.go 自动导出，内容与之逐字一致。
-- 禁止修改：改内容会让老库与新库结构不一致，且难以被发现。

CREATE TABLE IF NOT EXISTS audit_logs (
				id TEXT NOT NULL PRIMARY KEY,
				user_id TEXT NOT NULL,
				username TEXT NOT NULL,
				agent_id TEXT NOT NULL DEFAULT '',
				agent_name TEXT NOT NULL DEFAULT '',
				action TEXT NOT NULL,
				target TEXT NOT NULL DEFAULT '',
				detail TEXT NOT NULL DEFAULT '',
				result TEXT NOT NULL,
				error TEXT NOT NULL DEFAULT '',
				duration_ms INTEGER NOT NULL DEFAULT 0,
				created_at INTEGER NOT NULL
			);

CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs (created_at DESC);

CREATE INDEX IF NOT EXISTS idx_audit_user ON audit_logs (user_id);

CREATE INDEX IF NOT EXISTS idx_audit_agent ON audit_logs (agent_id);

CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_logs (action);
