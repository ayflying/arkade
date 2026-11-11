-- 迁移 v1：初始化用户、用户组、Agent 与会话表
-- 本文件由旧 migrate_sql.go 自动导出，内容与之逐字一致。
-- 禁止修改：改内容会让老库与新库结构不一致，且难以被发现。

CREATE TABLE IF NOT EXISTS users (
				id            TEXT PRIMARY KEY,
				username      TEXT NOT NULL UNIQUE,
				display_name  TEXT NOT NULL,
				password_hash TEXT NOT NULL,
				role          TEXT NOT NULL,
				disabled      INTEGER NOT NULL DEFAULT 0,
				created_at    INTEGER NOT NULL,
				updated_at    INTEGER NOT NULL
			);

CREATE TABLE IF NOT EXISTS groups (
				id          TEXT PRIMARY KEY,
				name        TEXT NOT NULL UNIQUE,
				description TEXT NOT NULL DEFAULT '',
				created_at  INTEGER NOT NULL
			);

CREATE TABLE IF NOT EXISTS user_groups (
				user_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
				PRIMARY KEY (user_id, group_id)
			);

CREATE TABLE IF NOT EXISTS group_agents (
				group_id   TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
				agent_id   TEXT NOT NULL,
				permission TEXT NOT NULL,
				PRIMARY KEY (group_id, agent_id)
			);

CREATE TABLE IF NOT EXISTS agents (
				id              TEXT PRIMARY KEY,
				name            TEXT NOT NULL,
				status          TEXT NOT NULL,
				version         TEXT NOT NULL DEFAULT '',
				runtime         TEXT NOT NULL DEFAULT 'none',
				runtime_version TEXT NOT NULL DEFAULT '',
				last_seen       INTEGER NOT NULL DEFAULT 0,
				labels          TEXT NOT NULL DEFAULT '{}',
				created_at      INTEGER NOT NULL,
				updated_at      INTEGER NOT NULL
			);

CREATE TABLE IF NOT EXISTS sessions (
				token_hash TEXT PRIMARY KEY,
				user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				created_at INTEGER NOT NULL,
				expires_at INTEGER NOT NULL
			);

CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);

CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);
