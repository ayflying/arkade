package stackmigrate

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func (m *Manager) run(parent context.Context, t *Task, opts Options) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Hour)
	stop := context.AfterFunc(m.ctx, cancel)
	defer func() { stop(); cancel() }()
	res := &Residual{}
	// Convert backend panics into a terminal task so capacity/pair locks never leak.
	defer func() {
		if p := recover(); p != nil {
			res.Uncertain = true
			m.fail(t, res, fmt.Errorf("迁移后端异常：%v", p))
		}
	}()
	t.mu.Lock()
	t.status = StatusRunning
	t.startedAt = m.now()
	t.mu.Unlock()
	if err := m.runStages(ctx, t, opts, res); err != nil {
		m.fail(t, res, err)
		return
	}
	t.setResidual(*res)
	t.setStage(StageFinish)
	t.appendEvent(StreamStage, "迁移完成")
	m.finish(t, StatusSucceeded, "")
}
func (m *Manager) fail(t *Task, res *Residual, err error) {
	t.setResidual(*res)
	snap := t.snapshot(0)
	message := "迁移失败（停在第 " + itoa(StageIndex(snap.Stage)) + " 步「" + StageTitle(snap.Stage) + "」）：" + err.Error() + "。" + res.Describe()
	t.appendEvent(StreamStderr, message)
	m.finish(t, StatusFailed, message)
}
func (m *Manager) finish(t *Task, status, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	// Pair release and visible terminal transition form one critical section.
	pk := pairKey(t.Source, t.Target)
	if m.busyPair[pk] == t.id {
		delete(m.busyPair, pk)
	}
	t.status = status
	t.finishedAt = m.now()
	t.errText = message
}
func (m *Manager) permissions(ctx context.Context, o Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !m.backend.AgentOnline(o.Source) {
		return errors.New("源服务器当前离线")
	}
	if !m.backend.AgentOnline(o.Target) {
		return errors.New("目标服务器当前离线")
	}
	if o.Source == o.Target {
		return errors.New("源服务器与目标服务器不能是同一台")
	}
	ok, err := m.backend.CanManage(ctx, o.Source, o.StackName)
	if err != nil {
		return fmt.Errorf("无法确认你对源服务器的管理权限：%w", err)
	}
	if !ok {
		return errors.New("你没有源服务器上该编排的管理权限")
	}
	ok, err = m.backend.CanManage(ctx, o.Target, o.StackName)
	if err != nil {
		return fmt.Errorf("无法确认你对目标服务器的管理权限：%w", err)
	}
	if !ok {
		return errors.New("你没有目标服务器的管理权限")
	}
	return nil
}
func (m *Manager) targetAbsent(ctx context.Context, o Options) error {
	exists, err := m.backend.StackExists(ctx, o.Target, o.StackName)
	if err != nil {
		return fmt.Errorf("无法检查目标服务器上的同名编排：%w", err)
	}
	if exists {
		return errors.New("目标服务器上已存在同名编排「" + o.StackName + "」，请先在目标上改名或删除后再迁移")
	}
	return nil
}
func (m *Manager) sourceIdle(ctx context.Context, o Options) error {
	busy, err := m.backend.StackBusy(ctx, o.Source, o.StackName)
	if err != nil {
		return fmt.Errorf("无法确认源端编排是否正在操作：%w", err)
	}
	if busy {
		return errors.New("源端编排正在执行其它操作，请等它结束后再迁移")
	}
	return nil
}

type sourceConfig struct{ config, env, hash string }

func (m *Manager) guard(ctx context.Context, o Options, source *sourceConfig, written bool) error {
	if err := m.permissions(ctx, o); err != nil {
		return err
	}
	if !written {
		if err := m.targetAbsent(ctx, o); err != nil {
			return err
		}
	}
	if err := m.sourceIdle(ctx, o); err != nil {
		return err
	}
	if source != nil {
		config, env, hash, err := m.backend.ReadCompose(ctx, o.Source, o.StackName)
		if err != nil {
			return fmt.Errorf("重新确认源端编排配置失败：%w", err)
		}
		if config != source.config || env != source.env || hash != source.hash {
			return errors.New("源端编排配置已变化，已停止迁移；请基于最新配置重新提交")
		}
	}
	return ctx.Err()
}
func mergeFailure(res *Residual, err error) {
	var detail *ResidualError
	if errors.As(err, &detail) {
		res.ImagesImported += detail.Residual.ImagesImported
		res.VolumesRestored += detail.Residual.VolumesRestored
		res.ComposeWritten = res.ComposeWritten || detail.Residual.ComposeWritten
		res.Started = res.Started || detail.Residual.Started
		res.Uncertain = res.Uncertain || detail.Residual.Uncertain
		return
	}
	// RPC cancellation/timeout may occur after target-side commit; never promise
	// rollback or "no changes" without a backend-provided result.
	res.Uncertain = true
}
