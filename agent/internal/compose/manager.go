package compose

// manager.go 是编排能力的对外门面，把 runner / workspace / status 组装起来。
//
// 分层意图：handler 层只调用 Manager，不直接碰命令与文件。
// 这样"某个动作要跑几条命令、什么顺序"的知识集中在一处，
// 不会散落在各个 handler 里各写一遍（一处改了别处忘改）。

import (
	"context"
	"errors"
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
	// RootCtx 是 Agent 的生命周期上下文（SIGTERM 时取消）。
	//
	// 长任务（镜像更新）必须挂在它上面，而不是挂请求 ctx：
	// 请求在 RPC 返回时就结束，挂请求会导致任务刚启动就被取消。
	// 由 CLI 在构造后注入；nil 表示任务只随进程退出而结束。
	RootCtx context.Context
	// labelSource 用于在 Apply 前探测"这个名字是否已被外部项目占用"。
	// 为 nil 表示不做该检查（仅测试场景），生产路径一定注入。
	labelSource containerLabelSource
	// applier 是落盘前配置校验的实现，nil 表示走真实校验。
	//
	// 只有测试会覆盖它（构造本文件外的实例无法设置未导出字段）。
	// 生产代码不给它赋值，因此**生产路径永远真实校验**，
	// 不存在"配置里忘了打开校验"的静默降级。
	applier PrecheckFunc
	// locks 是按项目名的互斥锁，保证同一项目上的操作串行。
	//
	// 覆盖范围：Apply/Adopt（校验 + 落盘 + up 全程）、
	// Down/Stop/Restart/Pull/Rebuild/Remove，以及读侧的 List/Status/Preview。
	// 边界与理由见 lock.go——只保证**进程内**互斥，不保证跨进程。
	locks projectLocks
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

// ProjectSpec 是"一次编排下发"的完整输入。
//
// 把 compose.yml 与 .env 放在同一个结构体里，而不是给 Apply 加两个参数：
// 两者必须**一起**通过哈希校验、一起落盘，否则会出现
// "只用新 compose.yml 配旧 .env"启动的中间态——那是用户没有要求的状态，
// 且很可能让服务用错密码/端口启动。
type ProjectSpec struct {
	// Name 是项目名。
	Name string
	// Content 是 compose.yml 文本。
	Content string
	// EnvContent 是 .env 文本；空表示"清空该项目 .env"（删除文件）。
	EnvContent string
	// ExpectHash 是 Hub 计算的项目哈希；空表示不校验（旧调用方）。
	//
	// 校验用 HashProject(Content, EnvContent)，而不是 Hash(Content)：
	// 否则改了 .env 却不会触发"配置已偏离"，用户看到的一致性是假的。
	ExpectHash string
}

// ApplyProject 落盘 compose.yml 与 .env 并启动。
//
// 与 Apply 的唯一区别是它接受 .env。安全闸门、哈希校验、落盘顺序完全一致，
// 这样"有没有 .env"不会让两条路径产生不同的安全行为。
func (m *Manager) ApplyProject(ctx context.Context, spec ProjectSpec) (Result, error) {
	n, err := cleanName(spec.Name)
	if err != nil {
		return Result{}, err
	}

	// **全程持项目锁**：校验 → 落盘 → up。
	//
	// 为什么必须覆盖到 up 结束：up 是长命令（拉镜像可能要十分钟）。
	// 若在它执行期间另一个请求改了配置文件，compose 读到的可能是
	// 新旧混合的状态，而两个请求都会各自报告成功——
	// 用户看到"两次都成功"，却不知道最终跑的是哪一版配置。
	var res Result
	err = m.withProject(ctx, n, func() error {
		var aerr error
		res, aerr = m.applyLocked(ctx, n, spec)
		return aerr
	})
	return res, err
}

// applyLocked 是 ApplyProject 的加锁内实现（调用方必须已持有项目锁）。
func (m *Manager) applyLocked(ctx context.Context, n string, spec ProjectSpec) (Result, error) {
	// .env 的格式问题必须在写盘前拒绝：
	// 落一个 compose 解析不了的 .env，会让这个项目之后**所有**命令都失败
	// （包括 down/stop 这类本应能救场的命令），用户就被卡住了。
	if err := ValidateEnvContent(spec.EnvContent); err != nil {
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

	// 校验的是**项目哈希**（compose.yml + .env 联合）。
	// 只校验 content 的话，"只改了 .env"的下发会被当成一致而静默放行，
	// 用户以为新变量生效了，实际用的还是旧 .env。
	localHash := HashProject(spec.Content, spec.EnvContent)
	if spec.ExpectHash != "" && spec.ExpectHash != localHash {
		// **哈希不符时绝不写盘。**
		//
		// 旧实现选择"先落盘再报错以便排查"，那是错的，而且危险：
		// 落盘会覆盖**正在运行的项目**的配置，于是下一次 stop/restart
		// 用的就是这份"我们刚刚判定为可疑"的内容——
		// 用户看到的是"下发失败"，实际却已经被改掉了。
		// 排查所需的信息（两个哈希值）在错误信息里已经完全够用，
		// 不需要用户去看磁盘上的文件。
		return Result{}, fmt.Errorf(
			"内容哈希与 Hub 声明不一致（本机算得 %s，Hub 声明 %s），已拒绝写入，现有配置未被改动",
			localHash, spec.ExpectHash)
	}

	// **写盘前先让 compose 自己校验一遍。**
	//
	// 若把解析不了的 compose.yml 写进项目目录，这个项目之后所有命令都会失败
	// （包括 down/stop——用户连"停掉它"都做不到，只能登机器手工删文件）。
	// 校验在临时目录里完成，正式文件一个字节都没动。
	pc := m.checkConfig(ctx, n, spec.Content, spec.EnvContent)
	if !pc.OK {
		return Result{}, errors.New(pc.Reason)
	}

	// 两个文件一起提交：任一失败都不会留下"新编排 + 旧 env"的半写状态。
	if _, _, err := m.ws.CommitProject(n, spec.Content, spec.EnvContent); err != nil {
		return Result{}, err
	}
	return m.run(ctx, n, TimeoutLong, "up", "-d", "--remove-orphans"), nil
}

// projectHash 计算某个已落盘项目的**联合哈希**（compose.yml + .env）。
//
// 为什么不直接用 HashFile(compose.yml)：
// 用户只改 .env 时（换镜像 tag、改密码、改端口），若哈希只看 compose.yml，
// 界面会一直显示"配置一致"，而实际运行的东西已经变了。
// 这类"界面说没变、实际变了"的偏差最难排查，因为没有任何报错。
//
// **读侧必须持项目锁**：提交是两个 rename（compose.yml 再 .env）。
// 若在这两步之间读，会拿到「新 compose.yml + 旧 .env」——
// 一个从未被校验过的组合，它的哈希既不等于旧值也不等于新值，
// 界面上就显示成"配置已偏离"，把用户引向完全错误的方向。
func (m *Manager) projectHash(name, cfgPath string) string {
	var h string
	_ = m.withProject(context.Background(), name, func() error {
		h = m.projectHashLocked(name, cfgPath)
		return nil
	})
	return h
}

// projectHashLocked 是 projectHash 的加锁内实现（调用方必须已持有项目锁）。
func (m *Manager) projectHashLocked(name, cfgPath string) string {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return ""
	}
	env, _ := m.ws.ReadEnv(name)
	return HashProject(string(data), env)
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

// lockedRun 是"持项目锁执行一条命令"的通用包装。
//
// 六种动作（down/stop/restart/pull/rebuild/remove）加锁的理由相同：
// 它们都读取项目目录里的配置文件，并且会改变引擎中容器的状态。
// 若不与 Apply 互斥，就会出现"up 正在按 A 版配置重建容器，
// 同时 stop 按 B 版配置停止服务"，两者都报告成功，而结果没人说得清。
func (m *Manager) lockedRun(ctx context.Context, n string, fn func() (Result, error)) (Result, error) {
	var res Result
	err := m.withProject(ctx, n, func() error {
		var rerr error
		res, rerr = fn()
		return rerr
	})
	return res, err
}

// Down 停止并移除容器（保留数据卷，除非 removeVolumes）。
func (m *Manager) Down(ctx context.Context, name string, removeVolumes bool) (Result, error) {
	n, err := cleanName(name)
	if err != nil {
		return Result{}, err
	}
	return m.lockedRun(ctx, n, func() (Result, error) {
		if err := m.requireExists(n); err != nil {
			return Result{}, err
		}
		args := []string{"down"}
		if removeVolumes {
			args = append(args, "-v")
		}
		return m.run(ctx, n, TimeoutLong, args...), nil
	})
}

// Stop 停止容器但不移除。
func (m *Manager) Stop(ctx context.Context, name string, services []string) (Result, error) {
	n, err := cleanName(name)
	if err != nil {
		return Result{}, err
	}
	return m.lockedRun(ctx, n, func() (Result, error) {
		if err := m.requireExists(n); err != nil {
			return Result{}, err
		}
		args := []string{"stop"}
		args = appendServices(args, services)
		return m.run(ctx, n, TimeoutLong, args...), nil
	})
}

// Restart 重启容器。
func (m *Manager) Restart(ctx context.Context, name string, services []string) (Result, error) {
	n, err := cleanName(name)
	if err != nil {
		return Result{}, err
	}
	return m.lockedRun(ctx, n, func() (Result, error) {
		if err := m.requireExists(n); err != nil {
			return Result{}, err
		}
		args := []string{"restart"}
		args = appendServices(args, services)
		return m.run(ctx, n, TimeoutLong, args...), nil
	})
}

// Pull 拉取镜像（对应界面上的"更新镜像"）。不改动运行中的容器。
func (m *Manager) Pull(ctx context.Context, name string) (Result, error) {
	n, err := cleanName(name)
	if err != nil {
		return Result{}, err
	}
	return m.lockedRun(ctx, n, func() (Result, error) {
		if err := m.requireExists(n); err != nil {
			return Result{}, err
		}
		return m.run(ctx, n, TimeoutLong, "pull"), nil
	})
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
	return m.lockedRun(ctx, n, func() (Result, error) {
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
	})
}

// RemoveWithVolumes 删除编排项目，是否连带删除数据卷由调用方显式决定。
//
// ============================ 为什么必须显式 ============================
// 删数据卷是不可逆的：卷里是最难重建的那部分数据（数据库文件、上传件）。
// 把它作为默认行为意味着"点一下删除编排"就永久丢数据，
// 而这个后果在 UI 上只是一行普通操作。因此这里要求调用方明确表态。
//
// ============================ 失败必须保留配置 ============================
// down 失败时**不删除**本机编排文件：
//   - 目录里的 compose.yml 是用户恢复服务的唯一依据；
//   - down 失败多为"引擎连不上"这类可恢复故障，
//     此时删掉配置等于把可恢复故障变成不可恢复；
//   - 异步任务会因此置为 failed，而 failed 的任务本意是"可以重试"——
//     若配置已被删除，重试就变成"项目不存在"，用户彻底失去入口。
func (m *Manager) RemoveWithVolumes(ctx context.Context, name string, removeVolumes bool) (Result, error) {
	n, err := cleanName(name)
	if err != nil {
		return Result{}, err
	}

	return m.lockedRun(ctx, n, func() (Result, error) {
		args := []string{"down"}
		if removeVolumes {
			args = append(args, "-v")
		}
		res := m.run(ctx, n, TimeoutLong, args...)

		if res.ExitCode != 0 {
			// 安全语义：失败即保留，让用户能重试或自行排查。
			res.Output = truncate(res.Output + "\n\n（编排文件已保留，可修复问题后重试删除）")
			return res, nil
		}

		if err := m.ws.Remove(n); err != nil {
			return res, err
		}
		if res.ExitCode != 0 {
			res.Output = truncate(res.Output + "\n\n（已删除本机编排文件，但上面的 down 未能正常完成）")
		}
		return res, nil
	})
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
//
// 并发说明：**逐个项目非阻塞试锁，拿不到就标 busy**。
//
// 为什么不用阻塞等锁：某个项目正在 up（可能十分钟），
// 列表若排队等它，整个 ComposeList 会一直卡到 15 秒超时，
// 界面表现成"列表加载失败"——而其它项目其实完全正常。
// 因此这里用 tryWithProject：拿不到锁就立刻返回 busy，
// 让用户在列表里看到"这个项目正在操作中"，而不是整张列表挂掉。
// 每次都只持有一把项目信号量（用完即放），不存在多锁嵌套，也就没有死锁。
func (m *Manager) List(ctx context.Context) ([]ProjectStatus, error) {
	projects, err := m.ws.List()
	if err != nil {
		return nil, err
	}
	out := make([]ProjectStatus, 0, len(projects))
	for _, p := range projects {
		var ps ProjectStatus
		got := m.tryWithProject(p.Name, func() {
			ps = ProjectStatus{Project: p}
			cfgPath := composePathIn(p.Dir)
			ps.Hash = m.projectHashLocked(p.Name, cfgPath)
			ps.Services = servicesFromConfig(cfgPath)
			m.fillStatus(ctx, p.Name, cfgPath, &ps)
		})
		if !got {
			// 该项目正在执行写操作：**不读**它的文件（可能正处在两次 rename 之间），
			// 也不跑 compose ps（会和正在运行的命令抢引擎）。
			// 如实告诉用户"正在操作中"，不给一个可能过时的状态。
			ps = ProjectStatus{Project: p}
			ps.Status = StatusBusy
			out = append(out, ps)
			continue
		}
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

	var ps ProjectStatus
	err = m.withProject(ctx, n, func() error {
		dir, derr := m.ws.ProjectDir(n)
		if derr != nil {
			return derr
		}
		info, statErr := os.Stat(filepath.Join(dir, composeFileName))
		if statErr != nil {
			return fmt.Errorf("编排 %s 在本机不存在", n)
		}
		ps = ProjectStatus{Project: Project{
			Name: n, Dir: dir, UpdatedAt: info.ModTime().UTC(), HasComposeFile: true,
		}}
		cfgPath := composePathIn(dir)
		ps.Hash = m.projectHashLocked(n, cfgPath)
		ps.Services = servicesFromConfig(cfgPath)
		m.fillStatus(ctx, n, cfgPath, &ps)
		return nil
	})
	if err != nil {
		return ProjectStatus{}, err
	}
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

// runnerFor 构造"针对某个项目"的 Runner（含 --env-file 与需剔除的键）。
//
// 抽成独立方法有两个原因：
//   - 便于测试**直接断言**传给 compose 的参数与环境，而不必真的执行 docker
//     （真执行会让测试依赖宿主机是否有 compose，甚至可能真的启停容器）；
//   - 让"每次命令都从磁盘重读 .env"这条规则只有一个实现点。
//
// 每次从磁盘重读而不是缓存：.env 可能被备份恢复、被用户手工编辑、
// 被上一次 Apply 替换过。缓存会造成"文件已变但命令还用旧插值"，
// 那是最难查的一类偏差。
func (m *Manager) runnerFor(name string) *Runner {
	r := *m.runner
	r.ProjectName = name

	if p, err := m.ws.EnvPath(name); err == nil {
		if _, statErr := os.Stat(p); statErr == nil {
			r.EnvFile = p
			if content, rerr := m.ws.ReadEnv(name); rerr == nil {
				r.EnvExcludeKeys = ParseEnvKeys(content)
			}
		}
	}
	return &r
}

// run 在项目目录下执行命令。
//
// 工作目录设为项目目录：compose 默认从当前目录向上找 compose.yml，
// 显式设 Dir 能保证无论调用方在哪个目录，命令作用对象都正确。
//
// 有 .env 时显式传 --env-file，并把其中定义的键从继承环境剔除。
// 剔除是必须的：compose 的插值优先级是 shell 环境 > .env，
// 不剔除的话 .env 里的值会被 Agent 进程的同名变量压住，
// 用户改 .env 却"没有任何效果"，且看不到任何报错。
func (m *Manager) run(ctx context.Context, name string, timeout time.Duration, args ...string) Result {
	dir, err := m.ws.ProjectDir(name)
	if err != nil {
		return Result{Output: err.Error(), ExitCode: -1}
	}
	r := m.runnerFor(name)
	r.Dir = dir
	return r.Run(ctx, name, timeout, args...)
}

// runStream 与 run 完全一样，只是把输出逐行推给 sink（用于异步任务的流式输出）。
func (m *Manager) runStream(ctx context.Context, name string, timeout time.Duration, sink StreamSink, args ...string) Result {
	dir, err := m.ws.ProjectDir(name)
	if err != nil {
		return Result{Output: err.Error(), ExitCode: -1}
	}
	r := m.runnerFor(name)
	r.Dir = dir
	return r.RunStream(ctx, name, timeout, sink, args...)
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
