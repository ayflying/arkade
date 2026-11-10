package runtime

// helper_lifecycle.go：restricted helper 容器的**公共生命周期**。
//
// volume 文件管理与编排配置读取都需要同一套东西：
// 并发闸门 + 总超时 + 镜像固定 + 挂载 + 注入二进制 + 启动等待 +
// 读回结果 + 按自己 ID 删除。差异只有"挂什么、跑哪个子命令"。
//
// 抽出来的理由：安全字段（CapDrop / no-new-privileges / 非特权 /
// CreateMountpoint=false）一旦在某一处漏掉就是静默的权限放宽。
// 只有一份实现才能保证改一次全生效。
//
// 注意：既有 volume 路径的常量与函数仍留在 volume_helper.go，
// 本文件只新增 source 需要的部分，避免改动已冻结的 volume 行为。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"arkade/agent/internal/helperfs"
)

const (
	// helperTimeout 是单次 helper 操作的总时限，从入口算起。
	//
	// 不依赖上游 ctx：Hub 的 WS pending 超时不会自动取消远端请求，
	// ctx 可能根本不带取消信号。Agent 必须自己兜住总时长。
	helperTimeout = 60 * time.Second

	// maxHelperConcurrent 是同时运行的 helper 容器上限。
	//
	// 每个 helper 上限 192MiB，请求可能有数 MiB。不限制时并发会成倍
	// 放大内存，把 Agent 自己 OOM 掉。限 2 够"列表+读文件"的正常并行。
	maxHelperConcurrent = 2

	// maxHelperResponseBytes 是 helper 结果文件的上限。
	maxHelperResponseBytes = 8 << 20

	// sourceMountPoint 是 helper 容器内工作目录的固定挂载点。
	//
	// 固定常量而不是传入：hidden CLI 只认这一个位置，
	// 因此它永远不可能被当成"读容器内任意目录"的工具。
	sourceMountPoint = "/source"

	// helperSourceResponsePath 是 compose-source-helper 的结果文件位置。
	//
	// 直接引用 helperfs 的常量，而不是在这里再写一次字面量：
	// 注入端、读取端、产出端必须是同一个路径，
	// 各自写一份就会像 request 那次一样静默漂移（真实故障）。
	helperSourceResponsePath = helperfs.ResponsePath
)

// helperSem 是全局并发闸门（容量即上限）。
var helperSem = make(chan struct{}, maxHelperConcurrent)

// ErrHelperBusy 表示并发已满。
var ErrHelperBusy = errors.New("文件操作繁忙，请稍后重试")

// acquireHelperSlot 非阻塞地占用一个并发槽位。
//
// 满了**立刻**返回 busy 而不是排队：请求已在 Hub 侧计超时，
// 在这里静默排队只会让总时长不可控。
func acquireHelperSlot(ctx context.Context) (func(), error) {
	select {
	case helperSem <- struct{}{}:
		return func() { <-helperSem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return nil, ErrHelperBusy
	}
}

// helperSpec 描述一次 helper 运行的全部差异点。
type helperSpec struct {
	// cmd 是传给注入二进制的子命令与参数（不含二进制本身）。
	cmd []string
	// mounts 是挂载列表，由各业务自行构造，公共层不改写。
	mounts []helperMount
	// request 会被序列化成 request.json 注入。
	request []byte
	// responsePath 是结果文件在容器内的路径。
	responsePath string
	// readSearch 为 true 时给 helper 加 DAC_READ_SEARCH capability。
	//
	// ============================ 为什么需要它 ============================
	//
	// helper 是 User:0:0 + CapDrop ALL，即**没有 DAC_OVERRIDE**。
	// 因此宿主上 0600/0700 的编排文件在 helper 里会读失败，
	// 而我们绝不能替用户 chmod（那是改别人的文件）。
	//
	// DAC_READ_SEARCH 只绕过"读/目录遍历"的权限检查，不授予写、不授予
	// 改变属主、也不扩大挂载范围——挂什么仍然只能读到什么。
	//
	// ============================ 谁设置它 ============================
	//
	// 只有编排配置读取（compose.source 的 auto 与 raw 两条路径）会设为 true。
	// 卷文件管理等其余 helper 恒为 false——它们没有"读 0600 宿主配置"的需求，
	// 也就没有理由放宽。
	readSearch bool
}

// runHelper 执行一次完整的 helper 生命周期并返回结果文件内容。
//
// 容器**只删除自己创建的 ID**，绝不用标签批量删（会误删别人的容器）。
func (e *engine) runHelper(ctx context.Context, spec helperSpec) ([]byte, error) {
	// 总时限：不依赖上游 ctx 是否携带取消。
	ctx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()

	release, err := acquireHelperSlot(ctx)
	if err != nil {
		return nil, err
	}
	// 槽位归还与外部 ctx 是否取消无关，否则会永久泄漏并发额度。
	defer release()

	// 镜像用**内容 ID** 而不是 tag：tag 可被并发 retag，
	// 用 tag 会让"检查过的镜像"和"实际启动的镜像"可能不是同一个。
	imageID, err := e.helperImageID(ctx)
	if err != nil {
		return nil, err
	}

	containerID, err := e.createHelper(ctx, imageID, spec)
	if err != nil {
		return nil, err
	}
	// 无论成败都删掉自己创建的这个容器，且**只用这个 ID**。
	defer func() {
		rmCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = e.client.delete(rmCtx, pathContainerPrefix+url.PathEscape(containerID)+"?force=1&v=0")
	}()

	if err := e.injectHelper(ctx, containerID, spec.request); err != nil {
		return nil, err
	}
	return e.startAndCollect(ctx, containerID, spec.responsePath)
}

// createHelper 创建 helper 容器并返回其 ID。
func (e *engine) createHelper(ctx context.Context, imageID string, spec helperSpec) (string, error) {
	pids := int64(64)
	// CapAdd 默认是 nil（即 CapDrop ALL 之后不再加回任何能力）。
	// 只有 raw 读取路径会加 DAC_READ_SEARCH，其余一律保持空。
	var capAdd []string
	if spec.readSearch {
		capAdd = []string{"DAC_READ_SEARCH"}
	}
	cfg := helperContainerConfig{
		Image: imageID,
		// Entrypoint 显式固定：基础镜像自带 ENTRYPOINT（alpine 是 /bin/sh）
		// 会把我们的参数当成 shell 脚本参数。
		Entrypoint: []string{helperBinaryPath},
		Cmd:        spec.cmd,
		// User 覆盖为 root：镜像的 USER 可被任何本地 tag 改动，不可信。
		User: "0:0",
		// Env 清空：不继承镜像里的任何环境变量。
		Env:    []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Labels: map[string]string{helperLabel: "helper"},
		HostConfig: helperHostConfig{
			// 注入二进制需要写容器根，故不设只读根；容器内是隔离环境。
			ReadOnlyRootfs: false,
			Privileged:     false,
			AutoRemove:     false, // 自己删，便于失败时读日志
			NetworkMode:    "none",
			CapDrop:        []string{"ALL"},
			CapAdd:         capAdd,
			SecurityOpt:    []string{"no-new-privileges"},
			Tmpfs:          map[string]string{"/tmp": "rw,noexec,nosuid,size=16m"},
			Memory:         192 << 20,
			NanoCpus:       1_000_000_000,
			PidsLimit:      &pids,
			Mounts:         spec.mounts,
		},
		Networking: helperNetworkConfig{NetworkMode: "none"},
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("序列化容器配置失败: %w", err)
	}

	respData, err := e.client.do(ctx, http.MethodPost,
		pathContainersCreate+"?name="+url.QueryEscape("arkade-helper-"+shortID()),
		strings.NewReader(string(body)))
	if err != nil {
		return "", e.translateHelperError(err)
	}
	var created struct {
		ID string `json:"Id"`
	}
	if json.Unmarshal(respData, &created) != nil || created.ID == "" {
		return "", errors.New("创建 helper 容器失败：引擎返回无法解析")
	}
	return created.ID, nil
}

// injectHelper 把二进制与请求文件注入容器根。
func (e *engine) injectHelper(ctx context.Context, containerID string, reqJSON []byte) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("无法定位 Agent 可执行文件: %w", err)
	}
	if err := e.putArchiveStream(ctx, containerID, "/", exe, reqJSON); err != nil {
		return fmt.Errorf("向 helper 容器注入文件失败: %w", err)
	}
	return nil
}

// startAndCollect 启动容器、等待结束、读回结果。
func (e *engine) startAndCollect(ctx context.Context, containerID, responsePath string) ([]byte, error) {
	if err := e.client.post(ctx, pathContainerPrefix+url.PathEscape(containerID)+actionStart); err != nil {
		return nil, e.translateHelperError(err)
	}

	exitCode, werr := e.waitContainer(ctx, containerID, helperTimeout)
	if werr != nil {
		return nil, werr
	}

	// 非零退出码仍要先读结果：helper 业务失败时也会写 response.json
	// 并返回非零。直接报错会把结构化错误码丢掉，上层只能落到兜底错误。
	out, gerr := e.getArchiveFile(ctx, containerID, responsePath)
	if gerr != nil {
		if exitCode != 0 {
			return nil, fmt.Errorf("helper 进程失败（退出码 %d）且未留下结果", exitCode)
		}
		return nil, fmt.Errorf("读取 helper 结果失败: %w", gerr)
	}
	if !json.Valid(out) {
		if exitCode != 0 {
			return nil, fmt.Errorf("helper 进程失败（退出码 %d）且结果不是合法 JSON", exitCode)
		}
		return nil, errors.New("helper 返回了无法解析的结果")
	}
	return out, nil
}
