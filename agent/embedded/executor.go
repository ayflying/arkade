// Package embedded 为 Hub 提供进程内 Agent 执行器，复用原有处理器与运行时。
// 公开 API 不暴露 agent/internal 类型，也不启动 Agent 的网络连接。
package embedded

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"arkade/agent/internal/compose"
	"arkade/agent/internal/handler"
	"arkade/agent/internal/imagetransfer"
	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// Options 是本机执行器的启动配置。ComposeBin 为 docker 可执行文件路径。
type Options struct {
	DataDir, Runtime, Endpoint, ComposeBin, AgentID string
	Log                                             *slog.Logger
}

// Info 是可公开给 Hub 的运行时信息；Capabilities 与实际注册方法完全对应。
type Info struct {
	Kind, Version, Endpoint string
	Rootless                bool
	Capabilities            []string
}

// Executor 管理独立的本机执行生命周期，不依赖构造请求或某次 RPC 的上下文。
type Executor struct {
	reg       *handler.Registry
	rt        runtime.Runtime
	agentID   string
	root      context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	closed    bool
	calls     sync.WaitGroup
	sweepDone chan struct{}
	closeDone chan struct{}
	image     *imagetransfer.Manager
	seq       atomic.Uint64
}

// New 完成装配。数据目录不可写直接报错；引擎不可用则降级 none，不阻断 Hub。
func New(ctx context.Context, opts Options) (*Executor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	kind := strings.ToLower(strings.TrimSpace(opts.Runtime))
	if kind != "" && kind != "auto" && kind != "docker" && kind != "podman" && kind != "none" {
		return nil, errors.New("不支持的本机运行时类型")
	}
	dir := strings.TrimSpace(opts.DataDir)
	if dir == "" {
		dir = "/opt/arkade/agent"
	}
	// 探测真实写入能力，不能把初始化失败静默降级成缺少能力。
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("创建本机 Agent 数据目录失败: %w", err)
	}
	probe, err := os.CreateTemp(dir, ".embedded-write-*")
	if err != nil {
		return nil, fmt.Errorf("本机 Agent 数据目录不可写: %w", err)
	}
	probePath := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(probePath)
	if closeErr != nil {
		return nil, fmt.Errorf("检查本机 Agent 数据目录失败: %w", closeErr)
	}
	if removeErr != nil {
		return nil, fmt.Errorf("清理本机 Agent 数据目录探针失败: %w", removeErr)
	}
	log := opts.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	var rt runtime.Runtime = runtime.NewNone("")
	if kind != "none" {
		rt, err = runtime.NewWithContext(ctx, runtime.Config{Socket: opts.Endpoint, PreferPodman: kind == "podman"})
		if err != nil {
			log.Warn("本机运行时不可用，降级为 none")
			rt = runtime.NewNone("")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	image, err := imagetransfer.NewManager(dir, rt, imagetransfer.DefaultLimits())
	if err != nil {
		return nil, fmt.Errorf("初始化本机传输工作区失败: %w", err)
	}
	root, cancel := context.WithCancel(context.Background())
	id := strings.TrimSpace(opts.AgentID)
	if id == "" {
		id = "agent-local"
	}
	e := &Executor{rt: rt, agentID: id, root: root, cancel: cancel, reg: handler.NewRegistry(), image: image, sweepDone: make(chan struct{}), closeDone: make(chan struct{})}
	ri, _ := rt.Info(ctx)
	host := ri.Endpoint
	// Go HTTP 客户端使用 http://，docker CLI 使用 tcp://；两者必须指向同一 daemon。
	// 禁止丢弃已选中的远端，否则 compose 会误操作默认本机 daemon。
	if strings.HasPrefix(host, "http://") {
		host = "tcp://" + strings.TrimPrefix(host, "http://")
	}
	if !strings.HasPrefix(host, "unix://") && !strings.HasPrefix(host, "tcp://") {
		host = ""
	}
	mgr := compose.NewManager(dir, host)
	mgr.SetBinary(opts.ComposeBin)
	mgr.RootCtx = root
	if src, ok := rt.(interface {
		ListContainerLabels(context.Context) ([]compose.ContainerLabels, error)
	}); ok {
		mgr.SetLabelSource(src)
	}
	handler.RegisterDefaultsFull(e.reg, rt, mgr, image, filepath.Join(dir, imagetransfer.TransfersDirName()), log)
	go func() { defer close(e.sweepDone); image.RunSweeper(root) }()
	return e, nil
}

// Info 返回运行时信息副本，不暴露 internal 类型。
func (e *Executor) Info(ctx context.Context) (Info, error) {
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed {
		return Info{}, errors.New("本机执行器已关闭")
	}
	ri, err := e.rt.Info(ctx)
	if err != nil {
		return Info{}, err
	}
	return Info{Kind: string(ri.Kind), Version: ri.Version, Endpoint: ri.Endpoint, Rootless: ri.Rootless, Capabilities: e.reg.Methods()}, nil
}

// Call 经过与远程 Agent 相同的 Envelope 校验及 Registry.Dispatch。
func (e *Executor) Call(ctx context.Context, method string, payload []byte) ([]byte, *envelope.Error) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, envelope.NewError(envelope.CodeAgentOffline, "本机执行器已关闭")
	}
	e.calls.Add(1)
	e.mu.Unlock()
	defer e.calls.Done()
	callCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(e.root, cancel)
	defer func() { stop(); cancel() }()
	if err := callCtx.Err(); err != nil {
		return nil, envelope.NewError(envelope.CodeTimeout, "本机调用已取消")
	}
	if len(payload) == 0 {
		payload = []byte(`{}`)
	}
	req, err := envelope.NewRequest(fmt.Sprintf("embedded-%d", e.seq.Add(1)), method, "agent:"+e.agentID, json.RawMessage(payload))
	if err != nil {
		return nil, envelope.NewError(envelope.CodeBadRequest, "请求载荷不是有效 JSON")
	}
	resp := e.reg.Dispatch(callCtx, req)
	if resp.Error != nil {
		return nil, resp.Error
	}
	if method == envelope.MethodContainerList {
		// 与网络连接器 stampSnapshotAgentID 同构：使用当前稳定 Agent 身份。
		var result envelope.ContainerListResult
		if err := json.Unmarshal(resp.Payload, &result); err != nil {
			return nil, envelope.NewError(envelope.CodeInternal, "解析本机容器列表失败")
		}
		result.AgentID = e.agentID
		stamped, err := json.Marshal(result)
		if err != nil {
			return nil, envelope.NewError(envelope.CodeInternal, "序列化本机容器列表失败")
		}
		return stamped, nil
	}
	return append([]byte(nil), resp.Payload...), nil
}

// Close 幂等地取消任务并等待在途 RPC 与清扫器退出。超时后清理仍继续。
func (e *Executor) Close(ctx context.Context) error {
	e.mu.Lock()
	if !e.closed {
		e.closed = true
		e.cancel()
		go func() {
			// 根上下文已取消同步/异步命令；先等 RPC 退出，避免管理器关闭竞争。
			e.calls.Wait()
			e.reg.Close()
			<-e.sweepDone
			e.image.Close()
			close(e.closeDone)
		}()
	}
	e.mu.Unlock()
	select {
	case <-e.closeDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
