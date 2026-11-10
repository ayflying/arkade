package compose

// observer.go：把"真实命令与其输出"广播给异步任务。
//
// ============================ 为什么挂在 ctx 上 ============================
//
// 编排操作的实际执行路径是 Manager 的八个方法（ApplyProject/Down/Stop/
// Restart/Pull/Rebuild/Remove/Backup），它们内部各自加项目锁、各自校验，
// 而且**在外层再套锁就会死锁**（它们自己调 withProject）。
//
// 若把观察者做成参数，就要逐个改这八个方法的签名，还会传染到所有调用点；
// 挂在 ctx 上则完全无侵入：任务层只需 withObserver(ctx, ob)，
// 底层 Runner 与 backup 的裸 exec 都能从 ctx 取到同一个 sink。
//
// ============================ 必须在 Runner.Run 处捕获 ============================
//
// 只在 Manager.run 里挂钩会漏掉两类真实命令：
//   - ApplyProject 内部的 precheck 直接走 Runner.Run；
//   - backup 的 tar / volume ls 是裸 exec.CommandContext，
//     根本不经过 Manager.run。
//
// 因此观察点放在**最底层的实际 exec 处**，而不是某个容易漏的中间层。

import (
	"context"
	"strings"
	"sync"
)

// observerKey 是 ctx 里存放观察者的键（未导出的空结构体，外部无法伪造）。
type observerKey struct{}

// sinkFn 是一行输出的消费者。
type sinkFn func(string)

// executionObserver 收集任务执行期间的真实命令与输出。
//
// 并发安全：stdout/stderr 两路 goroutine 会并发回调，因此内部统一加锁。
type executionObserver struct {
	mu sync.Mutex
	// onCommand 在**每次**实际 exec 之前调用。
	//
	// 参数是**真实执行用的 Runner**，而不是任务自己的 workspace 推断：
	//   - up 的 precheck 在临时目录执行，且携带**新** spec 写出的 .env；
	//   - 任务若只读最终 workspace，是拿旧值去遮新密码——新 secret 会漏。
	//
	// 因此把 r 交出去，让调用方用 buildRedactorWith(r) 拿到
	// "这条命令真正会用的那份配置"。
	onCommand func(argv []string, r *Runner)
	// onStdout / onStderr 在输出到达时调用（已按行切分）。
	onStdout func(string)
	onStderr func(string)
	// onStage 用于"没有单一 argv 可展示"的步骤（如文件复制）。
	//
	// 同样携带真实 Runner：阶段发生在哪个目录，用户有权知道真实值，
	// 报成最终 workspace 就是虚报。
	onStage func(name string, r *Runner)
}

// withObserver 把观察者放进 ctx。
func withObserver(ctx context.Context, ob *executionObserver) context.Context {
	return context.WithValue(ctx, observerKey{}, ob)
}

// observerFrom 从 ctx 取出观察者（可能为 nil，表示不在任务里执行）。
func observerFrom(ctx context.Context) *executionObserver {
	if ctx == nil {
		return nil
	}
	ob, _ := ctx.Value(observerKey{}).(*executionObserver)
	return ob
}

// command 上报一次真实命令（附真实 Runner）。
func (o *executionObserver) command(argv []string, r *Runner) {
	if o == nil || o.onCommand == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.onCommand(argv, r)
}

// stdout / stderr 上报输出行。
func (o *executionObserver) stdout(line string) {
	if o == nil || o.onStdout == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.onStdout(line)
}

func (o *executionObserver) stderr(line string) {
	if o == nil || o.onStderr == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.onStderr(line)
}

// splitLines 按换行切分并去掉尾部空行。
//
// 供"命令结束后才有输出"的场景使用（backup 的 docker run 用的是
// CombinedOutput，拿不到逐行实时输出，只能在结束时按行补发）。
func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

// stage 上报一个"明确阶段"（没有单一 argv 可如实展示时使用）。
//
// ============================ 为什么需要它 ============================
//
// backup 的某些步骤（复制编排文件、统计卷列表）没有一条"真实命令"可展示。
// 编造一条假命令会误导用户去复制执行，因此只报阶段名，
// 由前端显示为进度标记而不是可执行命令。
func (o *executionObserver) stage(name string, r *Runner) {
	if o == nil || o.onStage == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.onStage(name, r)
}
