package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// agentIDFile 是 agent_id 的持久化文件名。
//
// 为什么单独存一个文件而不是回写 config.toml：配置文件是运维资产（可能由配置管理
// 工具分发、只读挂载），程序自动改写它会制造"配置漂移"和权限问题。运行期数据
// 一律落在 data_dir 下，与配置文件彻底分开。
const agentIDFile = "agent_id"

// ResolveAgentID 确定本机应使用的 Agent ID。
//
// 优先级：配置文件显式值 > data_dir 里持久化的值 > 空（等待 Hub 在 HelloAck 中下发）。
// 持久化值是"第一次连接时 Hub 分配"的结果，见 PersistAgentID。
func (c *Config) ResolveAgentID() (string, error) {
	if id := strings.TrimSpace(c.AgentID); id != "" {
		return id, nil
	}
	path := filepath.Join(c.DataDir, agentIDFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("读取已保存的 agent_id 失败(%s): %w", path, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// PersistAgentID 把 Hub 下发的 Agent ID 写入数据目录。
//
// 采用「写临时文件 + 重命名」的原子写法：Agent 可能被 systemd 频繁重启，
// 直接覆盖写在断电/被杀时可能留下半截文件，导致下次启动读到错误的 agent_id
// （Hub 侧会把同一个 Agent 识别成新机器）。重命名在同一文件系统上是原子的。
func (c *Config) PersistAgentID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("agent_id 为空，拒绝写入")
	}
	if err := os.MkdirAll(c.DataDir, 0o750); err != nil {
		return fmt.Errorf("创建数据目录失败(%s): %w", c.DataDir, err)
	}
	final := filepath.Join(c.DataDir, agentIDFile)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o640); err != nil {
		return fmt.Errorf("写入 agent_id 临时文件失败(%s): %w", tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		// 清理临时文件，避免数据目录里残留垃圾。
		_ = os.Remove(tmp)
		return fmt.Errorf("写入 agent_id 失败(%s): %w", final, err)
	}
	c.AgentID = id
	return nil
}

// RegistryFile 是"已纳管容器"的本地记录路径。
//
// 需要它的原因：协议里的 ContainerSummary.Origin 要求区分 arkade / adopted /
// discovered（01 架构 4.2），而运行时本身并不知道哪些容器是 Arkade 创建的。
// 首期用数据目录里的一个 JSON 文件做归属登记，等 Hub 侧的编排能力落地后
// 再由 Hub 下发覆盖，不在本阶段扩展成数据库。
func (c *Config) RegistryFile() string {
	return filepath.Join(c.DataDir, "containers.json")
}

// Registry 是归属登记表。
type Registry struct {
	// Containers 以容器 ID 为键，值为归属信息。
	Containers map[string]RegistryEntry `json:"containers"`
}

// RegistryEntry 记录单个容器的归属。
type RegistryEntry struct {
	Origin  string `json:"origin"`   // arkade / adopted / discovered
	StackID string `json:"stack_id"` // 所属编排 ID，可能为空
}

// LoadRegistry 读取归属登记表；文件不存在时返回空表而不是错误。
func (c *Config) LoadRegistry() (Registry, error) {
	reg := Registry{Containers: map[string]RegistryEntry{}}
	data, err := os.ReadFile(c.RegistryFile())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return reg, nil
		}
		return reg, fmt.Errorf("读取容器归属登记失败(%s): %w", c.RegistryFile(), err)
	}
	if err := json.Unmarshal(data, &reg); err != nil {
		return reg, fmt.Errorf("容器归属登记文件损坏(%s): %w", c.RegistryFile(), err)
	}
	if reg.Containers == nil {
		reg.Containers = map[string]RegistryEntry{}
	}
	return reg, nil
}
