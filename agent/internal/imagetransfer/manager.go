package imagetransfer

// manager.go：任务表、并发上限、生命周期与清理。
// 操作分散在同包：pull.go / save.go / load.go / quota.go / storage.go。
//
// 锁纪律：Manager.mu 保护任务表与字段，**不持锁做 IO**；
// Task.ioMu 串行化单任务的文件操作。锁序单向——ioMu 在 Manager.mu 之外，
// 即 锁 ioMu → IO → 放 ioMu → 锁 Manager.mu 改字段。反向必然死锁。

import (
	"context"
	"errors"
	"sync"
	"time"

	"arkade/agent/internal/runtime"
)

// Limits 是管理器使用的限额配置。
type Limits struct {
	MaxArchiveBytes      int64
	MaxTotalArchiveBytes int64
	MaxConcurrentTasks   int
	ChunkSize            int64
}

// DefaultLimits 返回默认限额。
func DefaultLimits() Limits {
	return Limits{
		MaxArchiveBytes:      DefaultMaxArchiveBytes,
		MaxTotalArchiveBytes: DefaultMaxTotalArchiveBytes,
		MaxConcurrentTasks:   DefaultMaxConcurrentTasks,
		ChunkSize:            ChunkSize,
	}
}

// Manager 管理本进程内的全部传输任务。
type Manager struct {
	// rt 是镜像传输能力；rtOK 为 false 表示本引擎不支持（操作返回 unsupported）。
	rt   runtime.ImageTransferRuntime
	rtOK bool

	store  *storage
	limits Limits

	mu    sync.Mutex
	tasks map[string]*Task

	// now 可被测试替换，用于验证 TTL 而不必真的等待。
	now func() time.Time
}

// NewManager 构造管理器。
//
// rt 用类型断言探测可选能力：不支持时**不报错**，而是让所有操作
// 返回"本引擎不支持"。这样在不支持镜像传输的环境里，Agent 仍能正常启动。
func NewManager(dataDir string, rt runtime.Runtime, limits Limits) (*Manager, error) {
	if limits.MaxArchiveBytes <= 0 {
		limits = DefaultLimits()
	}
	if limits.ChunkSize <= 0 || limits.ChunkSize > ChunkSize {
		limits.ChunkSize = ChunkSize
	}
	store, err := newStorage(dataDir)
	if err != nil {
		return nil, err
	}
	m := &Manager{
		store:  store,
		limits: limits,
		tasks:  make(map[string]*Task),
		now:    time.Now,
	}
	if tr, ok := rt.(runtime.ImageTransferRuntime); ok && tr != nil {
		m.rt = tr
		m.rtOK = true
	}
	// 启动即清理：进程被 kill 时 defer 不会执行，残留只能在这里收拾。
	// 此时任务表为空，因此传入空的活动集。
	m.store.cleanupOrphans(nil)
	return m, nil
}

// Supported 表示本引擎是否支持镜像传输。
func (m *Manager) Supported() bool { return m.rtOK }

// LimitsView 返回当前限额（供协议回传给前端展示）。
func (m *Manager) LimitsView() Limits { return m.limits }

// Snapshot 是任务的对外只读副本。
//
// 为什么要副本：Task 在锁内会被持续修改，直接交出去会让调用方
// 读到"半新半旧"的字段组合（例如 bytes_done 已更新而 status 还没）。
type Snapshot struct {
	ID         string
	Operation  Operation
	Reference  string
	Status     Status
	Phase      string
	Message    string
	BytesDone  int64
	BytesTotal int64
	SHA256     string
	SizeBytes  int64
}

// snap 在锁内生成副本（调用方必须已持锁）。
func (t *Task) snap() Snapshot {
	return Snapshot{
		ID: t.ID, Operation: t.Operation, Reference: t.Reference,
		Status: t.Status, Phase: t.Phase, Message: t.Message,
		BytesDone: t.BytesDone, BytesTotal: t.BytesTotal,
		SHA256: t.SHA256, SizeBytes: t.SizeBytes,
	}
}

// errUnsupported 表示引擎不支持镜像传输。
var errUnsupported = errors.New("当前引擎不支持镜像传输")

// reservedBytesLocked 计算**已预留**的归档总量（调用方必须已持锁）。
//
// 三类占用相加：磁盘实存 + 各任务未落盘的声明余量 + 在写者的最坏增量。
// 只为 t.SizeBytes 记账是不够的——save 在写完前不知真实大小
// （inspect 给的是解压后大小），两个 save 会各自写到接近单归档上限。
// 因此每个仍在写的 writer 额外预留 (MaxArchiveBytes - SizeBytes)。
// 该估计偏严，但误拒只需重试，误放会写满宿主机磁盘。
func (m *Manager) reservedBytesLocked() int64 {
	onDisk := m.store.usage()
	reserved := onDisk

	for _, t := range m.tasks {
		// 归档已删且无 worker 存活 → 不再占用。
		if t.gone && t.writers == 0 {
			continue
		}
		if t.SizeBytes > 0 {
			if remain := t.SizeBytes - t.BytesDone; remain > 0 {
				reserved += remain
			}
		}
		// 在写者的最坏增量。已落盘部分由 onDisk 反映，不必再加。
		if t.writers > 0 && isWritingOp(t.Operation) {
			if headroom := m.limits.MaxArchiveBytes - t.SizeBytes; headroom > 0 {
				reserved += headroom
			}
		}
	}
	return reserved
}

// isWritingOp 判断该操作是否写传输临时区。
// pull 写的是引擎存储而非临时区，为它预留配额会白占空间。
func isWritingOp(op Operation) bool {
	return op == OpSave || op == OpLoad
}

// checkWriteBudget 核对"任务 t 再写 add 字节"是否突破总配额（每次写盘前调用）。
//
// 预检基于 SizeBytes，而 save 的真实大小在写完前未知，故需逐块兜底。
// 必须排除 t 自己，否则 t 的预留会自我否决，导出永远无法完成。
func (m *Manager) checkWriteBudget(t *Task, add int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var others int64
	for _, o := range m.tasks {
		if o == t || (o.gone && o.writers == 0) {
			continue
		}
		if o.SizeBytes > 0 {
			if remain := o.SizeBytes - o.BytesDone; remain > 0 {
				others += remain
			}
		}
		if o.writers > 0 && isWritingOp(o.Operation) {
			if headroom := m.limits.MaxArchiveBytes - o.SizeBytes; headroom > 0 {
				others += headroom
			}
		}
	}

	// store.usage() 与 t 的实时写入之间有滞后，这里再把 t.BytesDone 加一次
	// 留出余量：判断因此偏严（写超过半配额时可能误拒），但绝不偏松。
	total := m.store.usage() + others + t.BytesDone + add
	if total > m.limits.MaxTotalArchiveBytes {
		return ErrQuotaExceeded
	}
	return nil
}

// activeWorkersLocked 统计后台仍在干活的任务数（调用方必须已持锁）。
//
// 并发上限不能只看 !terminal()：任务 A 已 canceled 但 save 仍在写，
// 只看状态就会再多放 2 个写者进来。非终态**或** writers>0 都算占位。
func (m *Manager) activeWorkersLocked() int {
	n := 0
	for _, t := range m.tasks {
		if !t.Status.terminal() || t.writers > 0 {
			n++
		}
	}
	return n
}

// 注意：这里**不做磁盘配额检查**。配额判断要读磁盘（store.usage），
// 属于 IO，必须在锁外做；调用方先算好再进来。
func (m *Manager) acquireTaskLocked(op Operation, reference string) (*Task, error) {
	if active := m.activeWorkersLocked(); active >= m.limits.MaxConcurrentTasks {
		return nil, ErrTooManyTasks
	}
	now := m.now()
	ctx, cancel := context.WithCancel(context.Background())
	t := &Task{
		ID:        newTaskID(),
		Operation: op,
		Reference: reference,
		Status:    StatusPending,
		Phase:     string(op),
		createdAt: now,
		updatedAt: now,
		cancel:    cancel,
		ctx:       ctx,
		done:      make(chan struct{}),
		// ============================ 登记必须在这里，不能进 goroutine ============================
		//
		// 这里踩过一个真实竞态：原先在 go 语句**内部**才调 beginWrite，
		// 于是 acquireTask 返回与 goroutine 真正开始之间存在一个窗口。
		// 若此刻 Cancel 紧接着 Release，Release 看到的 writers==0
		// 就会把任务移出 map 并删目录；随后 goroutine 才启动、
		// 才把 writers 加到 1——但那个 Task 已经不在表里了。
		// 后果：
		//   - 它照常创建归档并写入（`dirFor` 会重新建出目录），
		//     这份占用**逃过了配额记账**（它不在 tasks 表里）；
		//   - 没人再能查询或取消它，进程退出前一直占着磁盘。
		//
		// 正确做法：在**创建任务、仍持锁**的这一刻就把 writers 置为 1。
		// 此后任何 Release 都会看到 writers>0 并等待，绝无窗口。
		// goroutine 结束时用 endWrite 归零（见各调用点）。
		writers: 1,
	}
	m.tasks[t.ID] = t
	return t, nil
}

// Status 返回任务当前快照。
func (m *Manager) Status(taskID string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[taskID]
	if !ok {
		return Snapshot{}, ErrTaskNotFound
	}
	return t.snap(), nil
}

// Close 停止管理器并清理本进程创建的全部临时归档。
//
// ============================ 为什么常驻进程也需要它 ============================
//
// 常驻 Agent 收到 SIGTERM 时走优雅退出。此时传输临时区里可能还留着
// save 成功但 Hub 没来得及拉走的归档（几百 MB 到几 GB）。
// 若不在这里清理，它们会一直占着磁盘，直到下次启动的
// cleanupOrphans 才被收走——而"下次启动"可能是几天后。
//
// 注意：这里**不等待**正在执行的任务自然结束，而是直接取消它们。
// 理由是退出的时间预算有限（systemd 默认 90 秒就会被 SIGKILL），
// 而一个 2GB 的 load 可能远超这个时间；与其让进程被强杀、
// 留下状态未知的引擎操作，不如主动取消，让引擎自己收拾。
// load 中途取消的镜像已经入库，这一点无法回滚（见 Cancel 的说明）。
func (m *Manager) Close() {
	m.mu.Lock()
	active := make([]*Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		if !t.Status.terminal() && t.cancel != nil {
			active = append(active, t)
		}
	}
	m.mu.Unlock()

	for _, t := range active {
		t.cancel()
	}

	// 给正在收尾的 goroutine 一点时间落状态，但绝不无限等。
	// 200ms 足够把"取消"写进任务字段；真正慢的是引擎侧，那不在我们掌控内。
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		running := 0
		for _, t := range m.tasks {
			if !t.Status.terminal() {
				running++
			}
		}
		m.mu.Unlock()
		if running == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// 把全部任务标记 gone 并移出表，然后删目录。
	// 用 cleanupOrphans 而不是逐个 removeTask：它本来就会跳过
	// 不符合任务 id 形态的目录，安全性最高，且与启动清理共用一套逻辑。
	m.mu.Lock()
	m.tasks = make(map[string]*Task)
	m.mu.Unlock()
	m.store.cleanupOrphans(nil)
}

// Cancel 取消任务。
//
// 对 load 方向，**取消不代表已回滚**：镜像层入库没有事务，
// 可能已经写入了部分镜像。调用方必须把这个边界原样告诉用户。
func (m *Manager) Cancel(taskID string) (bool, string, error) {
	m.mu.Lock()
	t, ok := m.tasks[taskID]
	if !ok {
		m.mu.Unlock()
		return false, "", ErrTaskNotFound
	}
	if t.Status.terminal() {
		msg := "任务已结束，无需取消"
		m.mu.Unlock()
		return false, msg, nil
	}
	cancel := t.cancel
	op := t.Operation
	t.updatedAt = m.now()
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	// ============================ 必须自己落终态 ============================
	//
	// 不能只调 cancel() 就算完。有的任务形态**没有任何 goroutine 在监听 ctx**：
	//
	//   - load 在 begin 之后、commit 之前：只有上传在推进，没有后台 goroutine；
	//   - 同理，任务卡在"等待上传"时取消，没有人会把状态改成 canceled。
	//
	// 于是界面上会出现"已取消"的提示，但 status 一直显示 running，
	// 用户会不断重试、不断积累任务（还占着配额）。
	//
	// 因此这里显式落终态。**保留已收到的字节数**：
	// 它说明"取消时进度到哪了"，对排查有用，且不影响任何配额判断
	// （终态任务的 SizeBytes 不再计入 reservedBytesLocked）。
	//
	// 若任务已有后台 goroutine 在跑（pull/save/load 阶段），
	// 它随后会看到 ctx 被取消并再次写一次终态——状态相同，幂等无害。
	m.mu.Lock()
	if !t.Status.terminal() {
		t.Status = StatusCanceled
		t.updatedAt = m.now()
		if op == OpLoad {
			// 关键边界：镜像层入库没有事务，取消无法回滚。
			t.Message = "导入已取消；注意镜像入库无法回滚，可能已写入部分镜像"
		} else {
			t.Message = "已取消"
		}
	}
	m.mu.Unlock()

	msg := "已取消"
	if op == OpLoad {
		msg = "已取消；注意镜像入库无法回滚，可能已写入部分镜像"
	}
	return true, msg, nil
}

// Release 释放任务（删除归档、移出任务表）。
//
// ============================ 为什么不直接 unlink ============================
//
// 释放流程是"删除归档 + 移出任务表"。若照直写：
//
//	delete(m.tasks, id)
//	m.store.removeTask(id)     ← 与并发的 chunk get / hash 校验撞车
//
// 会出现真实的竞态：一个正在读归档的 GetChunk 会拿到一个已被 unlink 的
// 文件描述符，在 POSIX 上它**还能继续读到内容**（直到关闭），
// 于是 Hub 收到"看似正常"的数据——但它已经不在磁盘上，
// 下一次重试就会 404。数据流断裂的位置完全不可预测。
//
// 因此这里做两件事：
//  1. 先把任务标记 gone（此后任何新的读写立即失败，语义明确）；
//  2. 等当前在飞的读全部结束（readers 归零）再删文件。
//
// 等待用**轮询而非条件变量**：临界区只有"递减计数"，等待时间在
// 微秒到毫秒级，条件变量带来的唤醒复杂度不值得。轮询还有上限，
// 绝不会因为某个 reader 忘了递减而永久卡住整个 release。
func (m *Manager) Release(taskID string) (bool, string, error) {
	m.mu.Lock()
	t, ok := m.tasks[taskID]
	if !ok {
		m.mu.Unlock()
		return false, "", ErrTaskNotFound
	}
	// ============================ 判据必须是"worker 已退出" ============================
	//
	// 不能只看 Status.terminal()。Cancel 会把状态立刻写成 canceled，
	// 但后台 save/load goroutine 可能还在写盘。此时若放行 release，
	// 就会 unlink 一个**正在被写**的文件：
	//   - POSIX 上写入不会报错（文件还有打开的句柄），
	//     于是 save goroutine 一直"写成功"，而内容永远不可见；
	//   - 更糟的是配额被提前释放，后续任务可以再占同样的空间，
	//     磁盘实际用量因此突破上限。
	//
	// 所以这里同时检查状态与 writers：
	//   - 状态非终态 → 拒绝（正常时序，返回 released=false 而非报错）；
	//   - 状态终态但 writers > 0 → **等待**它退出（不是拒绝）：
	//     这时任务确实已经收尾，只是最后几步还没跑完，
	//     让 release 等一下比让用户重试更符合预期。
	if !t.Status.terminal() {
		msg := "任务仍在执行，暂时无法释放"
		m.mu.Unlock()
		return false, msg, nil
	}
	busy := t.writers > 0
	m.mu.Unlock()

	// 等 worker 真正退出（事件等待，非固定 sleep）。
	// 超时就如实失败，绝不谎称已释放。
	if busy && !m.waitWritersIdle(t, 10*time.Second) {
		return false, "归档仍被写入任务占用，请稍后重试", errors.New("写入任务尚未退出")
	}

	m.mu.Lock()
	if t.gone {
		// 并发 release 或清扫器已经处理过：幂等返回成功。
		m.mu.Unlock()
		return true, "已释放", nil
	}
	t.gone = true
	delete(m.tasks, taskID)
	m.mu.Unlock()

	if err := m.removeArchiveWhenIdle(t); err != nil {
		return false, "删除归档失败，请稍后重试", err
	}
	return true, "已释放", nil
}

// removeArchiveWhenIdle 等该任务的在飞读结束，然后删除归档目录。
//
// 轮询上限 2 秒：一次 chunk get 只有 256 KiB，即便正在写哈希也远低于此。
// 超时就放弃删除并返回错误，让调用方如实报告——
// 宁可留一份垃圾等清扫器处理，也不要谎称"已释放"。
func (m *Manager) removeArchiveWhenIdle(t *Task) error {
	const (
		step    = 2 * time.Millisecond
		maxWait = 2 * time.Second
	)
	waited := time.Duration(0)
	for {
		m.mu.Lock()
		n := t.readers
		m.mu.Unlock()
		if n == 0 {
			break
		}
		if waited >= maxWait {
			return errors.New("归档仍被读取占用，稍后重试")
		}
		time.Sleep(step)
		waited += step
	}
	return m.store.removeTask(t.ID)
}

// beginWrite 登记一次写操作（save 落盘 / load 引擎调用 / pull）。
//
// 必须与 endWrite 配对（用 defer endWrite）。配对的收尾里会关闭 done，
// 从而让 Release 确切知道"这个任务再也不会动文件了"。
func (m *Manager) beginWrite(t *Task) {
	m.mu.Lock()
	t.writers++
	m.mu.Unlock()
}

// endWrite 结束一次写操作；最后一次结束时关闭 done。
func (m *Manager) endWrite(t *Task) {
	m.mu.Lock()
	if t.writers > 0 {
		t.writers--
	}
	last := t.writers == 0
	m.mu.Unlock()
	if last {
		t.doneOnce.Do(func() {
			if t.done != nil {
				close(t.done)
			}
		})
	}
}

// waitWritersIdle 等到该任务没有任何在飞的写操作。
//
// ============================ 为什么不能只等状态 ============================
//
// 状态是"事后宣布"的：一个 save goroutine 可能已经把 Status 写成
// succeeded，但它的 defer 还在跑（关文件、删半截归档）。
// 此时若 Release 立刻去删同一个目录，就会与那个 defer 撞车——
// 结果是"删除失败但状态已经报成功"，而且失败原因完全看不出来。
//
// 这里等的是 done 通道（一次性、由最后一次 endWrite 关闭），
// 因此是**真正的事件等待**，不是轮询 sleep。
//
// 超时兜底 2 秒：正常操作的收尾是毫秒级。真超时就如实返回错误，
// 让调用方报告"归档仍被占用"，绝不谎称已释放。
func (m *Manager) waitWritersIdle(t *Task, timeout time.Duration) bool {
	m.mu.Lock()
	done := t.done
	busy := t.writers > 0
	m.mu.Unlock()
	if !busy {
		return true
	}
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// 必须与 endRead 配对（用完就 defer endRead），否则 release 会一直等。
func (m *Manager) beginRead(taskID string) (*Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[taskID]
	if !ok || t.gone {
		return nil, false
	}
	t.readers++
	return t, true
}

// endRead 结束一次归档读取登记。
func (m *Manager) endRead(t *Task) {
	m.mu.Lock()
	if t.readers > 0 {
		t.readers--
	}
	m.mu.Unlock()
}

// Sweep 清理过期任务。由后台定时器调用。
//
// 三类对象分别处理：
//  1. 超过总超时仍未结束的 → 取消并标记失败；
//  2. 终态且超过各自 TTL 的 → 删除归档并移出任务表；
//  3. 其它 → 不动。
func (m *Manager) Sweep() int {
	now := m.now()
	var expired []*Task

	m.mu.Lock()
	for id, t := range m.tasks {
		if !t.Status.terminal() {
			if now.Sub(t.createdAt) > TaskTimeout {
				if t.cancel != nil {
					t.cancel()
				}
				t.Status = StatusFailed
				t.Message = "任务超时（超过 30 分钟）"
				t.updatedAt = now
			}
			continue
		}
		ttl := SaveArchiveTTL
		if t.Operation == OpLoad {
			ttl = LoadBeginTTL
		}
		if now.Sub(t.updatedAt) > ttl {
			t.gone = true
			delete(m.tasks, id)
			expired = append(expired, t)
		}
	}
	m.mu.Unlock()

	// 文件删除在锁外做，且同样要等在飞读结束——
	// "过期清理"与"正在被读"撞车的后果与 Release 里描述的完全一样。
	for _, t := range expired {
		_ = m.removeArchiveWhenIdle(t)
	}
	return len(expired)
}
