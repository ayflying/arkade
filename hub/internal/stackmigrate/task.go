package stackmigrate

// task.go：迁移任务的模型与事件缓冲。
//
// ============================ 与 compose 操作任务的同构关系 ============================
//
// 事件结构（Seq/Stream/Text）、状态机（queued/running/succeeded/failed）、
// 增量游标（after）都与 compose 编排操作任务一致，前端因此零改动复用弹窗。
//
// 差异只有两处，且都是必要的：
//
//	1. 任务表在 Hub 而非 Agent —— 迁移的全局进度只可能存在于 Hub；
//	2. 多了"最远推进到的阶段"与"目标端残留"两个字段 —— 失败时
//	   用户必须知道目标上留下了什么，否则无法决定是重试还是手工清理。
//
// ============================ 锁纪律 ============================
//
//	Task.mu     保护状态与事件缓冲，**绝不在持锁时做 IO 或 RPC**
//	Manager.mu  保护任务表与"每对服务器只有一个迁移"的占位
//
// 顺序严格单向：先做 IO，再取 Task.mu 更新结果。
// 反过来会让一个慢网络拖死所有任务的日志读取。

import (
	"errors"
	"sync"
	"time"
)

// 任务限额。
const (
	// MaxTasks 是最多保留的迁移任务数（含已结束的）。
	MaxTasks = 16
	// TaskTTL 是任务的保留时长。
	//
	// 30 分钟：迁移比单次编排操作长得多，用户可能过一会儿才回来看结果，
	// 但也不该让内存长期占着（每任务上限 256 KiB 事件）。
	TaskTTL = 30 * time.Minute
	// maxEvents 是单任务保留的最大事件行数。
	maxEvents = 2000
	// maxEventBytes 是单任务保留的最大事件字节数。
	maxEventBytes = 256 << 10
)

// 任务状态取值（与 compose 操作任务完全一致）。
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

// 事件流取值（与 compose 操作任务完全一致）。
//
// ============================ 这里只用 stage 与 stderr ============================
// 迁移不打 command 事件：它执行的不是"某一条命令"，而是跨两台机器的
// 一组 RPC。若伪造一条 command 事件，前端会渲染成可复制执行的样式，
// 而用户复制去跑只会得到"命令不存在"——那是在骗用户。
//
// 失败信息用 stderr：前端对 stderr 的样式是"红色错误行"，
// 正符合用户对失败原因的预期。
const (
	StreamStage  = "stage"
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// 领域错误。
var (
	// ErrTaskNotFound 任务不存在或已过期。
	ErrTaskNotFound = errors.New("迁移任务不存在或已过期")
	// ErrTooManyTasks 任务数达上限。
	ErrTooManyTasks = errors.New("同时进行的迁移任务已达上限，请稍后再试")
	// ErrPairBusy 同一对服务器已有迁移在跑。
	ErrPairBusy = errors.New("该服务器组合已有迁移正在进行，请等待它结束")
	// ErrDuplicateRequest 同一 request_id 已用于不同的迁移。
	ErrDuplicateRequest = errors.New("该请求标识已用于另一项迁移")
)

// Event 是一条任务事件。
type Event struct {
	Seq    int64
	Stream string
	Text   string
}

// Residual 描述目标端在失败时留下的东西。
//
// ============================ 为什么必须如实记录 ============================
// 迁移失败后目标端可能已经有：导入的镜像、恢复了一半的卷、写好的配置。
// 用户需要据此决定"重试"还是"先去目标上清理"。
// 若什么都不说，用户重试时会遇到"同名冲突"或"卷里已经有一半数据"，
// 而这两个现象看起来都像是新问题。
type Residual struct {
	// ImagesImported 是已导入目标端的镜像数。
	ImagesImported int
	// VolumesRestored 是已恢复到目标端的卷数。
	VolumesRestored int
	// ComposeWritten 表示配置是否已写到目标端。
	ComposeWritten bool
	// Started 表示是否已在目标端启动过。
	Started bool
}

// Empty 表示目标端没有任何残留。
func (r Residual) Empty() bool {
	return r.ImagesImported == 0 && r.VolumesRestored == 0 && !r.ComposeWritten && !r.Started
}

// Describe 生成中文描述（给失败原因用）。
func (r Residual) Describe() string {
	if r.Empty() {
		return "目标服务器上未留下任何改动"
	}
	var parts []string
	if r.ImagesImported > 0 {
		parts = append(parts, "已导入 "+itoa(r.ImagesImported)+" 个镜像")
	}
	if r.VolumesRestored > 0 {
		parts = append(parts, "已恢复 "+itoa(r.VolumesRestored)+" 个数据卷")
	}
	if r.ComposeWritten {
		parts = append(parts, "已写入编排配置")
	}
	if r.Started {
		parts = append(parts, "已在目标上启动")
	}
	return "目标服务器上已留下：" + joinComma(parts)
}

// Task 是一次迁移任务。
type Task struct {
	id string
	// Name 是源端编排名（前端按它匹配弹窗）。
	Name string
	// RequestID 是幂等键。
	RequestID string
	// Source 是源 Agent 标识。
	Source string
	// Target 是目标 Agent 标识。
	Target string
	// IncludeVolumes 表示本次是否迁移数据卷。
	IncludeVolumes bool
	// StartAfter 表示迁移完成后是否启动。
	StartAfter bool
	// Enabled 是本次迁移是否选择执行（false 表示只校验并报告计划）。
	Enabled bool

	mu sync.Mutex
	// status 取值见 Status*。
	status    string
	events    []Event
	nextSeq   int64
	bytes     int
	truncated bool
	errText   string
	// stage 是**最远推进到**的阶段（失败时用它回答"停在哪"）。
	stage string
	// residual 是目标端残留。
	residual Residual
	// 统计（审计与结果文案用）。
	imageCount  int
	volumeCount int
	bytesCopied int64
	// skippedVolumes 是本次因未勾选而跳过的卷数。
	skippedVolumes int
	// bindDirs 是未迁移的宿主机绑定目录数。
	bindDirs int
	// startedAt/finishedAt/createdAt 是时间戳。
	createdAt  time.Time
	startedAt  time.Time
	finishedAt time.Time
}

// Snapshot 是任务的一次快照（协议层的输入）。
type Snapshot struct {
	TaskID         string
	Name           string
	Source         string
	Target         string
	IncludeVolumes bool
	StartAfter     bool
	Status         string
	Events         []Event
	NextCursor     int64
	Truncated      bool
	Error          string
	// Stage 是最远推进到的阶段（失败时用来回答"停在哪一步"）。
	Stage string
	// StageTitle 是该阶段的中文名。
	StageTitle string
	// Residual 是目标端残留。
	Residual Residual
	// 统计。
	ImageCount     int
	VolumeCount    int
	SkippedVolumes int
	BindDirs       int
	BytesCopied    int64

	CreatedAt  time.Time
	StartedAt  time.Time
	FinishedAt time.Time
}

// appendEvent 追加一条事件。
//
// ============================ 缓冲上限的处理 ============================
// 超出上限时**丢弃最早的事件**并置 truncated=true。
// 保尾部而不是保头部：迁移失败的原因总在最后几行，
// 头部只是"开始校验"之类的固定开场。
func (t *Task) appendEvent(stream, text string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.nextSeq++
	ev := Event{Seq: t.nextSeq, Stream: stream, Text: text}
	t.events = append(t.events, ev)
	t.bytes += len(text)

	for len(t.events) > maxEvents || (t.bytes > maxEventBytes && len(t.events) > 1) {
		t.bytes -= len(t.events[0].Text)
		t.events = t.events[1:]
		t.truncated = true
	}
}

// setStage 记录最远推进到的阶段。
func (t *Task) setStage(stage string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// 只前进不回退：finish 之前的阶段一旦走完就不该被后续步骤覆盖，
	// 否则失败报告会指向一个"更早"的阶段。
	if StageIndex(stage) >= StageIndex(t.stage) {
		t.stage = stage
	}
}

// setResidual 记录目标端残留。
func (t *Task) setResidual(r Residual) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.residual = r
}

// snapshot 生成快照，events 只返回 seq > after 的部分。
func (t *Task) snapshot(after int64) Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := Snapshot{
		TaskID: t.id, Name: t.Name, Source: t.Source, Target: t.Target,
		IncludeVolumes: t.IncludeVolumes, StartAfter: t.StartAfter,
		Status: t.status, NextCursor: t.nextSeq, Truncated: t.truncated,
		Error: t.errText, Stage: t.stage, StageTitle: StageTitle(t.stage),
		Residual: t.residual, ImageCount: t.imageCount, VolumeCount: t.volumeCount,
		SkippedVolumes: t.skippedVolumes, BindDirs: t.bindDirs, BytesCopied: t.bytesCopied,
		CreatedAt: t.createdAt, StartedAt: t.startedAt, FinishedAt: t.finishedAt,
	}
	for _, ev := range t.events {
		if ev.Seq > after {
			out.Events = append(out.Events, ev)
		}
	}
	return out
}

// terminal 判断状态是否终态。
func terminal(status string) bool {
	return status == StatusSucceeded || status == StatusFailed
}

// itoa 是 strconv.Itoa 的本地别名（避免在多个文件重复导入 strconv）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// joinComma 用顿号连接中文列表。
func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "、"
		}
		out += p
	}
	return out
}
