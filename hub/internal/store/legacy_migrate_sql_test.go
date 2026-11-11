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
	{
		version: 4,
		name:    "Agent 表补记显示名、备注与禁用状态",
		stmts: []string{
			// display_name 是用户可编辑的显示名，空串表示"未设置"。
			//
			// 为什么不复用 name：name 是 Agent 首次上线时自报的主机名，
			// 属于机器属性，用户改不了；而显示名是"人给这台服务器起的名字"
			// （如「生产-东京」）。两者来源不同，混在一列会导致
			// Agent 重连上报主机名时把用户改的名字覆盖掉。
			// 空串时由应用层回落到 name，界面上不会出现空白行。
			`ALTER TABLE agents ADD COLUMN display_name TEXT NOT NULL DEFAULT ''`,
			// note 是自由备注（如「生产环境-东京机房，负责商品服务」）。
			// 单独一列而不是塞进 labels：labels 是结构化的键值对，用于筛选与
			// 权限匹配；note 是给人读的自由文本，两者语义不同，
			// 放进 labels 会让"按 label 筛选"的结果里混入无法预期的自由文本。
			`ALTER TABLE agents ADD COLUMN note TEXT NOT NULL DEFAULT ''`,
			// disabled 表示该 Agent 被管理员禁用：连接请求在握手阶段就会被
			// 拒绝并给出明确原因。
			//
			// 为什么用禁用而不是直接删除记录：删除会连同纳管关系一起消失，
			// 而禁用保留全部历史（谁在被禁之前连过、有哪些编排挂在它上面）。
			// 运维场景里"先禁用观察"远比"直接删除"可控。
			`ALTER TABLE agents ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0`,
		},
	},
	{
		version: 5,
		name:    "操作审计日志表",
		stmts: []string{
			// id 用应用层生成的随机串（见 audit.NewID），不用自增：
			// 审计数据需要能跨库合并/导入，自增 ID 会撞号。
			//
			// 注意 TEXT PRIMARY KEY 在 SQLite 里**不蕴含 NOT NULL**
			// （只有 INTEGER PRIMARY KEY 才是 rowid 的别名并隐含非空），
			// 所以这里必须显式写 NOT NULL，否则能插进一条 NULL 主键的记录。
			`CREATE TABLE IF NOT EXISTS audit_logs (
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
			)`,
			// username / agent_name 是写入当时的名字快照，不是外键。
			// 用户或服务器被删掉之后，「谁删了什么」的答案恰恰最被需要，
			// 此时若靠 JOIN 取名字，审计列表会变成一片空白——那就成了废数据。
			//
			// 主查询是"按时间倒序列出全部"，因此 created_at 降序索引最关键。
			`CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs (created_at DESC)`,
			// 以下三个索引对应筛选条件：谁做的、哪台服务器、什么动作。
			`CREATE INDEX IF NOT EXISTS idx_audit_user ON audit_logs (user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_audit_agent ON audit_logs (agent_id)`,
			`CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_logs (action)`,
		},
	},
	{
		version: 6,
		name:    "Agent 专属接入凭据与预创建标记",
		stmts: []string{
			// agent_tokens 保存「服务器专属接入密钥」的摘要。
			//
			// **绝不存明文。** 明文只在创建/重置时的响应里出现一次，
			// 之后 Hub 只握有它的 SHA256。这样即使整个数据库被拖走，
			// 攻击者也拿不到可直接用的密钥。
			//
			// token_hash 做主键而不是自增 id：一个 Agent 在同一时刻
			// 只有一把有效密钥（重置即删旧行），主键语义正好表达这一点，
			// 也顺手阻止了"同一摘要被插入两次"。
			`CREATE TABLE IF NOT EXISTS agent_tokens (
				token_hash TEXT NOT NULL PRIMARY KEY,
				agent_id TEXT NOT NULL,
				created_at INTEGER NOT NULL
			)`,
			// 反向查询（按 agent_id 找摘要）用于重置时删旧行。
			// 建 UNIQUE 而不是普通索引：一个 Agent 不应同时存在两把密钥，
			// 否则"重置后旧密钥失效"就无法保证——旧行漏删会让旧密钥继续可用，
			// 那是一个安全漏洞而不是小瑕疵。
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_tokens_agent
				ON agent_tokens (agent_id)`,
			// preconditioned：预创建记录标记。
			//
			// 1 = 由管理员在界面预建（必须先持有专属密钥才能接入）；
			// 0 = 老版本自动注册建立的记录（共享 token 仍可照常接入）。
			//
			// 为什么需要这一列而不是"看有没有凭据行"：
			// 凭据行会被重置/删除，而"这是预建记录"这个身份不该随之改变。
			// 例如重置密钥的瞬间旧行已删、新行未写，若靠凭据行判断，
			// 那条记录会短暂退化成"可被共享 token 认领"的状态——竞态窗口虽小，
			// 但正好落在最敏感的时刻。
			`ALTER TABLE agents ADD COLUMN preprovisioned INTEGER NOT NULL DEFAULT 0`,
		},
	},
	{
		version: 7,
		name:    "编排项目保存环境文件",
		stmts: []string{
			`ALTER TABLE compose_stacks ADD COLUMN env_content TEXT NOT NULL DEFAULT ''`,
		},
	},
	{
		version: 8,
		name:    "外部编排独立草稿",
		stmts: []string{
			`CREATE TABLE external_compose_drafts (
				agent_id TEXT NOT NULL, name TEXT NOT NULL, content TEXT NOT NULL,
				source_hash TEXT NOT NULL, version INTEGER NOT NULL, updated_at BIGINT NOT NULL,
				PRIMARY KEY (agent_id, name))`,
		},
	},
	{version: 9, name: "服务器直连地址", stmts: []string{
		`ALTER TABLE agents ADD COLUMN direct_address TEXT NOT NULL DEFAULT ''`,
	}},
	{version: 10, name: "编排异步操作幂等元数据", stmts: []string{
		// 不复制 compose/env 等秘密配置；只绑定原表的身份、版本和摘要。
		// 不加 stack 外键：remove 后仍须保留幂等请求，防止重试误删同名新项目。
		`CREATE TABLE compose_operation_requests (
			agent_id TEXT NOT NULL, request_id TEXT NOT NULL,
			name TEXT NOT NULL, action TEXT NOT NULL, fingerprint TEXT NOT NULL,
			task_id TEXT NOT NULL DEFAULT '', stack_id TEXT NOT NULL,
			stack_version BIGINT NOT NULL, config_hash TEXT NOT NULL,
			created_at BIGINT NOT NULL, finalized INTEGER NOT NULL DEFAULT 0,
			final_error TEXT NOT NULL DEFAULT '', record_removed INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (agent_id, request_id))`,
		`CREATE INDEX idx_compose_operations_task ON compose_operation_requests(agent_id, name, task_id)`,
		`CREATE INDEX idx_compose_operations_expiry ON compose_operation_requests(finalized, created_at)`,
	}},
}
