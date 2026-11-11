package cli

// compose_setup.go 负责把编排能力装配进注册表。
//
// 单独成文件的原因：装配逻辑在两个入口（run 服务模式、本地 CLI 模式）
// 里都要用，且需要决定"docker 子进程该连哪个套接字"这个容易出错的判断。
// 把它写成一处，两个入口就不会各写一套而出现行为差异。

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"arkade/agent/internal/compose"
	"arkade/agent/internal/config"
	"arkade/agent/internal/imagetransfer"
	"arkade/agent/internal/runtime"
)

// buildComposeManager 构造编排管理器。
//
// 这里处理的是整套方案里最容易断的一环：**Agent 连引擎的路径与
// compose 子进程连引擎的路径是两条独立的路**。
//
//   - Agent 主程序通过 cfg.Socket（或自动发现的结果）连 Docker API；
//   - compose 是独立子进程，只认环境变量 DOCKER_HOST。
//
// 如果不把 Agent 实际连上的端点显式传给子进程，那么当用户配置了非默认
// 套接字（rootless Podman、tcp 端点、docker 装在不常见路径）时，
// Agent 自己工作正常，compose 却去连默认的 /var/run/docker.sock 然后失败——
// 表现是"Agent 明明能看容器，compose 却说连不上引擎"。
// 这种分裂症状极难排查，所以这里必须显式传递。
func buildComposeManager(cfg config.Config, rt runtime.Runtime) *compose.Manager {
	if !dataDirUsable(cfg.DataDir) {
		// 数据目录不可用时返回 nil，注册表会跳过编排方法，
		// Hub 侧得到 unsupported 而不是一堆写文件失败的错误。
		return nil
	}
	mgr := compose.NewManager(cfg.DataDir, dockerHostFor(rt))
	// 注入引擎探测源：Apply 执行 up 之前要确认"这个名字没被外部项目占用"，
	// 否则一次同名新建就能按项目名接管并重建别人的生产容器。
	// 见 compose/manager.go 里 Apply 开头那段说明。
	if src, ok := rt.(interface {
		ListContainerLabels(context.Context) ([]compose.ContainerLabels, error)
	}); ok {
		mgr.SetLabelSource(src)
	}
	return mgr
}

// buildImageTransferManager 构造镜像传输管理器。
//
// 与 buildComposeManager 的区别：**只依赖 data_dir 与运行时**，
// 不依赖 docker 子进程、也不依赖 compose 的任何配置。
// 因此这里的判断刻意独立：编排不可用（例如没有 compose 二进制）时，
// 镜像传输仍应可用——它只调用引擎 HTTP API。
//
// 返回 nil 表示"没有可用的传输临时区"，注册表会跳过传输方法，
// Hub 侧得到 unsupported，比"上传到一半才发现没地方写"清楚得多。
func buildImageTransferManager(cfg config.Config, rt runtime.Runtime) *imagetransfer.Manager {
	if !dataDirUsable(cfg.DataDir) {
		return nil
	}
	mgr, err := imagetransfer.NewManager(cfg.DataDir, rt, imagetransfer.DefaultLimits())
	if err != nil {
		// 构造失败（建目录/改权限失败）不阻断 Agent 启动：
		// 传输是可选能力，让它降级为 unsupported，而不是整个 Agent 起不来。
		return nil
	}
	return mgr
}

// imageTransferRoot 返回镜像与卷归档共用的临时区根目录（<data_dir>/transfers）。
//
// ============================ 为什么必须和 imagetransfer 用同一个目录 ============================
// 编排迁移的卷归档与镜像归档是同一类东西（可复用的大文件）、同一批生命周期
// 约束（TTL、数量上限、启动时清理无主残留）。各自新开一个目录会出现两套清理规则，
// 其中一套漏了就永久占盘；而且 transfers 的符号链接校验、越界检查这些
// 安全处理也已经在 imagetransfer 里做完了，重复实现只会让两边不一致。
//
// 与 buildImageTransferManager 的 data_dir 判断同源：镜像传输可用时它才存在。
func imageTransferRoot(cfg config.Config) string {
	if !dataDirUsable(cfg.DataDir) {
		return ""
	}
	return filepath.Join(cfg.DataDir, imagetransfer.TransfersDirName())
}

// buildTransferManagers 一次构造镜像传输管理器与迁移临时区。
//
// 单独成函数是为了保证两者**永不分裂**：它们同源于一个 data_dir 判断，
// 分开调用就可能出现"镜像能传、卷不能迁"的半可用状态，
// 而这种状态在界面上表现为"迁移跑到一半失败"，很难归因。
func buildTransferManagers(cfg config.Config, rt runtime.Runtime) (*imagetransfer.Manager, string) {
	mgr := buildImageTransferManager(cfg, rt)
	if mgr == nil {
		// 传输管理器不可用时临时区也一并视为不可用：
		// 卷归档没有 TTL 与清理规则的归属，落盘就是永久占用。
		return nil, ""
	}
	return mgr, imageTransferRoot(cfg)
}

// dockerHostFor 推导要传给 compose 子进程的 DOCKER_HOST。
//
// 返回空字符串表示"不覆盖"，让 compose 沿用当前进程环境或它自己的默认值。
//
// 取值优先级：
//  1. Agent 实际探测并连接成功的端点（最权威——这正是主程序在用的那条路）；
//  2. 环境变量 DOCKER_HOST（探测层也会读它，保持一致）；
//  3. 空（交给 compose 默认逻辑）。
func dockerHostFor(rt runtime.Runtime) string {
	if rt != nil && !runtime.IsNone(rt) {
		if info, err := rt.Info(context.Background()); err == nil {
			if ep := normalizeDockerHost(info.Endpoint); ep != "" {
				return ep
			}
		}
	}
	return normalizeDockerHost(os.Getenv("DOCKER_HOST"))
}

// normalizeDockerHost 校验端点形态，非 unix:// / tcp:// 前缀一律丢弃。
//
// 为什么丢弃而不是照抄：Info.Endpoint 在探测层里是「脱敏后的描述」，
// 将来若有人把它改成 "unix socket /var/run/docker.sock" 这类人类可读文本，
// 照抄进 DOCKER_HOST 会让 compose 直接崩掉。宁可退回默认值，
// 也不要传一个格式错误的值——错误的 DOCKER_HOST 比不设更糟。
func normalizeDockerHost(ep string) string {
	ep = strings.TrimSpace(ep)
	switch {
	case strings.HasPrefix(ep, "unix://"), strings.HasPrefix(ep, "tcp://"):
		return ep
	default:
		return ""
	}
}

// dataDirUsable 检查数据目录是否可用于写编排文件。
//
// 只做"能建目录 / 能写"这一件事的探测，不做更复杂的权限推理：
// 真正的写入失败会在具体操作时暴露，这里只需挡掉"路径明显不可用"的情况。
func dataDirUsable(dir string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return false
	}
	info, err := os.Stat(dir)
	if err != nil {
		// 目录不存在时尝试创建——Agent 首次启动时数据目录可能还没建。
		if os.IsNotExist(err) {
			return os.MkdirAll(dir, 0o755) == nil
		}
		return false
	}
	return info.IsDir()
}
