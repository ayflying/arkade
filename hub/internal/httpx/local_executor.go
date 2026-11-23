package httpx

import (
	"arkade/agent/embedded"
	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/config"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
	"arkade/protocol/hostmetrics"
	"context"
	"log/slog"
	"time"
)

// newLocalExecutor 只装配执行通道，不走网络握手；所有调用仍经过既有权限和审计。
func newLocalExecutor(ctx context.Context, cfg config.Config, db *store.DB, reg *agentlink.Registry, log *slog.Logger, version string) (*embedded.Executor, error) {
	if !cfg.Local.Enabled {
		return nil, nil
	}
	if err := db.EnsureEmbeddedLocalAgent(ctx); err != nil {
		return nil, err
	}
	rec, err := db.GetAgent(ctx, store.LocalAgentID)
	if err != nil {
		return nil, err
	}
	// 沿用旧禁用状态，升级不能悄悄重新启用已禁用的本机。
	if rec.Disabled {
		log.Warn("本机身份已禁用，未启动内置执行器")
		return nil, nil
	}
	executor, err := embedded.New(ctx, embedded.Options{DataDir: cfg.Local.DataDir, Runtime: cfg.Local.Runtime, Endpoint: cfg.Local.Endpoint, ComposeBin: cfg.Local.ComposeBin, AgentID: store.LocalAgentID, Log: log})
	if err != nil {
		return nil, err
	}
	info, err := executor.Info(ctx)
	if err != nil {
		_ = executor.Close(context.Background())
		return nil, err
	}
	now := time.Now().Unix()
	if err = db.UpdateEmbeddedLocalAgentRuntime(ctx, store.AgentRecord{ID: store.LocalAgentID, Status: "online", Version: version, Runtime: info.Kind, RuntimeVersion: info.Version, LastSeen: now, UpdatedAt: now}); err != nil {
		_ = executor.Close(context.Background())
		return nil, err
	}
	if err = reg.RegisterLocal(store.LocalAgentID, executor); err != nil {
		_ = executor.Close(context.Background())
		return nil, err
	}
	log.Info("Hub内置本机执行器已启动", "runtime", info.Kind)
	return executor, nil
}

func startLocalHeartbeat(db *store.DB, executor *embedded.Executor, reg *agentlink.Registry, log *slog.Logger, version string) func(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	collector := hostmetrics.NewCollector()
	record := reg.LocalHealthRecorder(store.LocalAgentID)
	go func() {
		defer close(done)
		timer := time.NewTicker(30 * time.Second)
		defer timer.Stop()
		for ctx.Err() == nil {
			metrics := collector.Sample()
			run, stop := context.WithTimeout(ctx, 5*time.Second)
			info, err := executor.Info(run)
			runtimeOK := false
			if err == nil && (info.Kind == string(envelope.RuntimeDocker) || info.Kind == string(envelope.RuntimePodman)) {
				// Info 是启动探测的缓存；成功查询容器才能证明当前引擎可达。
				_, probeErr := executor.Call(run, envelope.MethodContainerList, nil)
				runtimeOK = probeErr == nil
			}
			stop()
			record(runtimeOK, metrics)
			if err == nil {
				now := time.Now().Unix()
				write, stopWrite := context.WithTimeout(ctx, 5*time.Second)
				err = db.UpdateEmbeddedLocalAgentRuntime(write, store.AgentRecord{ID: store.LocalAgentID, Status: "online", Version: version, Runtime: info.Kind, RuntimeVersion: info.Version, LastSeen: now, UpdatedAt: now})
				stopWrite()
			}
			if err != nil && ctx.Err() == nil {
				log.Warn("更新内置本机运行状态失败")
			}
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		}
	}()
	return func(ctx context.Context) error {
		cancel()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
