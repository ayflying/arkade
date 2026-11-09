package agentlink

// credential_lock.go 实现握手与撤销的互斥临界区（EnrollmentGuard）。
//
// 设计理由见 generation.go 的包注释。这里强调三条实现约束：
//
//  1. **临界区内不做网络 I/O**。写 HelloAck 在临界区之外，
//     否则一个慢客户端会把整个系统的握手全部卡住。
//  2. **临界区内只做内存与短数据库操作**（毫秒级），
//     这样全局串行的代价可以忽略。
//  3. **加锁必须带超时**。否则一旦某个临界区异常不释放，
//     后续所有握手会永久挂起，表现为"所有 Agent 突然都连不上"
//     且日志里没有任何错误——这类静默挂起极难排查。
//
// 两类操作走同一把锁：
//
//	RunEnrollment  —— 握手：重新校验 + 写库 + 登记，一个原子动作
//	RunRevocation  —— reset / delete / disable：写库 + 断开，一个原子动作

import (
	"context"
	"errors"
	"time"
)

// defaultGuardTimeout 是等待进入临界区的最长时间。
//
// 握手本身有 10 秒预算，而临界区是毫秒级操作。
// 等 5 秒仍拿不到锁，说明临界区里有慢操作（设计违规），
// 此时快速失败比继续等待更有价值。
const defaultGuardTimeout = 5 * time.Second

// ErrGuardBusy 表示等待临界区超时。
var ErrGuardBusy = errors.New("系统繁忙（认证临界区等待超时），请稍后重试")

// EnrollmentGuard 把"握手"与"撤销"串行化。
//
// 零值不可用，必须用 NewEnrollmentGuard 构造。
//
// 全局一把锁是**有意的选择**：正确性比吞吐重要得多，
// 而两个操作都是低频的（每台机器握手一次、管理员偶尔重置一次）。
// 按 agent 分锁会引入"撤销方拿 A 的锁、握手方拿 B 的锁"这类推理负担，
// 收益不足以抵消复杂度。
type EnrollmentGuard struct {
	// sem 是容量 1 的信号量。
	//
	// 用带缓冲 channel 而不是 sync.Mutex，是因为需要**带超时的等待**：
	// mutex 只有非阻塞的 TryLock，"等一会儿再放弃"用 channel 表达最直接，
	// 也不会出现"goroutine 拿着锁无法归还"的错位。
	sem chan struct{}

	// timeout 是等待进入临界区的上限。
	timeout time.Duration
}

// NewEnrollmentGuard 构造守卫。
func NewEnrollmentGuard() *EnrollmentGuard {
	return &EnrollmentGuard{
		sem:     make(chan struct{}, 1),
		timeout: defaultGuardTimeout,
	}
}

// NewEnrollmentGuardWithTimeout 构造自定义等待上限的守卫。
//
// 供测试使用：测"等待超时会失败而不是永久挂起"时，
// 用默认的 5 秒会让测试变慢且不稳定。
func NewEnrollmentGuardWithTimeout(d time.Duration) *EnrollmentGuard {
	return &EnrollmentGuard{
		sem:     make(chan struct{}, 1),
		timeout: d,
	}
}

// RunEnrollment 在临界区内执行握手侧的"校验 + 写库 + 登记"。
//
// fn 里必须完成：最终凭据校验、OnAgentHello、Registry.Add。
// 返回的 error 原样透传给调用方（通常是拒握手的原因）。
func (g *EnrollmentGuard) RunEnrollment(ctx context.Context, fn func() error) error {
	return g.run(ctx, fn)
}

// RunRevocation 在临界区内执行撤销侧的"写库 + 断开"。
//
// fn 里必须完成：数据库修改（换/删摘要、置禁用、删记录）+ Registry.Close。
// 建议先写库后 Close —— 写库成功后再断开才有意义。
func (g *EnrollmentGuard) RunRevocation(ctx context.Context, fn func() error) error {
	return g.run(ctx, fn)
}

// run 是两条路径共用的加锁执行逻辑。
func (g *EnrollmentGuard) run(ctx context.Context, fn func() error) error {
	if err := g.acquire(ctx); err != nil {
		return err
	}
	// defer 保证 panic 时也归还信号量。
	defer func() { <-g.sem }()

	return fn()
}

// acquire 带超时地占用信号量。
//
// 三条路径：
//   - 成功拿到 → 调用方负责归还；
//   - context 取消 / 超时 → 立刻返回错误，**没有占用**任何资源，
//     因此不会出现"归还一个没拿到的信号量"的错位。
func (g *EnrollmentGuard) acquire(ctx context.Context) error {
	select {
	case g.sem <- struct{}{}:
		// 拿到。
		return nil
	default:
	}

	timer := time.NewTimer(g.timeout)
	defer timer.Stop()

	select {
	case g.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ErrGuardBusy
	}
}
