package audit

// record.go 定义审计记录的结构与写入器接口。
//
// 把这些放在 audit 包而不是 store 包：中间件与查询接口都依赖它们，
// 而 store 包已经承担了全部业务表，把审计的数据形状也塞进去
// 会让 store 变得过于臃肿。

import "time"

// Record 是一条待写入的审计记录。
//
// 冗余字段的设计说明（这是本表最重要的取舍）：
// username 与 agent_name 都是**快照**，不是外键。
// 用户或服务器被删除后，如果只存 ID，审计列表会变成一片空白或 NULL——
// 「谁删了什么」这个问题的答案恰恰在删除之后最需要，此时查不到就是废数据。
// 因此写入时把当时的名字一起存下来。代价是用户改名后历史记录仍显示旧名，
// 这是正确的：审计记录的是"当时发生了什么"，不是"现在叫什么"。
type Record struct {
	// ID 由应用层生成（见 NewID），不用数据库自增：
	// 审计需要跨库迁移与合并，自增 ID 会发生冲突。
	ID string

	UserID   string
	Username string

	AgentID   string
	AgentName string

	// Action 是 audit/actions.go 里登记的动作标识。
	Action string
	// Target 是目标标识：容器名、编排名、镜像 ID 等。
	Target string
	// Detail 是补充说明，例如编排子动作（restart/pull）或删除镜像的 tag。
	Detail string

	Result     string
	Error      string
	DurationMs int64

	CreatedAt time.Time
}

// Writer 是审计写入端。
//
// 定义成接口而不是直接依赖 *store.DB：中间件要能在写审计失败时
// 只打日志、不影响业务响应，而且测试需要一个"永远写失败"的实现
// 来验证这条规则。接口让这两件事都变得直接。
type Writer interface {
	// WriteAudit 写入一条审计记录。
	//
	// 调用方必须忽略返回的 error（只记日志）：审计是旁路功能，
	// 绝不能让"审计写不进去"变成"用户操作失败"。
	// 这个约束写在注释里，因为它是使用方最需要知道的事。
	WriteAudit(rec Record) error
}
