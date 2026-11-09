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
	{
		version: 2,
		name:    "新增容器编排表",
		stmts: []string{
			// 编排项目表：Hub 是权威副本，Agent 本机另存一份 compose.yml。
			//
			// 为什么两边都存：Agent 必须能在 Hub 不可达时独立运行编排（离线自治），
			// 因此本机文件不能依赖 Hub；而 Hub 必须能展示/编辑编排内容，不能假设
			// 某台 Agent 此刻在线。两边各存一份是这套架构下的必然结果，
			// 一致性靠"下发时带哈希 + 上报时带哈希"比对，而不是靠数据库同步。
			//
			// 关于 id 显式写 NOT NULL：SQLite 有个与标准 SQL 不同的历史行为——
			// 列声明为 TEXT PRIMARY KEY 时并不会自动加上 NOT NULL（只有 INTEGER
			// PRIMARY KEY 会）。不写 NOT NULL 就允许插入 id 为 NULL 的行，
			// 主键约束形同虚设。这里显式声明，schema 测试也在守着这一点。
			`CREATE TABLE IF NOT EXISTS compose_stacks (
				id          TEXT NOT NULL PRIMARY KEY,
				agent_id    TEXT NOT NULL,
				name        TEXT NOT NULL,
				content     TEXT NOT NULL,
				config_hash TEXT NOT NULL,
				version     INTEGER NOT NULL DEFAULT 1,
				created_at  INTEGER NOT NULL,
				updated_at  INTEGER NOT NULL
			)`,
			// 同一台 Agent 上项目名唯一：项目名就是 Agent 本机的目录名，
			// 重名会导致两个编排互相覆盖对方的文件。
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_compose_stacks_agent_name ON compose_stacks(agent_id, name)`,
			// 按 Agent 列编排是最常见的查询（列表页）。
			`CREATE INDEX IF NOT EXISTS idx_compose_stacks_agent ON compose_stacks(agent_id)`,
		},
	},
	{
		version: 3,
		name:    "编排表补记最后成功下发时间",
		stmts: []string{
			// synced_at 记录最近一次成功下发到 Agent 的时间（Unix 秒，0 表示从未成功下发）。
			//
			// 为什么需要单独一列：updated_at 是"用户在 Hub 改内容"的时间，
			// 而 synced_at 是"内容真的落到某台机器上"的时间，两者不同。
			// Agent 离线时用户照样能改内容并保存，此时 updated_at 会变而
			// synced_at 不变——界面据此提示"已保存但尚未下发"，用户才不会
			// 误以为线上已经生效。缺了这列就只能靠哈希猜，而哈希在
			// "改完又改回去"的场景下会给出错误结论。
			//
			// 用 ALTER TABLE ADD COLUMN 而不是重建表：SQLite 支持该语法，
			// 且 NOT NULL 列必须带默认值，否则已有行无法填充。
			`ALTER TABLE compose_stacks ADD COLUMN synced_at INTEGER NOT NULL DEFAULT 0`,
		},
	},
}
