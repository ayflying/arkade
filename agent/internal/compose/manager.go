package compose

// manager.go 是编排能力的对外门面，把 runner / workspace / status 组装起来。
//
// 分层意图：handler 层只调用 Manager，不直接碰命令与文件。
// 这样"某个动作要跑几条命令、什么顺序"的知识集中在一处，
// 不会散落在各个 handler 里各写一遍（一处改了别处忘改）。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Manager 编排能力入口。
type Manager struct {
	ws     *Workspace
	runner *Runner
	// labelSource 用于在 Apply 前探测"这个名字是否已被外部项目占用"。
	// 为 nil 表示不做该检查（仅测试场景），生产路径一定注入。
	labelSource containerLabelSource
}

// SetLabelSource 注入引擎标签探测源。
//
// 为什么用 setter 而不是构造参数：Manager 在 cli 包里构造时引擎还没就绪
// （引擎探测本身可能失败，而编排能力要独立于它），
// 用 setter 可以让两者解耦，也避免改动 NewManager 的既有调用点。
func (m *Manager) SetLabelSource(src containerLabelSource) {
	m.labelSource = src
}

// NewManager 构造。dockerHost 会显式传给 compose 子进程（见 runner.go 的说明）。
func NewManager(dataDir, dockerHost string) *Manager {
	return &Manager{
		ws:     &Workspace{DataDir: dataDir},
		runner: &Runner{DockerHost: dockerHost},
	}
}

// Workspace 暴露工作区，供备份等子能力复用（避免再 new 一个导致路径规则分裂）。
func (m *Manager) Workspace() *Workspace { return m.ws }

// Apply 把编排内容落盘并启动（对应 compose.up）。
//
// 期望哈希的处理：Hub 下发的 ContentHash 用来校验"落盘的内容与 Hub 认为的一致"。
// 如果收到的内容与它自己声明的哈希对不上，说明传输或 Hub 侧计算出了问题。
// 此时**先落盘再报错**——保留文件便于排查（用户能直接看到实际落下去的是什么），
// 同时不让一份哈希错误的内容被静默接受并启动。
func (m *Manager) Apply(ctx context.Context, name, content, expectHash string) (Result, error) {
	n, err := cleanName(name)
	if err != nil {
		return Result{}, err
	}

	// **安全闸门：不许按同名接管外部项目。**
	//
	// 这是最后一道防线，也是本任务里最危险的一处。
	//
	// 攻击/误操作路径：外部生产项目（例如 act-runner）已在引擎里跑着，
	// 但 Arkade 工作区里没有它的目录。此时用户在界面上新建一个同名编排，
	// 内容随便写——`docker compose up -d` 会按项目名匹配到**已有的那些容器**，
	// 用新配置重建它们。也就是说：只要名字撞上，一次"新建"就能
	// 悄悄改掉一台机器上的生产负载，而界面上看起来只是"创建了一个新编排"。
	//
	// 仅靠列表只读是不够的：列表只读拦不住用户手工输入相同名字。
	// 因此判定必须落在真正执行 up 的这一层。
	//
	// 判定逻辑：工作区里没有这个项目（目录/文件不存在），但引擎里已有
	// 同名项目的容器 → 说明它与外部项目同名 → 拒绝。
	//
	// 为什么不用"引擎里有没有"单独判断：工作区里正常存在的项目在引擎里
	// 当然也有容器，那样会把自己管理的项目也拦掉。必须两个条件同时成立。
	if !m.ws.Exists(n) && m.engineHasProject(ctx, n) {
		return Result{}, fmt.Errorf(
			"机器上已存在同名的 compose 项目 %q，但它不是由 Arkade 创建的；"+
				"为避免改动该项目的现有容器，本次操作已拒绝。"+
				"请换一个编排名称，或先通过导入流程纳管（当前版本暂不支持导入）", n)
	}

	localHash := Hash(content)
	if expectHash != "" && expectHash != localHash {
		// 关键：仍然写盘。不写的话这条"哈希不符"的错误就无从排查，
		// 用户也看不到实际内容是什么样子。
		if _, werr := m.ws.Write(n, content); werr != nil {
			return Result{}, fmt.Errorf("内容哈希与 Hub 声明不一致（本机算得 %s，Hub 声明 %s），且保存文件失败: %w",
				localHash, expectHash, werr)
		}
		return Result{
			Output: fmt.Sprintf("内容哈希与 Hub 声明不一致（本机算得 %s，Hub 声明 %s），已保存文件但未启动",
				localHash, expectHash),
			ExitCode: -1,
		}, nil
	}

	if _, err := m.ws.Write(n, content); err != nil {
		return Result{}, err
	}
	return m.run(ctx, n, TimeoutLong, "up", "-d", "--remove-orphans"), nil
}

// engineHasProject 判断引擎里是否已有该名字的 compose 项目容器。
//
// 探测失败时**返回 true**（保守拒绝）：
// 我们无法确认这个名字是否安全，而在"可能改动生产负载"和
// "拒绝一次新建"之间，后者显然是更轻的代价。
// 用户会看到明确错误，可以重试；而生产容器被重建是不可逆的。
func (m *Manager) engineHasProject(ctx context.Context, name string) bool {
	if m.labelSource == nil {
		// 没有注入探测源（例如单元测试里的裸 Manager）。
		// 返回 false 以保持既有行为，避免让所有测试都要构造引擎。
		// 生产路径由 buildComposeManager 注入。
		return false
	}
	found, err := DiscoverExternal(ctx, m.labelSource)
	if err != nil {
		// 探测不了就不敢放行。
		return true
	}
	for _, p := range found {
		if p.Name == name {
			return true
		}
	}
	return false
}

// Down 停止并移除容器（保留数据卷，除非 removeVolumes）。
func (m *Manager) Down(ctx context.Context, name string, removeVolumes bool) (Result, error) {
	n, err := cleanName(name)
	if err != nil {
		return Result{}, err
	}
	if err := m.requireExists(n); err != nil {
		return Result{}, err
	}
	args := []string{"down"}
	if removeVolumes {
		args = append(args, "-v")
	}
	return m.run(ctx, n, TimeoutLong, args...), nil
}

// Stop 停止容器但不移除。
func (m *Manager) Stop(ctx context.Context, name string, services []string) (Result, error) {
	n, err := cleanName(name)
	if err != nil {
		return Result{}, err
	}
	if err := m.requireExists(n); err != nil {
		return Result{}, err
	}
	args := []string{"stop"}
	args = appendServices(args, services)
	return m.run(ctx, n, TimeoutLong, args...), nil
}

// Restart 重启容器。
func (m *Manager) Restart(ctx context.Context, name string, services []string) (Result, error) {
	n, err := cleanName(name)
	if err != nil {
		return Result{}, err
	}
	if err := m.requireExists(n); err != nil {
		return Result{}, err
	}
	args := []string{"restart"}
	args = appendServices(args, services)
	return m.run(ctx, n, TimeoutLong, args...), nil
}

// Pull 拉取镜像（对应界面上的"更新镜像"）。不改动运行中的容器。
func (m *Manager) Pull(ctx context.Context, name string) (Result, error) {
	n, err := cleanName(name)
	if err != nil {
		return Result{}, err
	}
	if err := m.requireExists(n); err != nil {
		return Result{}, err
	}
	return m.run(ctx, n, TimeoutLong, "pull"), nil
}

// Rebuild 强制重建：pull + up -d --force-recreate。
//
// 为什么要两步而不是直接 up：up 发现镜像没变就不会重建容器，
// 用户点"重建"期望的是"用最新镜像重来一遍"。先 pull 才能拿到新镜像。
//
// pull 失败时**仍然继续 up**：可能是某个私有镜像仓库暂时不可达，
// 但本地已有镜像，up 起来至少服务可用。把两次输出合并返回，
// 让用户看到"拉取失败但用本地镜像启动了"，而不是一个笼统的失败。
func (m *Manager) Rebuild(ctx context.Context, name string) (Result, error) {
	n, err := cleanName(name)
	if err != nil {
		return Result{}, err
	}
	if err := m.requireExists(n); err != nil {
		return Result{}, err
	}

	pull := m.run(ctx, n, TimeoutLong, "pull")
	up := m.run(ctx, n, TimeoutLong, "up", "-d", "--force-recreate", "--remove-orphans")

	combined := Result{
		Stdout: up.Stdout,
		// 以 up 的退出码为准：pull 失败但 up 成功，结果就是成功的。
		ExitCode: up.ExitCode,
		TimedOut: pull.TimedOut || up.TimedOut,
	}
	parts := make([]string, 0, 2)
	if pull.ExitCode != 0 {
		parts = append(parts, "【拉取镜像未成功】\n"+pull.Output)
	}
	parts = append(parts, "【启动】\n"+up.Output)
	combined.Output = truncate(strings.Join(parts, "\n\n"))
	return combined, nil
}

// Remove 删除编排：down -v，然后删掉本机目录。
//
// down 失败时的处理：仍然删除目录。理由——用户明确要删除，
// 而 down 失败最常见的原因是"容器本来就没跑"，此时留着目录毫无意义。
// 但如果 down 失败是因为"引擎连不上"，删目录就丢了唯一能排查的线索，
// 因此把 down 的输出一并返回给用户看。
func (m *Manager) Remove(ctx context.Context, name string) (Result, error) {
	n, err := cleanName(name)
	if err != nil {
		return Result{}, err
	}

	res := m.run(ctx, n, TimeoutLong, "down", "-v")
	if err := m.ws.Remove(n); err != nil {
		return res, err
	}
	if res.ExitCode != 0 {
		res.Output = truncate(res.Output + "\n\n（已删除本机编排文件，但上面的 down 未能正常完成）")
	}
	return res, nil
}

// Logs 拉取日志。
func (m *Manager) Logs(ctx context.Context, name, service string, tail int) (Result, error) {
	n, err := cleanName(name)
	if err != nil {
		return Result{}, err
	}
	if service != "" {
		if err := ValidateService(service); err != nil {
			return Result{}, err
		}
	}
	if err := m.requireExists(n); err != nil {
		return Result{}, err
	}
	if tail <= 0 {
		tail = defaultLogTail
	}
	if tail > maxLogTail {
		tail = maxLogTail
	}
	args := []string{"logs", "--no-color", "--tail", fmt.Sprintf("%d", tail)}
	if service != "" {
		args = append(args, service)
	}
	// logs 只读，用短超时即可。
	return m.run(ctx, n, TimeoutShort, args...), nil
}

// 日志行数限制：与容器日志接口保持一致，避免无界输出。
const (
	defaultLogTail = 200
	maxLogTail     = 2000
)

// PS 返回该编排下的容器状态（原始 JSON，由 handler 转成协议结构）。
func (m *Manager) PS(ctx context.Context, name string) (Result, error) {
	n, err := cleanName(name)
	if err != nil {
		return Result{}, err
	}
	if err := m.requireExists(n); err != nil {
		return Result{}, err
	}
	return m.run(ctx, n, TimeoutShort, "ps", "--format", "json"), nil
}

// ProjectStatus 是本机项目的完整状态（列表与详情共用）。
type ProjectStatus struct {
	Project
	Hash     string
	Status   string
	Running  int
	Total    int
	Services []string
}

// List 探测本机全部项目的实时状态。
//
// 性能说明：每个项目要跑一次 compose ps（约几十毫秒），项目多时会线性变慢。
// 这里不做并发：并发跑 compose 会在同一 Docker 引擎上互相争抢，
// 而 compose 项目数量在可预见规模内是个位数，串行完全够用且结果更稳定。
func (m *Manager) List(ctx context.Context) ([]ProjectStatus, error) {
	projects, err := m.ws.List()
	if err != nil {
		return nil, err
	}
	out := make([]ProjectStatus, 0, len(projects))
	for _, p := range projects {
		ps := ProjectStatus{Project: p}
		cfgPath := composePathIn(p.Dir)
		if h, herr := HashFile(cfgPath); herr == nil {
			ps.Hash = h
		}
		ps.Services = servicesFromConfig(cfgPath)
		m.fillStatus(ctx, p.Name, cfgPath, &ps)
		out = append(out, ps)
	}
	return out, nil
}

// Status 探测单个项目。
func (m *Manager) Status(ctx context.Context, name string) (ProjectStatus, error) {
	n, err := cleanName(name)
	if err != nil {
		return ProjectStatus{}, err
	}
	dir, err := m.ws.ProjectDir(n)
	if err != nil {
		return ProjectStatus{}, err
	}
	info, statErr := os.Stat(filepath.Join(dir, composeFileName))
	if statErr != nil {
		return ProjectStatus{}, fmt.Errorf("编排 %s 在本机不存在", n)
	}

	ps := ProjectStatus{Project: Project{
		Name: n, Dir: dir, UpdatedAt: info.ModTime().UTC(), HasComposeFile: true,
	}}
	cfgPath := composePathIn(dir)
	if h, herr := HashFile(cfgPath); herr == nil {
		ps.Hash = h
	}
	ps.Services = servicesFromConfig(cfgPath)
	m.fillStatus(ctx, n, cfgPath, &ps)
	return ps, nil
}

// fillStatus 跑一次 compose ps 并填充状态字段。
//
// List 与 Status 都需要这段逻辑，抽出来避免两处实现出现细微差异
// （例如一处把 ps 失败当作 stopped、另一处当作 unknown）。
func (m *Manager) fillStatus(ctx context.Context, name, cfgPath string, ps *ProjectStatus) {
	res := m.run(ctx, name, TimeoutShort, "ps", "--format", "json")
	entries := parsePSJson(res.Stdout)
	ps.Running, ps.Total = serviceStats(ps.Services, entries)
	if res.ExitCode != 0 && len(entries) == 0 {
		// ps 失败（没装 compose、引擎连不上、项目从没启动）时归为 unknown，
		// 而不是 stopped——stopped 会让用户以为服务挂了，实际是查不到。
		ps.Status = StatusUnknown
		return
	}
	ps.Status = aggregateStatus(ps.Running, ps.Total)
}

// requireExists 要求本机已有该项目的 compose.yml。
//
// 为什么要这一层：对不存在的项目跑 compose 命令，compose 会以
// "no such file" 之类的晦涩错误失败，用户难以判断是自己打错了项目名
// 还是编排真的不在。提前给一句中文提示更清楚。
func (m *Manager) requireExists(name string) error {
	if !m.ws.Exists(name) {
		return fmt.Errorf("编排 %s 在本机不存在，请先下发编排内容", name)
	}
	return nil
}

// run 在项目目录下执行命令。
//
// 工作目录设为项目目录：compose 默认从当前目录向上找 compose.yml，
// 显式设 Dir 能保证无论调用方在哪个目录，命令作用对象都正确。
func (m *Manager) run(ctx context.Context, name string, timeout time.Duration, args ...string) Result {
	dir, err := m.ws.ProjectDir(name)
	if err != nil {
		return Result{Output: err.Error(), ExitCode: -1}
	}
	r := *m.runner
	r.Dir = dir
	return r.Run(ctx, name, timeout, args...)
}

// appendServices 把服务名列表追加到参数尾部（空列表时不追加任何东西）。
func appendServices(args, services []string) []string {
	for _, s := range services {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		args = append(args, s)
	}
	return args
}
