package audit

// id.go 生成审计记录 ID。

import (
	"crypto/rand"
	"encoding/hex"
)

// IDBytes 是随机 ID 的字节数。
const IDBytes = 16

// NewID 生成一条审计记录的 ID。
//
// 用随机值而不是自增或时间戳：
//   - 自增：跨库合并/导入历史数据时会撞号；
//   - 纯时间戳：同一毫秒内的并发写入会重复，而审计恰恰是"几个管理员
//     同时操作"的场景，撞号后主键冲突会让后一条记录被静默丢弃。
//
// 随机 128 位碰撞概率可忽略，且天然可排序性不影响审计——
// 列表按 created_at 排序，不看 ID。
func NewID() string {
	b := make([]byte, IDBytes)
	// rand.Read 在现代 Go 上不会失败（内部已用 ChaCha8 兜底），
	// 但仍保留错误分支：万一失败，宁可返回一个明显异常的 ID，
	// 也不要让审计整体挂掉。
	if _, err := rand.Read(b); err != nil {
		return "audit-rand-error"
	}
	return hex.EncodeToString(b)
}
