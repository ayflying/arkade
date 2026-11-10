package compose

// pullbuffer.go：任务的输出缓冲（有界、单调 seq、增量快照）。
//
// 单独一个文件：缓冲的边界规则（上限、丢最旧、seq 跳号语义）
// 是这一整套功能里最容易被改错的地方，集中一处便于审查。

import (
	"sync"
)

type taskSink struct {
	task *PullTask
	red  *redactor
	// mu 串行化 stdout/stderr 两路回调，保证 seq 顺序就是写入顺序。
	mu sync.Mutex
}

func (s *taskSink) Stdout(line string) { s.push("stdout", line) }
func (s *taskSink) Stderr(line string) { s.push("stderr", line) }

func (s *taskSink) push(stream, line string) {
	// 脱敏在锁外做（纯 CPU，不依赖任务状态），但要保证顺序，
	// 因此整体仍用一个 sink 级互斥锁。
	s.mu.Lock()
	defer s.mu.Unlock()

	text := sanitizeLine(line, s.red)
	s.task.appendEvent(stream, text)
}

// appendEvent 追加一行输出（有界）。
func (t *PullTask) appendEvent(stream, text string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status != PullRunning {
		return
	}
	t.nextSeq++
	ev := PullEvent{Seq: t.nextSeq, Stream: stream, Text: text}
	t.events = append(t.events, ev)
	t.totalBytes += len(text)

	// 有界：超限丢弃**最旧**的（保留尾部，因为失败原因总在末尾）。
	for len(t.events) > 0 && (len(t.events) > maxPullEvents || t.totalBytes > maxPullBytes) {
		t.totalBytes -= len(t.events[0].Text)
		t.events = t.events[1:]
		t.truncated = true
	}
}

// snapshot 生成快照，只含 seq > after 的输出。
func (t *PullTask) snapshot(after int64) PullSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()

	snap := PullSnapshot{
		TaskID:     t.id,
		Name:       t.name,
		Command:    t.command,
		WorkingDir: t.workingDir,
		Status:     t.status,
		NextCursor: t.nextSeq,
		Truncated:  t.truncated,
		Error:      t.errText,
		StartedAt:  t.startedAt,
		FinishedAt: t.finishedAt,
	}
	if t.exitCode != nil {
		ec := *t.exitCode
		snap.ExitCode = &ec
	}
	// 增量：只返回游标之后的行。
	for _, ev := range t.events {
		if ev.Seq > after {
			snap.Output = append(snap.Output, ev)
		}
	}
	return snap
}

// sweepLocked 清理过期任务（调用方必须持 p.mu）。
