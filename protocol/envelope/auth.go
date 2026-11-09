package envelope

// auth.go 明确 Hub 与 Agent 之间的认证头约定。
//
// 为什么写在协议模块而不是 Hub 侧：认证格式是两端契约，
// 放在一处才不会出现 Agent 发一种、Hub 收另一种的错配。

// Header 是认证与身份相关的 HTTP 请求头名称。
//
// 安全要求：凭据**只允许走请求头**。查询串（?token=）会被反向代理、
// 访问日志与浏览器历史记录留存，属于凭据泄漏的高危路径，Hub 侧一律忽略。
const (
	HeaderAuthorization = "Authorization"     // Bearer <token>
	HeaderAgentID       = "X-Arkade-Agent-Id" // Agent 在重连时上报自己已知的 ID，供 Hub 识别并复用
	HeaderProtoVersion  = "X-Arkade-Proto"    // Agent 支持的协议版本，便于在建立连接前快速拒绝
)

// AgentConnectPath 是 Agent 反向连接的固定路径。
const AgentConnectPath = "/agent/connect"

// BearerPrefix 是 Authorization 头的固定前缀。
const BearerPrefix = "Bearer "
