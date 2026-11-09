package compose

// lock.go 提供**项目级互斥**：同一编排项目上的操作串行执行。
//
// ============================ 为什么需要它 ============================
//
// 一个项目上的操作不是原子的，而且彼此会互相踩：
//
//  1. **写侧互踩**。CommitProject 用"临时文件 + rename"提交两个文件。
//     若两个请求同时写同一项目，临时文件会被对方当成"上次崩溃的残留"删掉——
//     于是 A 正在写的内容消失，**而 A 已经报告了成功**。
//     这种"报告成功但磁盘上是别人的内容"会让哈希与磁盘永久不一致。
//
//  2. **读侧撕裂**。提交是两个 rename（compose.yml 再 .env）。
//     List/Status 若在两次 rename 之间读，会看到「新 compose.yml + 旧 .env」——
//     一个我们从未校验过的组合，界面上显示成"配置已偏离"。
//
//  3. **配置在飞行中被换掉**。`up` 是长命令（拉镜像可能十分钟）。
//     若在它执行期间另一个请求改了配置，compose 读到的可能是新旧混合的状态，
//     而两个请求都会各自报告成功。
//
// ============================ 为什么不用 sync.Mutex ============================
//
// sync.Mutex 的 Lock **不响应 context**。这带来两个真实故障：
//
//   - **请求已取消，动作照样执行**。用户点了取消、或 Hub 侧 15 秒超时返回，
//     但那个 goroutine 还卡在 Lock 上；等锁一放开，它会**继续往下执行**
//     up/down——用户以为"超时了什么都没发生"，实际几秒后服务被重建。
//     这是最危险的一种：取消语义变成了一句空话。
//
//   - **读侧被长任务拖死**。up 可能跑十分钟，此时 List 若排在同一个锁后面，
//     整个编排列表要等到 up 结束才能返回，最终撞上 15 秒超时，
//     界面表现成"列表加载失败"，而实际上是我们在等一把不该等的锁。
//
// 因此这里用**容量为 1 的 channel 当信号量**，取锁走 ctx select：
//   - 等锁期间 ctx 取消 → 立刻放弃，**绝不执行动作**；
//   - 读侧可以"试一试，拿不到就跳过"（见 tryWithProject），
//     让 List 对正在忙的项目返回 busy 而不是整表阻塞。
//
// ============================ 边界（不可宣称超出） ============================
//
// 这是**进程内**锁，只在同一个 Agent 进程内生效。
// 它**不能**保证跨进程互斥：如果用户同时运行本地 CLI（arkade-agent）
// 和后台常驻的 Agent 对同一项目写文件，仍然可能交错。
// 要走跨进程互斥需要文件锁（flock/LockFileEx），
// 但那会引入"锁文件残留导致永久卡死"这类更难处理的问题，
// 因此当前版本**明确不做跨进程保证**，只保证"同一个 Agent 内绝不冲突"。
//
// 相应地，临时文件名也必须唯一（见 Workspace.commitTempName），
// 这样即使真的发生跨进程并发，也不会互相删除对方的临时文件——
// 我们不承诺结果正确，但绝不能主动制造破坏。

import (
	"context"
	"sync"
)

// projectLocks 是按项目名的信号量表。
//
// 为什么按名字分锁而不是一把大锁：不同项目之间没有任何冲突
// （文件路径、compose 项目名都不同），一把大锁会让"给 A 项目 up"
// 阻塞"给 B 项目备份"，界面上表现为莫名其妙的整体卡顿。
//
// 为什么锁对象不删除：项目数量是几十的量级，容量为 1 的 channel 极小，
// 而"删锁"要判断"当前没人持有且没人等待"，那本身又是一个竞态源。
type projectLocks struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
}

// semaphoreFor 返回某个项目对应的信号量（必要时创建）。
func (p *projectLocks) semaphoreFor(name string) chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.locks == nil {
		p.locks = make(map[string]chan struct{})
	}
	sem, ok := p.locks[name]
	if !ok {
		sem = make(chan struct{}, 1)
		p.locks[name] = sem
	}
	return sem
}

// errLockCanceled 表示"等锁期间上下文结束，动作未执行"。
//
// 单独一个错误类型而不是笼统的 ctx.Err()：调用方需要能区分
// "动作执行到一半超时"和"动作根本没开始"。前者可能已经改了磁盘/容器，
// 后者保证什么都没发生——界面上这两种提示完全不同。
type errLockCanceled struct{ err error }

func (e errLockCanceled) Error() string {
	return "等本项目其它操作结束的过程中请求已取消或超时，本次操作未执行"
}

func (e errLockCanceled) Unwrap() error { return e.err }

// withProject 在项目锁保护下执行 fn，等锁期间响应 ctx。
//
// ctx 在等到锁之前结束 → 返回 errLockCanceled，且**绝不调用 fn**。
// 这条保证是"取消"语义的实质：不是提示一句"已取消"然后偷偷执行。
//
// 用法约定（防止死锁的唯一规则）：
// **持锁期间只能调用本包的 private helper，不得调用任何其它公开方法**
// ——公开方法自己也会取锁，容量为 1 的信号量不可重入，嵌套会直接卡死。
func (m *Manager) withProject(ctx context.Context, name string, fn func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	sem := m.locks.semaphoreFor(name)

	// 先试一次非阻塞获取：绝大多数情况没有竞争，省掉一次 select 开销。
	select {
	case sem <- struct{}{}:
	default:
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return errLockCanceled{err: ctx.Err()}
		}
	}
	defer func() { <-sem }()

	// 拿到锁之后再检查一次 ctx：可能在上面的瞬间它已经结束了。
	// 不检查的话，就会出现"锁到手时请求已超时，却照样执行 up/down"。
	if err := ctx.Err(); err != nil {
		return errLockCanceled{err: err}
	}
	return fn()
}

// tryWithProject 尝试**不等待**地拿项目锁；拿不到就返回 ok=false。
//
// 专供读侧（List）使用：某个项目正在执行长任务时，
// 列表不该为它整体阻塞到超时，而是把它标成 busy、其余项目照常返回。
func (m *Manager) tryWithProject(name string, fn func()) (ok bool) {
	sem := m.locks.semaphoreFor(name)
	select {
	case sem <- struct{}{}:
	default:
		return false
	}
	defer func() { <-sem }()
	fn()
	return true
}
