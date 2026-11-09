package store

// migrate.go 是数据库迁移执行器的 SQL 脚本部分。
//
// 设计原因（决策 D-03）：
//   - 迁移脚本按版本号顺序执行，每个脚本只跑一次，已执行版本记录在
//     schema_migrations 表里；
//   - 表结构语句里禁止出现 SQLite 专有语法（AUTOINCREMENT / INSERT OR REPLACE 等），
//     统一使用文本主键由应用层生成，便于后续切换 PostgreSQL；
//   - 迁移只增不改：已发布的脚本改内容会导致老库与新库结构不一致，且难以被发现。

// migration 是一组有序的 SQL 语句及其版本号。
type migration struct {
	version int
	name    string
	stmts   []string
}

// migrations 是全部迁移脚本。新增表结构时在末尾追加新版本，不要修改已有版本。
var migrations = []migration{
	{
		version: 1,
		name:    "初始化用户、用户组、Agent 与会话表",
		stmts: []string{
			// 用户表。id 用文本主键由应用层生成，避免依赖 SQLite 自增语义。
			`CREATE TABLE IF NOT EXISTS users (
				id            TEXT PRIMARY KEY,
				username      TEXT NOT NULL UNIQUE,
				display_name  TEXT NOT NULL,
				password_hash TEXT NOT NULL,
				role          TEXT NOT NULL,
				disabled      INTEGER NOT NULL DEFAULT 0,
				created_at    INTEGER NOT NULL,
				updated_at    INTEGER NOT NULL
			)`,
			// 用户组。组是权限授予的基本单位，绑定的服务器范围见 group_agents。
			`CREATE TABLE IF NOT EXISTS groups (
				id          TEXT PRIMARY KEY,
				name        TEXT NOT NULL UNIQUE,
				description TEXT NOT NULL DEFAULT '',
				created_at  INTEGER NOT NULL
			)`,
			// 用户与组的多对多关系：用户可加入多个组（决策 D-04）。
			`CREATE TABLE IF NOT EXISTS user_groups (
				user_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
				PRIMARY KEY (user_id, group_id)
			)`,
			// 组授权的服务器范围与权限档位（view/operate/manage）。
			`CREATE TABLE IF NOT EXISTS group_agents (
				group_id   TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
				agent_id   TEXT NOT NULL,
				permission TEXT NOT NULL,
				PRIMARY KEY (group_id, agent_id)
			)`,
			// Agent 表：记录注册身份与最近一次上报的状态。
			// labels 用 JSON 文本存储，读取时由应用层解析，不依赖数据库的 JSON 函数。
			`CREATE TABLE IF NOT EXISTS agents (
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
			)`,
			// 会话表：登录态。只存 token 的哈希，数据库被读走也无法直接冒用会话。
			`CREATE TABLE IF NOT EXISTS sessions (
				token_hash TEXT PRIMARY KEY,
				user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				created_at INTEGER NOT NULL,
				expires_at INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at)`,
		},
	},
}
