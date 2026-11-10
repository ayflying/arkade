package compose

// pulltask.go：编排「更新镜像」的异步任务（compose pull 的流式执行）。
//
// ============================ 为什么要有它 ============================
//
// compose pull 要几十秒到几分钟。若让一次 RPC 等到底：
//   - 会撞上 Hub 请求超时，用户看到"失败"，实际镜像已被更新；
//   - 用户看不到任何输出，无法判断是卡住还是在拉大层。
//
// 因此：start 立即返回 task_id，get 增量轮询。
//
// ============================ 与 imagetransfer 的关系 ============================
//
// 借鉴了它的核心设计：任务只绑定本进程、limit 是硬约束、ctx 不用请求 ctx。
// 但**不复用**它的实现：那边的任务围绕"归档文件与大字节流"，
// 这里的任务是"跑一条命令并推流"，两者的 ioMu/writers/quota 全都不适用。
// 强行复用会让两边都变复杂。
//
// ============================ 安全边界 ============================
//
//  1. 只执行**固定的 compose pull**，参数由 Agent 拼死，不接受任何用户参数；
//  2. 不经过 shell（exec(argv)），因此不存在注入面；
//  3. 输出**必须**脱敏；脱敏信息读不到时 fail-closed（隐藏全部输出）；
//  4. 不重启业务：只 pull，绝不 up。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// 任务限额。超了直接拒绝——这些是硬约束，不是提示。
const (
	// MaxPullTasks 是最多保留的任务数（含已结束的）。
	//
	// 32 对应"一台机器上十几个项目，用户连续点几次更新"的现实规模。
	// 超出的请求被拒绝而不是排队：排队会让用户以为"点了没反应"，
	// 明确报错至少能让他等几秒再试。
	MaxPullTasks = 32
	// PullTaskTTL 是任务（尤其终态任务）的保留时长。
	//
	// 15 分钟：足够用户看清完整日志，又不会让内存长期占着
	// （每任务最多 256KB 输出，32 个任务上限 8MB）。
	PullTaskTTL = 15 * time.Minute
	// maxPullEvents 是单任务保留的最大行数。
	maxPullEvents = 2000
	// maxPullBytes 是单任务保留的最大输出字节数。
	maxPullBytes = 256 << 10
)

// 任务状态。与协议 ComposePullResult.Status 的取值一一对应。
const (
	PullQueued    = "queued"
	PullRunning   = "running"
	PullSucceeded = "succeeded"
	PullFailed    = "failed"
)

// 领域错误。
var (
	// ErrPullTaskNotFound 任务不存在或已过期。
	ErrPullTaskNotFound = errors.New("更新任务不存在或已过期")
	// ErrTooManyPullTasks 任务数已达上限。
	ErrTooManyPullTasks = errors.New("同时进行的镜像更新任务已达上限，请稍后再试")
	// ErrPullRequestMismatch 同一 request_id 被用于不同项目。
	ErrPullRequestMismatch = errors.New("该请求标识已用于其他项目")
	// ErrPullRedactUnavailable 无法安全读取脱敏配置。
	//
	// 这是 fail-closed 的分界线：宁可让用户看不到任何输出，
	// 也不能在"不知道该遮什么"的前提下把原始输出推出去。
	ErrPullRedactUnavailable = errors.New("无法安全读取项目的脱敏配置，已隐藏命令输出")
)

// PullEvent 是一行已脱敏的输出。
type PullEvent struct {
	Seq    int64
	Stream string
	Text   string
}

// PullTask 是一次镜像更新任务。
type PullTask struct {
	id         string
	name       string
	requestID  string
	command    string
	workingDir string

	ctx    context.Context
	cancel context.CancelFunc

	mu         sync.Mutex
	status     string
	events     []PullEvent
	nextSeq    int64
	totalBytes int
	truncated  bool
	exitCode   *int
	errText    string
	startedAt  time.Time
	finishedAt time.Time
	createdAt  time.Time
	// redactedUnavailable 表示脱敏配置读取失败：此时所有输出都被隐藏。
	redactFailed bool
}

// PullSnapshot 是任务的一次快照（协议层的输入）。
type PullSnapshot struct {
	TaskID     string
	Name       string
	Command    string
	WorkingDir string
	Status     string
	Output     []PullEvent
	NextCursor int64
	Truncated  bool
	ExitCode   *int
	Error      string
	StartedAt  time.Time
	FinishedAt time.Time
}

// PullManager 管理全部镜像更新任务。
//
// 与 Manager 分开一个类型：任务表有独立的锁与生命周期，
// 混进 Manager 会让那个已经很长的类型再长一截，
// 且任务清理与项目锁是两件互不相关的事。
type PullManager struct {
	mgr *Manager

	mu sync.Mutex
	// byID 是任务表。
	byID map[string]*PullTask
	// byRequest 是幂等键 → task_id。
	byRequest map[string]string

	// timeout 是单任务预算（等锁 + 执行共用）。
	//
	// 默认为 TimeoutLong；测试注入短值以验证超时路径，
	// 否则验证一次超时要真等 10 分钟。
	timeout time.Duration

	// rootCtx 是 Agent 生命周期上下文。
	// rootCtx 是 Agent 生命周期上下文。
	//
	// 所有任务都挂在它上面：Agent 关闭时任务一起停。
	// **不是**请求 ctx——请求在 start 返回时就结束了，
	// 挂请求 ctx 会让每个任务刚启动就自杀。
	rootCtx context.Context
}

// NewPullManager 构造任务管理器。
//
// rootCtx 必须来自 Agent 主循环（而非单个请求），否则长任务会被提前取消。
// 传 nil 表示使用 Manager 上记录的生命周期上下文（见 Manager.RootCtx）。
func NewPullManager(mgr *Manager, rootCtx context.Context) *PullManager {
	if rootCtx == nil && mgr != nil {
		rootCtx = mgr.RootCtx
	}
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	return &PullManager{
		mgr:       mgr,
		byID:      make(map[string]*PullTask),
		byRequest: make(map[string]string),
		rootCtx:   rootCtx,
		timeout:   TimeoutLong,
	}
}

// StartPull 启动一次 compose pull，立即返回快照。
//
// 幂等：同一 requestID 重复调用返回同一个任务，命令只执行一次。
func (p *PullManager) StartPull(name, requestID string) (PullSnapshot, error) {
	n, err := cleanName(name)
	if err != nil {
		return PullSnapshot{}, err
	}
	requestID = strings.TrimSpace(requestID)
	// 长度上限：request_id 会成为任务表里的 map key。直连 RPC 的调用方
	// 可以传任意长度（不经过 Hub 的校验），不限制就是一个无界内存增长的入口。
	if len(requestID) < 8 || len(requestID) > 128 {
		return PullSnapshot{}, errors.New("请求标识长度必须在 8 到 128 字节之间")
	}

	p.mu.Lock()
	// 幂等检查必须在建任务之前：Hub 重试时两次请求可能几乎同时到达。
	if existingID, ok := p.byRequest[requestID]; ok {
		if task, alive := p.byID[existingID]; alive {
			if task.name != n {
				p.mu.Unlock()
				return PullSnapshot{}, ErrPullRequestMismatch
			}
			snap := task.snapshot(0)
			p.mu.Unlock()
			return snap, nil
		}
		// 记录存在但任务已被清理：清掉陈旧映射，按新请求处理。
		delete(p.byRequest, requestID)
	}

	p.sweepLocked()

	if len(p.byID) >= MaxPullTasks {
		p.mu.Unlock()
		return PullSnapshot{}, ErrTooManyPullTasks
	}

	if err := p.mgr.requireExists(n); err != nil {
		p.mu.Unlock()
		return PullSnapshot{}, err
	}

	// 任务 ctx 挂在 Agent 生命周期上，与请求无关。
	ctx, cancel := context.WithCancel(p.rootCtx)
	task := &PullTask{
		id:        newPullTaskID(),
		name:      n,
		requestID: requestID,
		ctx:       ctx,
		cancel:    cancel,
		status:    PullQueued,
		createdAt: time.Now(),
	}
	task.command = p.mgr.pullCommandDisplay(n)
	task.workingDir = p.mgr.pullWorkDir(n)
	p.byID[task.id] = task
	p.byRequest[requestID] = task.id
	snap := task.snapshot(0)
	p.mu.Unlock()

	// 命令在独立 goroutine 里跑，start 立即返回。
	go p.run(task)
	return snap, nil
}

// GetPull 返回任务快照（只含 seq > after 的输出）。
func (p *PullManager) GetPull(taskID string, after int64) (PullSnapshot, error) {
	p.mu.Lock()
	// 查询路径也清理过期任务。
	//
	// 只在 Start 里清理的话，一个"启动过一次就再没人点更新"的项目，
	// 它的终态任务会一直占着内存直到下次 start——而用户以为早已过期。
	// 这里清理是顺带的（O(n)，n 上限 32），不引入后台定时器。
	p.sweepLocked()
	task, ok := p.byID[taskID]
	p.mu.Unlock()
	if !ok {
		return PullSnapshot{}, ErrPullTaskNotFound
	}
	return task.snapshot(after), nil
}

// run 执行任务主体。
// run 执行任务主体。
//
// ============================ 状态与锁的顺序（重要） ============================
//
// 任务在**拿到项目锁之前保持 queued**：此时命令还没开始，报 running 会让用户
// 以为镜像已经在拉，实际它在排队等另一个操作。真正开始执行才转 running。
//
// ============================ 三个必须在锁内完成的事 ============================
//
//  1. requireExists：锁外检查会在"检查通过"与"真正执行"之间被 Remove 掉项目；
//  2. buildRedactor：这是 TOCTOU 的关键——若在锁外读 .env，
//     并发的 Apply 可能刚把 .env 换成新值，于是我们用旧值做脱敏、
//     命令却按新值执行，新密码就漏了；
//  3. 因此"读 .env → 构造脱敏器 → 执行命令"必须在**同一个**项目锁临界区内，
//     中间不允许有任何 Apply 插进来。
//
// 脱敏失败是 fail-closed：不执行命令，直接失败。
func (p *PullManager) run(task *PullTask) {
	defer task.cancel()

	// 预算从**等锁之前**开始算：否则"等 10 分钟锁 + 跑 10 分钟命令"
	// 总耗时翻倍，用户无从预期。
	runCtx, cancel := context.WithTimeout(task.ctx, p.timeout)
	defer cancel()

	var (
		res    Result
		lockEr error
	)
	lockErr := p.mgr.withProject(runCtx, task.name, func() error {
		// 锁内：确认项目仍存在。
		if err := p.mgr.requireExists(task.name); err != nil {
			lockEr = err
			return nil
		}

		// ============================ 锁内构建执行对象 ============================
		// runner 必须在这里构造，并**同时**用于三件事：
		//   1. 展示命令（command 字段）——必须是真正要执行的 argv；
		//   2. 脱敏（读同一份 .env）；
		//   3. 执行。
		//
		// 若在 StartPull 时预构造 command，排队期间并发的 Apply 可能
		// 增删了 .env，于是"展示的命令"与实际执行的命令不一致；
		// 脱敏若另外再读一次，也会与执行用的环境不一致。
		// 锁内一次成型后就只有一个真相来源。
		r := p.mgr.runnerFor(task.name)
		dir := p.mgr.pullWorkDir(task.name)
		r.Dir = dir

		red, rerr := p.mgr.buildRedactorWith(r)
		if rerr != nil {
			lockEr = ErrPullRedactUnavailable
			return nil
		}

		argv := append([]string{r.binary()}, composeArgs(task.name, r.EnvFile, "pull")...)
		display := shellQuoteArgv(argv)

		// 到这里才真正开始执行——状态与展示命令在此刻一并生效。
		task.mu.Lock()
		task.status = PullRunning
		task.startedAt = time.Now()
		task.command = display
		task.workingDir = dir
		task.mu.Unlock()

		sink := &taskSink{task: task, red: red}
		res = r.RunStream(runCtx, task.name, p.timeout, sink, "pull")
		return nil
	})

	task.mu.Lock()
	defer task.mu.Unlock()
	task.finishedAt = time.Now()

	// 等锁阶段失败：命令根本没跑，退出码必须是 -1
	// （0 会被前端读成"成功"）。
	if lockErr != nil || lockEr != nil {
		failed := -1
		task.exitCode = &failed
		task.status = PullFailed
		if errors.Is(lockEr, ErrPullRedactUnavailable) {
			task.errText = ErrPullRedactUnavailable.Error()
			task.redactFailed = true
			return
		}
		if lockEr != nil {
			// requireExists 的文案（项目不存在）。
			task.errText = lockEr.Error()
			return
		}
		task.errText = "等待项目锁超时，请稍后重试"
		return
	}

	exitCode := res.ExitCode
	task.exitCode = &exitCode
	if res.TimedOut {
		task.status = PullFailed
		task.errText = "镜像更新超时，已终止"
		return
	}
	if res.ExitCode != 0 {
		task.status = PullFailed
		// 通用文案：详细原因已在流式输出里（已脱敏），这里不重复也不猜测。
		task.errText = "镜像更新失败，请查看命令输出"
		return
	}
	task.status = PullSucceeded
}

// taskSink 把 Runner 的流式输出写入任务缓冲（含脱敏）。
