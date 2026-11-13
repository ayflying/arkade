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
	// action 是本任务要执行的编排动作（up/down/stop/restart/pull/rebuild/remove/backup）。
	action string
	// fingerprint 是参数指纹，用于"同 request_id 不同参数"的冲突检测。
	fingerprint string
	// spec 是本任务的规范化参数。
	spec OperationSpec
	// backupPath 是 backup 成功后的相对备份路径（白名单字段）。
	backupPath string

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
	TaskID string
	Name   string
	// Action 是本次动作（up/down/stop/restart/pull/rebuild/remove/backup）。
	Action     string
	Command    string
	WorkingDir string
	// BackupPath 是 backup 动作成功后的备份路径（相对工作区，非绝对路径）。
	BackupPath string
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

	mu      sync.Mutex
	closed  bool
	workers sync.WaitGroup
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

// StartPull 启动一次 compose pull（兼容入口）。
//
// 它就是 StartOperation 的 action=pull 特例。保留这个函数是因为
// 既有调用方与测试都在用它，而 pull 仍然是最常用的动作。
func (p *PullManager) StartPull(name, requestID string) (PullSnapshot, error) {
	return p.StartOperation(OperationSpec{Action: ActionPull}, name, requestID)
}

// StartOperation 启动一次编排动作，立即返回快照。
//
// 幂等规则：
//   - 同 requestID + 同参数指纹 → 返回**同一个任务**，命令只执行一次；
//   - 同 requestID + 不同指纹 → ErrRequestFingerprintConflict（Hub 转 409）。
//
// 第二条是必要的：Hub 重试时若参数变了，说明用户实际提交了另一次操作，
// 静默返回旧任务会让用户以为新参数已生效，而实际执行的是旧参数。
func (p *PullManager) StartOperation(spec OperationSpec, name, requestID string) (PullSnapshot, error) {
	n, err := cleanName(name)
	if err != nil {
		return PullSnapshot{}, err
	}
	spec, err = normalizeSpec(spec)
	if err != nil {
		return PullSnapshot{}, err
	}
	requestID = strings.TrimSpace(requestID)
	// 长度上限：request_id 会成为任务表里的 map key。直连 RPC 的调用方
	// 可以传任意长度（不经过 Hub 的校验），不限制就是一个无界内存增长的入口。
	if len(requestID) < 8 || len(requestID) > 128 {
		return PullSnapshot{}, errors.New("请求标识长度必须在 8 到 128 字节之间")
	}
	fp := spec.fingerprint(n)

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return PullSnapshot{}, errors.New("编排任务管理器已关闭")
	}
	// 幂等检查必须在建任务之前：Hub 重试时两次请求可能几乎同时到达。
	if existingID, ok := p.byRequest[requestID]; ok {
		if task, alive := p.byID[existingID]; alive {
			if task.fingerprint != fp {
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

	// up 会在 ApplyProject 内部自己校验/创建项目（新编排在此时落盘），
	// 因此只有"对既有项目做操作"的动作才要求项目已存在。
	if spec.Action != ActionUp {
		if err := p.mgr.requireExists(n); err != nil {
			p.mu.Unlock()
			return PullSnapshot{}, err
		}
	}

	// 任务 ctx 挂在 Agent 生命周期上，与请求无关。
	ctx, cancel := context.WithCancel(p.rootCtx)
	task := &PullTask{
		id:          newPullTaskID(),
		name:        n,
		requestID:   requestID,
		action:      spec.Action,
		fingerprint: fp,
		spec:        spec,
		ctx:         ctx,
		cancel:      cancel,
		status:      PullQueued,
		createdAt:   time.Now(),
	}
	// queued 阶段只给工作目录；command 由第一条真实 command 事件填充，
	// 避免展示一条"预计"命令却与实际执行的不一致。
	task.workingDir = p.mgr.pullWorkDir(n)
	p.byID[task.id] = task
	p.byRequest[requestID] = task.id
	snap := task.snapshot(0)
	// 必须在持锁时登记，Close 封闭入口后才能安全 Wait。
	p.workers.Add(1)
	p.mu.Unlock()

	// 命令在独立 goroutine 里跑，start 立即返回。
	go func() {
		defer p.workers.Done()
		p.run(task)
	}()
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
//
// ============================ 不做外层锁（关键） ============================
//
// 八个编排动作都调用**原 Manager 方法**，而它们各自内部已经
// `withProject` 加项目锁。任务层若再套一层，就是同一把锁的重复获取——
// 直接死锁。因此这里只负责：准备观察者、调用、结算状态。
//
// ============================ 脱敏与执行的时序 ============================
//
// 脱敏器必须在**每次实际命令之前**用当时的磁盘内容重建：
// up 会先把新 spec 落盘再执行，所以命令运行时 .env 已是新的。
// 这里在任务开始时建一个"惰性"redactor：每来一条输出就用当前快照脱敏，
// 而快照在每次 command 事件前刷新。
func (p *PullManager) run(task *PullTask) {
	defer task.cancel()

	runCtx, cancel := context.WithTimeout(task.ctx, p.timeout)
	defer cancel()

	sink := &taskSink{task: task}

	// ============================ 新 spec 的秘密必须先纳入 ============================
	// up 的 precheck / 首次执行用的 .env 是**新写入**的，此刻磁盘上可能还是旧值。
	// 若只读 workspace，新密码根本不在脱敏集合里——命令一回显就漏了。
	//
	// 因此在任何命令执行前，先把 task.spec 里的 Env/Config 提取成 superset。
	// 提取失败（含无法解析的插值等）→ 无从得知要遮什么，改为隐藏全部输出，
	// 但业务照常执行（这是用户明确授权的取舍）。
	if !sink.seedFromSpec(p.mgr, task) {
		sink.hideAll = true
	}

	ob := &executionObserver{
		onCommand: func(argv []string, r *Runner) {
			// 命令所在目录取**真实 Runner 的 Dir**：up 的 precheck 在临时目录跑，
			// 报成最终 workspace 就是虚报。
			dir := p.mgr.pullWorkDir(task.name)
			if r != nil && r.Dir != "" {
				dir = r.Dir
			}
			// 必须先置 running 再写事件：appendEvent 只在 running 时接收，
			// 否则第一条 command（以及紧随其后的输出）会被静默丢弃。
			task.markRunning(dir)

			display := shellQuoteArgv(argv)
			// 用真实 Runner 刷新脱敏：这条命令真正会读哪份 .env，就用哪份遮。
			// 刷新放在取值之前完成，随后一次成型地脱敏，不留原始值窗口。
			sink.refreshFromRunner(p.mgr, r)
			if !sink.hideAll {
				display = sanitizeLine(display, sink.currentRedactor())
			}
			task.setCommand(display, dir)
			sink.pushCommandSanitized(display)
		},
		onStdout: func(line string) { sink.push("stdout", line) },
		onStderr: func(line string) { sink.push("stderr", line) },
		onStage: func(name string, r *Runner) {
			// 阶段同样带真实目录，由前端决定怎么展示。
			dir := p.mgr.pullWorkDir(task.name)
			if r != nil && r.Dir != "" {
				dir = r.Dir
			}
			task.markRunning(dir)
			sink.pushStage(name)
		},
	}

	// 先按 workspace 建一次，保证没有 runner 的场景（如 backup）也能脱敏。
	sink.refreshFromRunner(p.mgr, nil)

	// 任务保持 queued 直到第一条 command/stage 事件到来。
	// 在等锁期间就置 running 会让用户以为命令已经在跑，而实际还在排队。

	execCtx := withObserver(runCtx, ob)
	exitCode, backupPath, err := p.mgr.execute(execCtx, task.name, task.spec)

	// 若整个动作没有产生任何 command/stage（极少数实现路径），
	// 仍需把状态推向终态；先把 running 补上以免快照出现 queued→failed 的怪状态。
	task.markRunningIfQueued(p.mgr.pullWorkDir(task.name))

	task.mu.Lock()
	defer task.mu.Unlock()
	task.finishedAt = time.Now()
	task.backupPath = backupPath

	// 超时与取消要能被区分：前者文案说"超时"，后者说"已停止"。
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		ec := -1
		task.exitCode = &ec
		task.status = PullFailed
		task.errText = "操作超时，已终止"
		return
	}
	if runCtx.Err() != nil {
		ec := -1
		task.exitCode = &ec
		task.status = PullFailed
		task.errText = "操作已取消"
		return
	}

	task.exitCode = &exitCode
	if err != nil {
		// ============================ 失败不能宣告退出码 0 ============================
		// up 的 precheck 失败、backup 的 IO 错误都发生在**命令之外**，
		// 此时没有任何子进程退出码可言，返回的是零值 0。
		// 若原样上报，前端会显示"失败（退出码 0）"——自相矛盾，
		// 用户无法判断到底跑没跑。
		//
		// 因此这类"没有实际退出码"的失败统一记为 -1。
		// 真正由子进程失败导致的（exitCode != 0）保持真实值。
		if exitCode == 0 {
			ec := -1
			task.exitCode = &ec
		}
		task.status = PullFailed
		// 错误文案必须**不含 secret**：这里只放领域错误的通用文本，
		// 具体细节留给（已脱敏的）输出流。
		task.errText = genericErrorText(err)
		return
	}
	if exitCode != 0 {
		task.status = PullFailed
		task.errText = "操作失败，请查看命令输出"
		return
	}
	task.status = PullSucceeded
}

// genericErrorText 把领域错误转成**不含 secret** 的通用文案。
//
// precheck 失败、配置非法等错误里可能带原始配置片段，
// 直接回显会把 .env 值送去浏览器，因此一律给固定文本。
func genericErrorText(err error) string {
	switch {
	case errors.Is(err, ErrUnknownAction), errors.Is(err, ErrActionParams):
		return err.Error()
	case errors.Is(err, ErrPullRedactUnavailable):
		// 含无法解析的 env 时，输出已隐藏，业务继续——
		// 这种情况**不是失败**（见 operation 的调用方），
		// 因此这里只作为兜底出现。
		return err.Error()
	default:
		return "操作失败，详细原因请查看命令输出"
	}
}

// taskSink 把观察者收到的输出写入任务缓冲（含脱敏）。
//
// ============================ 惰性脱敏 ============================
//
// 脱敏器不能只在任务开始时建一次：up 会先落盘新 spec 再执行命令，
// 命令用的 .env 与开始时那份可能不同。因此在每次 command 事件前
// 刷新快照，输出行用"此刻最新"的集合去遮。
//
// 惰性还有第二个好处：backup 这类没有 command 事件的动作，
// 不会因为"不知道要遮什么"而被迫 fail-closed。
type taskSink struct {
	task *PullTask
	// mu 串行化 stdout/stderr 两路回调，保证 seq 顺序就是写入顺序。
	mu  sync.Mutex
	red *redactor
	// hideAll 表示脱敏信息不可用：**输出全部隐藏，但业务继续**。
	//
	// 这是用户明确授权的取舍：日志脱敏不能反过来阻止用户本来就能做的操作。
	// 隐藏时只发一条固定说明，绝不发原文。
	hideAll bool
	// specHideFailed 表示"新 spec 里的秘密无法解析"。
	//
	// ============================ 为什么必须是粘性的 ============================
	// up 执行的 .env 是**新写入**的；若它含复杂插值，我们根本无从得知
	// 真实值，只能隐藏输出。但这个结论不能被后续"旧配置解析成功"覆盖：
	//   - precheck 阶段可能用的是临时目录里那份**能解析**的旧配置；
	//   - 一旦 hideAll 被重置为 false，随后输出的新 spec 秘密就会漏出去。
	//
	// 因此这里一旦为真就永不清零：宁可整场任务都看不到输出，
	// 也不能在某个阶段意外泄露。
	specHideFailed bool
	// notifiedHide 保证"已隐藏"的说明只发一次。
	notifiedHide bool
}

// seedFromSpec 把 task.spec 里的 Env/Config 秘密并入初始脱敏集合。
//
// ============================ 为什么必须有这一步 ============================
//
// up 的执行时序是"先落盘新 spec，再在临时目录做 precheck，最后 up"。
// 在 precheck 之前，磁盘上的 .env 可能还是旧的；而 precheck 命令
// 已经会用**新**配置去校验。此时若只按 workspace 建脱敏器，
// 新密码根本不在集合里，precheck 一回显就漏。
//
// 因此这里先按 spec 文本本身提取（与磁盘状态无关），保证新秘密从一开始就被遮。
// 提取不出来（复杂插值/转义）→ 返回 false，调用方改为隐藏全部输出。
func (s *taskSink) seedFromSpec(mgr *Manager, task *PullTask) bool {
	secrets := make([]string, 0, 16)

	// .env 文本：用与 .env 文件同一套安全子集解析。
	if task.spec.Env != "" {
		vals, ok := envValues(task.spec.Env)
		if !ok {
			s.markSpecHideFailed()
			return false
		}
		for _, v := range vals {
			secrets = append(secrets, v)
		}
	}
	// 编排正文：内联 environment 里的明文。
	if task.spec.Config != "" {
		inline, err := composeInlineSecrets(task.spec.Config)
		if err != nil {
			s.markSpecHideFailed()
			return false
		}
		secrets = append(secrets, inline...)
	}

	if len(secrets) == 0 {
		return true
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// 与既有集合合并（superset）：磁盘旧值 + spec 新值都要遮，
	// 因为 precheck 前后两套配置都可能出现在输出里。
	var merged []string
	if s.red != nil {
		merged = append(merged, s.red.literals...)
	}
	merged = append(merged, secrets...)
	s.red = newRedactor(merged)
	return true
}

// markSpecHideFailed 记录"新 spec 的秘密无法解析"，并立即开始隐藏输出。
//
// 单独抽出来是为了让调用方不必自己持锁修改两个字段——
// hideAll 与 specHideFailed 必须同时置位，漏一个就会出现
// "以为在隐藏、实际已恢复输出"的窗口。
func (s *taskSink) markSpecHideFailed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.specHideFailed = true
	s.hideAll = true
}

// currentRedactor 返回当前脱敏器（可能为 nil）。
func (s *taskSink) currentRedactor() *redactor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.red
}

// refreshFromRunner 用**真实执行的 Runner** 重建脱敏器。
//
// r 为 nil 时退回按项目的 workspace 推断（backup 这类无 Runner 的场景）。
func (s *taskSink) refreshFromRunner(mgr *Manager, r *Runner) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 新 spec 的秘密曾无法解析：这一场任务的输出必须始终隐藏。
	// 绝不能因为"此刻某个目录里的配置能解析"就把 hideAll 关掉——
	// 能解析的那份往往不含新密码，关掉就等于让新密码露面。
	if s.specHideFailed {
		s.hideAll = true
		return
	}

	var (
		red *redactor
		err error
	)
	if r != nil {
		red, err = mgr.buildRedactorWith(r)
	} else {
		red, err = mgr.buildRedactor(s.task.name)
	}
	if err != nil {
		// 脱敏不可用：不 fail 掉业务，改为隐藏全部输出。
		s.hideAll = true
		return
	}
	// 与 spec 秘密合并（superset）：旧值和新值都必须被遮住。
	if s.red != nil {
		merged := append(append([]string{}, red.literals...), s.red.literals...)
		red = newRedactor(merged)
	}
	s.red = red
	s.hideAll = false
}

// hideNoticeText 是输出被隐藏时给出的固定说明。
const hideNoticeText = "（本项目的配置无法安全解析，命令输出已隐藏；操作仍在正常执行）"

func (s *taskSink) Stdout(line string) { s.push("stdout", line) }
func (s *taskSink) Stderr(line string) { s.push("stderr", line) }

// push 写入一条已脱敏（或已隐藏）的输出。
func (s *taskSink) push(stream, line string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.hideAll {
		// 只发一次固定说明，后续原文一个字节都不输出。
		if !s.notifiedHide {
			s.notifiedHide = true
			s.task.appendEvent("stdout", hideNoticeText)
		}
		return
	}
	text := sanitizeLine(line, s.red)
	s.task.appendEvent(stream, text)
}

// stage 上报一个明确的阶段（没有可展示的真实命令时使用）。
func (s *taskSink) stage(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 阶段是进度信息，即便输出被隐藏也要让用户知道"正在做什么"。
	s.task.appendEvent("stage", name)
}

// pushCommandSanitized 写入一条**已脱敏**的命令文本。
//
// 调用方已完成脱敏，这里不再过脱敏器：那样会在持锁路径里
// 二次 sanitize，既浪费又可能把已替换的占位符再处理一遍。
func (s *taskSink) pushCommandSanitized(display string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.task.appendEvent("command", display)
}

// pushStage 是 stage 的对齐命名版本（供 observer 回调使用）。
func (s *taskSink) pushStage(name string) { s.stage(name) }

// pushCommand 上报一条真实执行的命令。
//
// 命令文本要过脱敏：虽然我们只放 argv（不含 env 值），
// 但路径等参数仍可能夹带用户自定义的敏感片段。
//
// 注意：**命令不因脱敏不可用而被隐藏**。用户明确要求"脱敏不影响业务"，
// 而命令本身是用户自己发起的操作参数，不属于需要保护的服务端秘密；
// 真正要防的是 .env 值经 compose 输出回显。
func (s *taskSink) pushCommand(display string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	text := display
	if !s.hideAll {
		text = sanitizeLine(display, s.red)
	}
	s.task.appendEvent("command", text)
}

// setCommand 记录最近一次真实执行的命令与工作目录。
func (t *PullTask) setCommand(display, dir string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.command = display
	if dir != "" {
		t.workingDir = dir
	}
}

// markRunning 把任务置为运行中，并记录工作目录。
func (t *PullTask) markRunning(dir string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status != PullQueued {
		// 已经不是 queued：不覆盖 startedAt，只更新目录。
		if dir != "" {
			t.workingDir = dir
		}
		return
	}
	t.status = PullRunning
	t.startedAt = time.Now()
	if dir != "" {
		t.workingDir = dir
	}
}

// markRunningIfQueued 兜底：动作没有产生任何 command/stage 时，
// 避免快照出现 queued → failed 的怪状态。
func (t *PullTask) markRunningIfQueued(dir string) { t.markRunning(dir) }
