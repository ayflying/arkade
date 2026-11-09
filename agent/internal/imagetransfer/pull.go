package imagetransfer

// pull.go 实现镜像拉取。
//
// 拉取是"只读外部 + 写引擎"的操作：不碰传输临时区，因此不受归档配额约束；
// 但仍受并发任务上限约束（每个 pull 都会占用引擎的下载带宽与镜像锁）。

import (
	"context"
	"errors"
	"strings"

	"arkade/agent/internal/runtime"
)

// Pull 启动一次拉取，立即返回任务快照。
//
// 立即返回而不是等完成：拉取可能要几分钟，让 RPC 从头等到尾会撞上
// Hub 的请求超时；而超时之后 Agent 其实还在跑——
// 用户以为失败，几秒后镜像却被换了。
func (m *Manager) Pull(ctx context.Context, reference, platform, digest string) (Snapshot, error) {
	if !m.rtOK {
		return Snapshot{}, errUnsupported
	}

	m.mu.Lock()
	t, err := m.acquireTaskLocked(OpPull, reference)
	if err != nil {
		m.mu.Unlock()
		return Snapshot{}, err
	}
	t.Status = StatusRunning
	t.Phase = "pulling"
	snap := t.snap()
	taskCtx, cancel := t.ctx, t.cancel
	m.mu.Unlock()

	go func() {
		defer cancel()
		// writers 已在 acquireTaskLocked 里置为 1（创建即登记，
		// 消除"启动前被 release"的窗口），这里只负责归零。
		defer m.endWrite(t)
		// runPull 里所有字段更新都自己取锁，这里不再持锁。
		m.runPull(taskCtx, t, reference, platform, digest)
	}()

	return snap, nil
}

// runPull 执行拉取并落状态。
func (m *Manager) runPull(taskCtx context.Context, t *Task,
	reference, platform, digest string) {

	// 进度回调在 runtime 的读循环里被高频调用：只做"记录数字"，
	// 不做 IO、不写日志。
	onProgress := func(p runtime.PullProgress) {
		m.mu.Lock()
		t.BytesDone = p.BytesDone
		t.BytesTotal = p.BytesTotal
		if p.Status != "" {
			t.Phase = p.Status
		}
		t.updatedAt = m.now()
		m.mu.Unlock()
	}

	// 总超时：即使 Hub 一直轮询，也要有一个兜底上限。
	runCtx, runCancel := context.WithTimeout(taskCtx, TaskTimeout)
	defer runCancel()

	res, perr := m.rt.PullImage(runCtx, reference, platform, digest, onProgress)

	m.mu.Lock()
	defer m.mu.Unlock()
	t.updatedAt = m.now()

	switch {
	case perr == nil:
		t.Status = StatusSucceeded
		t.Phase = "done"
		t.Reference = res.Reference
		t.SHA256 = res.Digest
		if t.BytesTotal > 0 {
			t.BytesDone = t.BytesTotal
		}
		t.Message = "镜像拉取完成"
	case errors.Is(taskCtx.Err(), context.Canceled):
		t.Status = StatusCanceled
		t.Message = "拉取已取消"
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		t.Status = StatusFailed
		t.Message = "拉取超时（超过 30 分钟）"
	default:
		t.Status = StatusFailed
		// 用固定文案，**不转发引擎原文**。
		t.Message = pullFailureMessage(perr)
	}
}

// pullFailureMessage 把拉取错误翻成固定的中文说明。
//
// 分类依据是 runtime 层已脱敏过的错误（sanitizeEngineError 只保留
// 认证/不存在/网络这几类可操作信息），这里再翻成用户能照做的句子。
func pullFailureMessage(err error) string {
	switch {
	case errors.Is(err, runtime.ErrPullRejected):
		return "镜像仓库拒绝了本次拉取（" + cleanErrDetail(err, runtime.ErrPullRejected) + "）"
	case errors.Is(err, runtime.ErrDigestMismatch):
		return "拉取到的镜像与该 digest 不一致，已按失败处理"
	}
	return "拉取失败，请检查镜像名称与本机网络"
}

// cleanErrDetail 取出错误里已脱敏的简短说明，去掉错误链前缀。
//
// 长度上限 100：这是给界面展示的一句话，不是诊断全文。
// 截断而不是整段抛出，避免异常长文本把界面撑开。
func cleanErrDetail(err, target error) string {
	msg := strings.TrimPrefix(err.Error(), target.Error()+": ")
	if len(msg) > 100 {
		msg = msg[:100]
	}
	return msg
}
