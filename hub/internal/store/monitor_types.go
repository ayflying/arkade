package store

// 监控载荷对仓储是不透明的；配置校验、加密和规则含义属于调用方。
// 以下按字节计的上限仅约束持久化载荷，不在仓储解析或重写 JSON。
const (
	MaxMonitorConfigBytes  = 1 << 20
	MaxMonitorSecretsBytes = 1 << 20
	MaxMonitorDetailBytes  = 64 << 10
	MaxMonitorErrorBytes   = 16 << 10
)

// MonitorConfig 是独立单行配置；密文不可直接作为公开 API 响应返回。
type MonitorConfig struct {
	ConfigJSON        string
	SecretsCiphertext string
	Version           int64
}

// MonitorIncident 按稳定 fingerprint 唯一，状态流转由单 worker 引擎负责。
type MonitorIncident struct {
	ID           string `json:"id"`
	Fingerprint  string `json:"fingerprint"`
	AgentID      string `json:"agent_id"`
	ContainerID  string `json:"container_id"`
	Kind         string `json:"kind"`
	Status       string `json:"status"`
	FirstSeen    int64  `json:"first_seen"`
	LastSeen     int64  `json:"last_seen"`
	LastNotified int64  `json:"last_notified"`
	ResolvedAt   int64  `json:"resolved_at"`
	DetailJSON   string `json:"detail_json"`
}

// MonitorDelivery 仅保存投递审计；Error 必须由调用方先脱敏。
type MonitorDelivery struct {
	ID         string `json:"id"`
	IncidentID string `json:"incident_id"`
	Channel    string `json:"channel"`
	Event      string `json:"event"`
	Status     string `json:"status"`
	Error      string `json:"error"`
	CreatedAt  int64  `json:"created_at"`
}
