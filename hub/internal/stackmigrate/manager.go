package stackmigrate

// manager.go：迁移任务的登记、调度与 7 阶段编排。
//
// ============================ 阶段划分的依据 ============================
//
// 每个阶段都有独立的"失败后可回答的问题"：
//
//	1 校验      —— 能不能做？（在线、权限、同名冲突、编排锁）
//	2 读配置    —— 源端到底有什么？（内容 + 哈希）
//	3 解析清单  —— 要搬多少东西？（镜像清单 / 卷清单 / 跳过项）
//	4 镜像      —— 逐个搬
//	5 数据卷    —— 逐个搬（仅当用户勾选）
//	6 写配置    —— 目标端落盘（不启动）
//	7 启动      —— 按 start_after 决定
//
// 阶段 4/5 是唯一耗时的部分，也是唯一可能"做了一半"的部分。
// 因此它们必须逐个汇报进度，而 1/2/3 只需要一行结论。
//
// ============================ 失败处理的原则 ============================
//
// 失败立即停止，并**如实报告**：
//   - 最远推进到哪个阶段；
//   - 目标上留下了什么（已导入镜像 / 已恢复卷 / 已写配置 / 已启动）。
//
// **绝不静默清理用户数据**。理由：清理本身可能失败，
// 而"清理失败 + 已删了一半"比"留着完整的一份"难收拾得多；
// 用户也可能正是想保留已导入的镜像再手工补完。
//
// ============================ 并发 ============================
//
// 两条约束：
//   - 同一对服务器同时只允许一个迁移（否则两个迁移会争同一个目标磁盘/网络）；
//   - 全局任务数上限（内存与磁盘的硬约束）。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// Backend 是迁移编排需要的外部能力。
//
// ============================ 为什么定义一个宽接口 ============================
//
// 它是编排器与"真实世界"之间唯一的接缝：所有 RPC 都从这里走。
// 定义一个只含必需方法的接口，测试就能用纯内存替身覆盖全部阶段
// （含失败路径），而不必起两台服务器、不下载任何镜像。
//
// 生产实现由 httpx 层适配 agentlink 得到（见 httpx 的 migrate_backend.go）。
type Backend interface {
	// CanManage 判断调用者能否管理该服务器上的编排。
	CanManage(ctx context.Context, agentID, stackName string) (bool, error)
	// AgentOnline 判断 Agent 是否在线。
	AgentOnline(agentID string) bool
	// StackExists 判断该 Agent 上是否已有同名编排。
	StackExists(ctx context.Context, agentID, stackName string) (bool, error)
	// StackBusy 判断该编排是否已有编排操作在进行。
	StackBusy(ctx context.Context, agentID, stackName string) (bool, error)
	// ReadCompose 读取源端编排配置与 env，返回内容与哈希。
	ReadCompose(ctx context.Context, agentID, stackName string) (config, env, hash string, err error)
	// ImagesOf 返回该编排用到的镜像引用（去重后）。
	ImagesOf(ctx context.Context, agentID, stackName string) ([]string, error)
	// CopyImage 把一个镜像从源端搬到目标端。
	CopyImage(ctx context.Context, source, target, reference string,
		progress func(done, total int64)) error
	// VolumesOf 返回该编排的卷迁移计划。
	VolumesOf(ctx context.Context, agentID, stackName string) (VolumePlan, error)
	// CopyVolume 把一个数据卷从源端搬到目标端。
	CopyVolume(ctx context.Context, source, target, volume string,
		progress func(done, total int64)) error
	// ImportCompose 把配置写到目标端（不启动）。
	ImportCompose(ctx context.Context, agentID, stackName, config, env, hash string) error
	// StartStack 在目标端启动编排。
	StartStack(ctx context.Context, agentID, stackName string) error
}

// VolumePlan 是一个编排的卷迁移计划（由 Agent 解析后传回）。
type VolumePlan struct {
	// Volumes 是要迁移的卷名。
	Volumes []string
	// BindDirs 是宿主机绑定目录数（不迁移，但必须统计上报）。
	BindDirs int
	// Anonymous 是匿名卷数（不迁移）。
	Anonymous int
	// External 是外部卷名（要迁移，但需提示是共享卷的副本）。
	External []string
}

// Options 是一次迁移的参数。
type Options struct {
	// StackName 是源端编排名（也是目标端的名字）。
	StackName string
	// Source 是源 Agent 标识。
	Source string
	// Target 是目标 Agent 标识。
	Target string
	// IncludeVolumes 表示是否迁移数据卷（**默认 false**）。
	IncludeVolumes bool
	// StartAfter 表示完成后是否启动（**默认 false**）。
	StartAfter bool
	// RequestID 是幂等键。
	RequestID string
	// OwnerID 是发起人（审计用）。
	OwnerID string
}

// Manager 管理全部迁移任务。
type Manager struct {
	backend Backend
	now     func() time.Time

	mu    sync.Mutex
	tasks map[string]*Task
	// byRequest 是 request_id → task_id，用于幂等。
	byRequest map[string]string
	// busyPair 是"源→目标"的组合占位。
	busyPair map[string]string
}

// New 构造编排器。
func New(backend Backend) *Manager {
	return &Manager{
		backend:   backend,
		now:       time.Now,
		tasks:     make(map[string]*Task),
		byRequest: make(map[string]string),
		busyPair:  make(map[string]string),
	}
}

// pairKey 生成服务器组合的占位键。
func pairKey(source, target string) string { return source + "→" + target }

// Submit 登记并启动一次迁移，立即返回任务快照。
//
// ============================ 为什么立即返回 ============================
// 迁移要几分钟到几十分钟。同步等到底会被 HTTP 超时切断，
// 而切断之后用户既看不到进度，也不知道操作是否还在跑。
func (m *Manager) Submit(ctx context.Context, opts Options) (Snapshot, error) {
	// 同一 request_id 幂等：同参数返回同一个任务，不同参数报冲突。
	if opts.RequestID != "" {
		m.mu.Lock()
		if id, ok := m.byRequest[opts.RequestID]; ok {
			if t, ok2 := m.tasks[id]; ok2 {
				// 参数必须一致：参数变了说明用户提交的是另一次迁移，
				// 静默复用旧任务会让用户以为新参数已生效。
				if t.Name != opts.StackName || t.Source != opts.Source ||
					t.Target != opts.Target || t.IncludeVolumes != opts.IncludeVolumes ||
					t.StartAfter != opts.StartAfter {
					m.mu.Unlock()
					return Snapshot{}, ErrDuplicateRequest
				}
				snap := t.snapshot(0)
				m.mu.Unlock()
				return snap, nil
			}
		}
		m.mu.Unlock()
	}

	m.mu.Lock()
	m.sweepLocked()
	live := 0
	for _, t := range m.tasks {
		t.mu.Lock()
		if !terminal(t.status) {
			live++
		}
		t.mu.Unlock()
	}
	if live >= MaxTasks {
		m.mu.Unlock()
		return Snapshot{}, ErrTooManyTasks
	}
	// 同一对服务器同时只允许一个迁移。
	pk := pairKey(opts.Source, opts.Target)
	if _, busy := m.busyPair[pk]; busy {
		m.mu.Unlock()
		return Snapshot{}, ErrPairBusy
	}

	id := newTaskID()
	t := &Task{
		id: id, Name: opts.StackName, RequestID: opts.RequestID,
		Source: opts.Source, Target: opts.Target,
		IncludeVolumes: opts.IncludeVolumes, StartAfter: opts.StartAfter,
		Enabled: true, status: StatusQueued, createdAt: m.now(),
	}
	m.tasks[id] = t
	m.busyPair[pk] = id
	if opts.RequestID != "" {
		m.byRequest[opts.RequestID] = id
	}
	m.mu.Unlock()

	// 登记完才真正开跑：Submit 必须立刻返回。
	go m.run(t, opts)

	return t.snapshot(0), nil
}

// Get 增量读取任务事件。
func (m *Manager) Get(taskID string, after int64) (Snapshot, error) {
	m.mu.Lock()
	t, ok := m.tasks[taskID]
	m.mu.Unlock()
	if !ok {
		return Snapshot{}, ErrTaskNotFound
	}
	return t.snapshot(after), nil
}

// sweepLocked 清理过期任务（调用方已持锁）。
func (m *Manager) sweepLocked() {
	now := m.now()
	for id, t := range m.tasks {
		t.mu.Lock()
		expired := terminal(t.status) && now.Sub(t.finishedAt) > TaskTTL
		t.mu.Unlock()
		if !expired {
			continue
		}
		delete(m.tasks, id)
		if t.RequestID != "" {
			delete(m.byRequest, t.RequestID)
		}
		// 释放组合占位。
		pk := pairKey(t.Source, t.Target)
		if m.busyPair[pk] == id {
			delete(m.busyPair, pk)
		}
	}
}

// run 执行 7 阶段编排。
//
// ============================ 这个函数是产品的核心路径 ============================
// 它的每一个 return 都对应一条用户可能看到的失败报告，
// 因此每个失败分支都必须：
//   - 写明停在哪一步；
//   - 说明目标上留下了什么。
func (m *Manager) run(t *Task, opts Options) {
	// 任务上下文不继承请求 ctx：HTTP 断开**不该**取消正在跑的迁移。
	// 那会让用户"关掉页面"变成"迁移失败"。
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	// ============================ 收尾：一次临界区完成两件事 ============================
	// 必须**先释放组合占位，再置终态**，且两步要在同一次加锁里完成。
	//
	// 反过来的话，Get 会先看到 succeeded，而占位还挂在表里，
	// 于是用户"看到完成了 → 立刻再点一次迁移"会收到
	// "该服务器组合已有迁移正在进行"——一个已经结束的任务挡住了新的。
	// 这个窗口很短但真实存在，而用户的重试时机恰好总在窗口内
	// （他看到成功就马上点）。
	//
	// 分两次加锁也不行：两次之间仍是可观测的窗口。
	// 因此收尾统一放在 finish() 里，由它在持有 m.mu 时顺手改 t 的状态。
	// 锁顺序与别处一致（先全局后任务），不会引入死锁。
	finish := func(status, errText string) {
		t.mu.Lock()
		t.finishedAt = m.now()
		t.mu.Unlock()

		m.mu.Lock()
		t.mu.Lock()
		t.status = status
		if errText != "" {
			t.errText = errText
		}
		t.mu.Unlock()
		pk := pairKey(t.Source, t.Target)
		if m.busyPair[pk] == t.id {
			delete(m.busyPair, pk)
		}
		m.mu.Unlock()
	}

	t.mu.Lock()
	t.status = StatusRunning
	t.startedAt = m.now()
	t.mu.Unlock()

	res := &Residual{}
	if err := m.runStages(ctx, t, opts, res); err != nil {
		// ============================ 失败：立即停止并如实报告 ============================
		t.setResidual(*res)
		stage := t.stage
		errText := "迁移失败（停在第 " + itoa(StageIndex(stage)) + " 步「" +
			StageTitle(stage) + "」）：" + err.Error() + "。" + res.Describe()

		// 错误行用 stderr 流：前端对它的样式就是红色错误行。
		t.appendEvent(StreamStderr, errText)
		finish(StatusFailed, errText)
		return
	}

	t.setResidual(*res)
	t.setStage(StageFinish)
	t.appendEvent(StreamStage, "迁移完成")
	finish(StatusSucceeded, "")
}

// runStages 依次执行各阶段，返回第一个错误。
func (m *Manager) runStages(ctx context.Context, t *Task, opts Options, res *Residual) error {
	stage := func(s string) {
		t.setStage(s)
		t.appendEvent(StreamStage, "第 "+itoa(StageIndex(s))+" 步「"+StageTitle(s)+"」")
	}
	// ============================ 阶段 1：校验 ============================
	stage(StageValidate)

	if !m.backend.AgentOnline(opts.Source) {
		return errors.New("源服务器当前离线")
	}
	if !m.backend.AgentOnline(opts.Target) {
		return errors.New("目标服务器当前离线")
	}
	if opts.Source == opts.Target {
		// 同一台机器上的"迁移"没有意义，且会让阶段 6 出现同名冲突。
		return errors.New("源服务器与目标服务器不能是同一台")
	}
	// 权限：两端都必须有 manage（迁移会改目标端配置并可能启动服务）。
	ok, err := m.backend.CanManage(ctx, opts.Source, opts.StackName)
	if err != nil {
		return errors.New("无法确认你对源服务器的管理权限：" + err.Error())
	}
	if !ok {
		return errors.New("你没有源服务器上该编排的管理权限")
	}
	ok, err = m.backend.CanManage(ctx, opts.Target, opts.StackName)
	if err != nil {
		return errors.New("无法确认你对目标服务器的管理权限：" + err.Error())
	}
	if !ok {
		return errors.New("你没有目标服务器的管理权限")
	}
	// 目标端同名拒绝：**绝不覆盖**，提示用户改名。
	exists, err := m.backend.StackExists(ctx, opts.Target, opts.StackName)
	if err != nil {
		return errors.New("无法检查目标服务器上的同名编排：" + err.Error())
	}
	if exists {
		return errors.New("目标服务器上已存在同名编排「" + opts.StackName +
			"」，请先在目标上改名或删除后再迁移")
	}
	// 编排锁：源端正在执行编排操作时不宜迁移（配置与容器状态可能变化）。
	busy, err := m.backend.StackBusy(ctx, opts.Source, opts.StackName)
	if err != nil {
		return errors.New("无法确认源端编排是否正在操作：" + err.Error())
	}
	if busy {
		return errors.New("源端编排正在执行其它操作，请等它结束后再迁移")
	}
	t.appendEvent(StreamStdout, "校验通过：两端在线、权限具备、目标无同名编排")

	// ============================ 阶段 2：读取源配置 ============================
	stage(StageReadConfig)
	config, env, hash, err := m.backend.ReadCompose(ctx, opts.Source, opts.StackName)
	if err != nil {
		return errors.New("读取源端编排配置失败：" + err.Error())
	}
	if strings.TrimSpace(config) == "" {
		// ============================ 必须用 TrimSpace ============================
		// 空配置若被"成功"迁移过去，目标端会得到一个空编排，
		// 而用户以为服务与数据都搬过去了。只有空白的配置同样没有意义，
		// 因此不能只判 == ""。
		return errors.New("源端编排配置为空，无法迁移")
	}
	t.appendEvent(StreamStdout, "已读取源端编排配置（哈希 "+hash+"）")

	// ============================ 阶段 3：解析清单 ============================
	stage(StagePlan)
	images, err := m.backend.ImagesOf(ctx, opts.Source, opts.StackName)
	if err != nil {
		return errors.New("解析源端镜像清单失败：" + err.Error())
	}
	t.appendEvent(StreamStdout, "需要迁移的容器镜像 "+itoa(len(images))+" 个")

	// ============================ 卷清单：按 include_volumes 决定 ============================
	//
	// 【关键要求】未勾选迁移数据卷时，**不得触发任何卷导出**。
	//
	// 这里的做法是：连 VolumesOf 都不调用（它是只读解析，但更重要的是
	// 后面绝不能进入阶段 5）。为了让用户知道"跳过了什么"，
	// 用一个纯本地的占位值代替：由源端配置文本里是否出现顶层 volumes 段
	// 无法可靠推断，因此改为向源端要"计划"（只解析、不导出），
	// 拿到卷数量后立刻在结果里写明"未迁移"。
	//
	// 注意：VolumesOf 是**只读解析**（读 compose 配置、算命名规则），
	// 它不会起容器、不会导出任何字节。真正的导出在阶段 5，
	// 而阶段 5 只在 IncludeVolumes=true 时才进入。
	var plan VolumePlan
	if opts.IncludeVolumes {
		plan, err = m.backend.VolumesOf(ctx, opts.Source, opts.StackName)
		if err != nil {
			return errors.New("解析源端数据卷清单失败：" + err.Error())
		}
		if len(plan.Volumes) > 0 {
			t.appendEvent(StreamStdout, "需要迁移的数据卷 "+itoa(len(plan.Volumes))+" 个："+
				joinComma(plan.Volumes))
		} else {
			t.appendEvent(StreamStdout, "该编排没有需要迁移的命名数据卷")
		}
		if len(plan.External) > 0 {
			t.appendEvent(StreamStdout,
				"其中外部卷 "+itoa(len(plan.External))+" 个（迁移的是副本，不保证与其它使用者一致）："+
					joinComma(plan.External))
		}
	} else {
		// 未勾选：仍然**只读解析**出卷数量，用于如实告知"跳过了几个"。
		// 这不是导出，但用户需要这个数字才能判断"我是不是漏勾了"。
		plan, err = m.backend.VolumesOf(ctx, opts.Source, opts.StackName)
		if err != nil {
			// 解析失败不阻断迁移：用户没要求迁卷，卷信息只是"附带告知"。
			// 但必须让用户知道这个数字拿不到，而不是显示 0。
			t.appendEvent(StreamStderr,
				"无法解析该编排的数据卷清单（不影响本次迁移，但无法告知跳过了几个卷）")
			plan = VolumePlan{}
		} else if len(plan.Volumes) > 0 {
			t.appendEvent(StreamStdout,
				"本次未迁移数据卷，目标上将是空数据（该项目有 "+itoa(len(plan.Volumes))+
					" 个数据卷被跳过）")
		} else {
			t.appendEvent(StreamStdout, "本次未迁移数据卷（该项目没有命名数据卷）")
		}
	}
	// 宿主机绑定目录：两端都要如实告知，因为它们的**内容不会被复制**。
	if plan.BindDirs > 0 {
		t.appendEvent(StreamStdout,
			"已跳过宿主机绑定目录 "+itoa(plan.BindDirs)+" 个（迁移不会复制宿主机上的目录内容）")
	}
	if plan.Anonymous > 0 && opts.IncludeVolumes {
		t.appendEvent(StreamStdout,
			"已跳过匿名卷 "+itoa(plan.Anonymous)+" 个（没有稳定名字，无法迁移）")
	}

	t.mu.Lock()
	t.imageCount = len(images)
	t.volumeCount = len(plan.Volumes)
	t.bindDirs = plan.BindDirs
	if !opts.IncludeVolumes {
		t.skippedVolumes = len(plan.Volumes)
	}
	t.mu.Unlock()

	// ============================ 阶段 4：逐个迁镜像 ============================
	stage(StageImages)
	if len(images) == 0 {
		t.appendEvent(StreamStdout, "该编排没有需要迁移的镜像")
	}
	for i, ref := range images {
		t.appendEvent(StreamStdout, "["+itoa(i+1)+"/"+itoa(len(images))+"] 迁移镜像 "+ref)
		var lastLogged int
		err := m.backend.CopyImage(ctx, opts.Source, opts.Target, ref,
			func(done, total int64) {
				// 进度按 25% 一档输出：整块进度条式的逐字节事件
				// 会把事件缓冲瞬间挤爆，反而丢掉真正有用的行。
				if total <= 0 {
					return
				}
				pct := int(done * 100 / total)
				if pct/25 <= lastLogged {
					return
				}
				lastLogged = pct / 25
				t.appendEvent(StreamStdout, "  镜像 "+ref+" 已传输 "+itoa(pct)+"%")
			})
		if err != nil {
			return errors.New("迁移镜像 " + ref + " 失败：" + err.Error())
		}
		res.ImagesImported = i + 1
	}
	if len(images) > 0 {
		t.appendEvent(StreamStdout, "镜像迁移完成，共 "+itoa(len(images))+" 个")
	}

	// ============================ 阶段 5：逐个迁卷（仅勾选时） ============================
	if opts.IncludeVolumes && len(plan.Volumes) > 0 {
		stage(StageVolumes)
		for i, vol := range plan.Volumes {
			t.appendEvent(StreamStdout,
				"["+itoa(i+1)+"/"+itoa(len(plan.Volumes))+"] 迁移数据卷 "+vol)
			err := m.backend.CopyVolume(ctx, opts.Source, opts.Target, vol,
				func(done, total int64) {
					if total <= 0 {
						return
					}
					pct := int(done * 100 / total)
					if pct%25 == 0 {
						t.appendEvent(StreamStdout, "  数据卷 "+vol+" 已传输 "+itoa(pct)+"%")
					}
				})
			if err != nil {
				return errors.New("迁移数据卷 " + vol + " 失败：" + err.Error())
			}
			res.VolumesRestored = i + 1
		}
		t.appendEvent(StreamStdout, "数据卷迁移完成，共 "+itoa(len(plan.Volumes))+" 个")
	}

	// ============================ 阶段 6：写目标配置 ============================
	stage(StageImportConf)
	if err := m.backend.ImportCompose(ctx, opts.Target, opts.StackName, config, env, hash); err != nil {
		return errors.New("写入目标端编排配置失败：" + err.Error())
	}
	res.ComposeWritten = true
	t.appendEvent(StreamStdout, "已在目标服务器写入编排配置")

	// ============================ 阶段 7：按 start_after 决定是否启动 ============================
	stage(StageStart)
	if !opts.StartAfter {
		// 明确记录"已迁移，未启动"：否则用户会以为服务已经在跑了。
		t.appendEvent(StreamStdout, "已迁移，未启动（按你的选择没有在目标服务器上启动该编排）")
		return nil
	}
	if err := m.backend.StartStack(ctx, opts.Target, opts.StackName); err != nil {
		return errors.New("在目标服务器启动编排失败：" + err.Error())
	}
	res.Started = true
	t.appendEvent(StreamStdout, "已在目标服务器启动该编排")
	return nil
}
