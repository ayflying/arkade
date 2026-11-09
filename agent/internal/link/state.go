package link

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// agentIDFileName 是 Hub 下发的 agent_id 的持久化文件名。
//
// 与 internal/config 里的同名概念保持一致（那边用于读取，这边用于写入），
// 但 link 包不依赖 config 包：连接层只关心"把 ID 落到数据目录"，
// 由上层把 DataDir 传进来即可，避免包之间形成双向依赖。
const agentIDFileName = "agent_id"

// persistAgentID 原子写入 agent_id。
//
// 为什么用临时文件+重命名：Agent 会被 systemd 反复重启，直接覆盖写在进程被 kill 时
// 可能留下半截文件；下次启动读到损坏 ID 会让 Hub 认为这是一台新机器。
func persistAgentID(dataDir, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("agent_id 为空，拒绝写入")
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return fmt.Errorf("创建数据目录失败: %w", err)
	}
	final := filepath.Join(dataDir, agentIDFileName)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.TrimSpace(id)+"\n"), 0o640); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// hostname 返回主机名，失败时返回空串（不阻断上线）。
func hostname() (string, error) {
	return os.Hostname()
}

// setAtomicInt64 是 atomic.StoreInt64 的封装。
//
// 之所以要包一层：Go 1.19+ 官方推荐使用 atomic.Int64 类型，但会话结构里
// 已经有较多字段，为保持字段声明紧凑，这里统一用函数式封装，读写都走同一套封装。
func setAtomicInt64(p *int64, v int64) {
	atomic.StoreInt64(p, v)
}
