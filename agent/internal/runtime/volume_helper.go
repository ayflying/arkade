package runtime

// volume_helper.go：通过临时 helper 容器读写"具名卷"里的文件。
//
// 背景：Agent 自己把 docker.sock 挂进来，但**看不到卷在宿主机上的真实路径**
// （Mountpoint）。因此不能直接 bind mount 卷——那需要宿主机路径。
//
// 方案：让引擎替我们挂载。创建一次性 helper 容器，把目标卷挂到 /data，
// 再用 PUT /containers/{id}/archive 把「当前 Agent 二进制 + request.json」
// 注入容器根目录，启动它执行隐藏子命令 volume-file-helper。
//
// 安全取舍（与 Lead 确认过）：
//   - helper **不**挂任何宿主机目录，只挂指定的那个具名卷；
//   - NetworkMode=none、CapDrop=ALL、no-new-privileges、tmpfs /tmp；
//   - ReadOnlyRootfs **必须设为 false**：创建后才用 PUT archive 写根目录，
//     只读根会让注入失败；而容器内是隔离的，写它的根不触及宿主机。
//     卷本身仍是唯一持久数据面。
//   - 不自动 pull 基础镜像；缺镜像时明确报错。
//   - 只使用**已存在**的具名卷，绝不自动创建卷。

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

const (
	// DefaultHelperImage 是 helper 基础镜像。必须已在本地，不自动拉取。
	DefaultHelperImage = "alpine:3.20"
	// helperBinaryPath 是注入到容器里的 Agent 二进制路径。
	helperBinaryPath = "/arkade-agent"
	// helperRequestPath 与 helperfs 中的常量保持一致。
	helperRequestPath = "/request.json"
	// helperResponsePath 是 helper 写回结果的位置。
	helperResponsePath = "/response.json"
	// helperDataMount 是卷在容器内的挂载点。
	helperDataMount = "/data"

	helperLabel = "io.arkade.role"
)

// ErrHelperImageMissing 表示本地缺少 helper 基础镜。
var ErrHelperImageMissing = errors.New("缺少 helper 基础镜像")

// ErrVolumeNotFound 表示具名卷不存在。
var ErrVolumeNotFound = errors.New("存储卷不存在")

// VolumeFileRuntime 是可选能力：在具名卷上执行文件操作。
//
// 与 ImageTransferRuntime 同样的做法——**独立可选接口**，
// 不修改既有 Runtime 接口，因此所有已有的 mock 无需改动。
type VolumeFileRuntime interface {
	// RunVolumeFileHelper 在具名卷上执行一次文件操作，返回 helper 的 JSON 响应。
	RunVolumeFileHelper(ctx context.Context, volume string, request map[string]any) ([]byte, error)
}

// helperContainerConfig 是创建 helper 容器时发给引擎的 JSON。
type helperContainerConfig struct {
	Image      string              `json:"Image"`
	Entrypoint []string            `json:"Entrypoint"`
	Cmd        []string            `json:"Cmd"`
	User       string              `json:"User"`
	Labels     map[string]string   `json:"Labels"`
	HostConfig helperHostConfig    `json:"HostConfig"`
	Env        []string            `json:"Env,omitempty"`
	WorkingDir string              `json:"WorkingDir,omitempty"`
	Networking helperNetworkConfig `json:"NetworkingConfig"`
}

type helperNetworkConfig struct {
	NetworkMode string `json:"NetworkMode"`
}

type helperHostConfig struct {
	// ReadOnlyRootfs 必须为 false：注入二进制需要写容器根。
	ReadOnlyRootfs bool              `json:"ReadOnlyRootfs"`
	Privileged     bool              `json:"Privileged"`
	AutoRemove     bool              `json:"AutoRemove"`
	NetworkMode    string            `json:"NetworkMode"`
	CapDrop        []string          `json:"CapDrop"`
	SecurityOpt    []string          `json:"SecurityOpt"`
	Tmpfs          map[string]string `json:"Tmpfs"`
	Memory         int64             `json:"Memory"`
	NanoCpus       int64             `json:"NanoCpus"`
	PidsLimit      *int64            `json:"PidsLimit"`
	Mounts         []helperMount     `json:"Mounts"`
}

type helperMount struct {
	Type        string             `json:"Type"`
	Source      string             `json:"Source"`
	Target      string             `json:"Target"`
	ReadOnly    bool               `json:"ReadOnly,omitempty"`
	BindOptions *helperBindOptions `json:"BindOptions,omitempty"`
}

// helperBindOptions 控制 bind 行为。
//
// CreateMountpoint 必须为 false：为 true 时 daemon 会在宿主机上
// **创建**缺失的目录，那会让"卷已被删除"变成"悄悄建出一个空目录"。
type helperBindOptions struct {
	CreateMountpoint bool `json:"CreateMountpoint"`
}

// isReadOnlyOperation 判断该操作是否只读。
//
// read/list 用只读挂载：即使应用层校验有疏漏，引擎也不会让写入落地。
func isReadOnlyOperation(op any) bool {
	s, _ := op.(string)
	return s == "list" || s == "read"
}

// ============================ 并发与超时限制 ============================
const (
	// helperTimeout 是单次卷文件操作的总时限，从进入 RunVolumeFileHelper 算起。
	//
	// 为什么在这里设而不只依赖 Hub：Hub 的 75s ctx 未必传到 Agent——
	// WS 侧 pending 超时不会自动取消远端请求，ctx 可能根本不带取消信号。
	// 因此 Agent 必须自己兜住总时长，否则一个卡住的 helper 会长期占着槽位。
	helperTimeout = 60 * time.Second

	// maxHelperConcurrent 是同时运行的 helper 容器上限。
	//
	// 每个 helper 内存上限 192MiB 且请求最大 5.6MiB（base64 在内存里
	// 还会被解码、拷贝若干份）。不加限制时 N 个并发请求会成倍放大
	// Agent 的内存占用，直接把 Agent 自己 OOM 掉。
	// 限 2 既能满足"看目录 + 读文件"这种正常并行，又能兜住上限。
	maxHelperConcurrent = 2
)

// helperSem 是全局并发闸门。
//
// 用带缓冲 channel 实现信号量：容量就是并发上限。
// 非阻塞获取（select + default）——满了就立刻返回 busy，
// 而不是排队等待：请求已经在 Hub 侧计了超时，
// 在这里静默排队只会让总时长不可控，还不如让用户知道"忙"。
var helperSem = make(chan struct{}, maxHelperConcurrent)

// ErrHelperBusy 表示并发已满。
var ErrHelperBusy = errors.New("文件操作繁忙，请稍后重试")

// RunVolumeFileHelper 实现 volumeFileRuntime。
func (e *engine) RunVolumeFileHelper(ctx context.Context, volume string, request map[string]any) ([]byte, error) {
	if strings.TrimSpace(volume) == "" {
		return nil, errors.New("存储卷名不能为空")
	}

	// 总时限：不依赖上游 ctx 是否携带取消。
	ctx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()

	// 并发闸门：满了直接报忙，不排队。
	select {
	case helperSem <- struct{}{}:
		// 槽位清理用独立 ctx：外部 ctx 可能已取消，
		// 但槽位必须归还，否则会永久泄漏并发额度。
		defer func() { <-helperSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return nil, ErrHelperBusy
	}

	// 1. 卷必须已存在，并取到它的宿主机路径。
	//
	// ============================ 为什么用 bind 而不是 volume 类型 ============================
	//
	// 用 Mounts[{Type:"volume", Source:name}] 有个陷阱：**Source 指定的卷不存在时，
	// daemon 会静默自动创建它**。而我们刚 inspect 过它存在——
	// 若这中间被外部删除，daemon 会新建一个空卷，于是用户"打开卷"看到的是
	// 一个空目录，还以为数据没了。这比直接报错糟糕得多。
	//
	// 改成 bind 到 inspect 返回的 Mountpoint：
	//   - 路径不存在 → daemon 直接 mount 失败，我们如实报错，绝不建卷；
	//   - Mountpoint 来自 daemon 而不是用户输入，不存在注入；
	//   - Agent 自己不需要能读这个路径（它只传字符串给 daemon）。
	//
	// CreateMountpoint 必须为 false：否则 daemon 会在宿主机上**创建**缺失的目录，
	// 那正好又回到"意外建出空路径"的问题。
	mountPoint, err := e.volumeMountpoint(ctx, volume)
	if err != nil {
		return nil, err
	}
	// 2. 基础镜像必须已在本地（绝不自动 pull）。
	//    用**镜像 ID**（内容寻址）而不是 tag：tag 可被并发 retag，
	//    用 tag 会让"检查过的镜像"和"实际启动的镜像"可能不是同一个。
	imageID, err := e.helperImageID(ctx)
	if err != nil {
		return nil, err
	}

	reqJSON, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	// 4. 创建容器。
	//
	// 资源与隔离规格（与 Lead 对齐）：
	//   - Memory 192MiB：4MiB base64 在 Go 里经过多次拷贝（解码、JSON、tar），
	//     加 Go runtime 自身开销，96MiB 偏紧会被 OOM kill；
	//   - PidsLimit 64：够 Go runtime 起必要的线程，又挡住 fork 炸弹；
	//   - NanoCpus 1 CPU：编码/哈希是 CPU 密集，但单个卷操作不该抢占业务；
	//   - NetworkMode none、不 Privileged、CapDrop ALL、no-new-privileges；
	//   - 只挂**明确的 volume 类型**，无任何 host path。
	pids := int64(64)
	cfg := helperContainerConfig{
		Image: imageID,
		// Entrypoint 必须显式固定：基础镜像自带的 ENTRYPOINT（alpine 是 /bin/sh）
		// 会覆盖 Cmd，导致我们的参数被当成 shell 脚本参数。
		// Cmd 第三个参数是**容器内的挂载点** /data，不是卷名——
		// helper 用它在卷根上开 os.Root。
		Entrypoint: []string{helperBinaryPath},
		Cmd:        []string{"volume-file-helper"},
		// User 必须显式覆盖为 root。
		//
		// 基础镜像的 USER 可以被任何本地 tag 改动（alpine:3.20 只是我们
		// 期望的样子，本地实际指向什么内容我们无法验证）。若继承镜像里的
		// 非 root 用户，helper 会以该身份读卷——可能读不到（权限错误）
		// 也可能读到不该读的东西，行为随本地镜像内容漂移。
		// 容器内 root + CapDrop ALL + 只挂一个卷，权限边界由引擎保证。
		User:   "0:0",
		Labels: map[string]string{helperLabel: "volume-file-helper"},
		// 清空 Env：不继承镜像里的任何环境变量。
		// 对静态链接的 Go 二进制而言 LD_PRELOAD 无影响，
		// 但显式清空能避免未来换成动态链接时出现隐蔽问题。
		Env: []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		HostConfig: helperHostConfig{
			// 见文件头说明：注入需要可写的容器根。
			ReadOnlyRootfs: false,
			Privileged:     false,
			AutoRemove:     false, // 由我们自己删，便于失败时读日志
			NetworkMode:    "none",
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges"},
			Tmpfs:          map[string]string{"/tmp": "rw,noexec,nosuid,size=16m"},
			Memory:         192 << 20,
			NanoCpus:       1_000_000_000,
			PidsLimit:      &pids,
			Mounts: []helperMount{
				// bind 类型 + daemon 提供的宿主机路径（见上方说明）。
				// read/list 用只读；写操作必须可写，否则引擎层会挡住。
				{
					Type:        "bind",
					Source:      mountPoint,
					Target:      helperDataMount,
					ReadOnly:    isReadOnlyOperation(request["operation"]),
					BindOptions: &helperBindOptions{CreateMountpoint: false},
				},
			},
		},
		Networking: helperNetworkConfig{NetworkMode: "none"},
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("序列化容器配置失败: %w", err)
	}

	respData, err := e.client.do(ctx, http.MethodPost,
		pathContainersCreate+"?name="+url.QueryEscape("arkade-vfh-"+shortID()), bytes.NewReader(body))
	if err != nil {
		return nil, e.translateHelperError(err)
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(respData, &created); err != nil || created.ID == "" {
		return nil, errors.New("创建 helper 容器失败：引擎返回无法解析")
	}
	containerID := created.ID

	// 5. 无论如何都要删掉自己创建的这个容器。
	// 只删自己的 ID，绝不用名字/标签批量删（那会误删别人的）。
	defer func() {
		// 用独立的超时上下文：调用方的 ctx 可能已取消。
		rmCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = e.client.delete(rmCtx, pathContainerPrefix+url.PathEscape(containerID)+"?force=1&v=0")
	}()

	// 6. 注入二进制与请求文件。
	//    二进制约 11MiB（静态链接），**不**整体读进内存：
	//    io.Pipe 让 tar 边写边发，内存占用只剩 32KiB 缓冲。
	//    请求最多 5.6MiB，已由 handler 的上限保证有界，可以直接放进 tar。
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("无法定位 Agent 可执行文件: %w", err)
	}
	if err := e.putArchiveStream(ctx, containerID, "/", exe, reqJSON); err != nil {
		return nil, fmt.Errorf("向 helper 容器注入文件失败: %w", err)
	}

	// 7. 启动并等待结束。
	if err := e.client.post(ctx, pathContainerPrefix+url.PathEscape(containerID)+actionStart); err != nil {
		return nil, e.translateHelperError(err)
	}
	exitCode, err := e.waitContainer(ctx, containerID, helperTimeout)
	if err != nil {
		return nil, err
	}

	// 8. 取回响应文件。
	//
	// ============================ 非零退出码为什么仍要读 response ============================
	//
	// helper 在**业务失败**时也会写 response.json 并返回非零退出码
	// （见 helperfs.Run 的退出码约定：2=路径/参数类，3=其它）。
	// 如果这里一看到非零就直接返回错误，就把 helper 结构化错误码
	// （exists / checksum / not_found …）整个丢掉了，
	// handler 只能落到底部兜底 → runtime_failed(503)。
	// 于是"重复创建目录"本该是 409 conflict，却变成 503，
	// 前端还会提示"稍后重试"，而重试永远不可能成功。
	//
	// 因此：先读 response，只要它是**合法 JSON**，就以它为准；
	// 读不到或解析不出，才用退出码作为最终判据。
	out, aerr := e.getArchiveFile(ctx, containerID, helperResponsePath)
	if aerr != nil {
		if exitCode != 0 {
			return nil, fmt.Errorf("helper 进程失败（退出码 %d）且未留下结果", exitCode)
		}
		return nil, fmt.Errorf("读取 helper 结果失败: %w", aerr)
	}
	if !json.Valid(out) {
		if exitCode != 0 {
			return nil, fmt.Errorf("helper 进程失败（退出码 %d）且结果不是合法 JSON", exitCode)
		}
		return nil, errors.New("helper 返回了无法解析的结果")
	}
	return out, nil
}

// HelperAvailable 检查基础镜像是否在本地。
func (e *engine) HelperAvailable(ctx context.Context) error {
	_, err := e.InspectImage(ctx, DefaultHelperImage)
	if err == nil {
		return nil
	}
	// 统一报"缺镜像"，让用户知道该 docker pull 而不是猜别的原因。
	return fmt.Errorf("%w: %s（请先在本机拉取该镜像）", ErrHelperImageMissing, DefaultHelperImage)
}

// helperImageID 返回 helper 基础镜像的内容 ID。
//
// 缺镜像时报 ErrHelperImageMissing，**绝不自动 pull**：
// 拉取是有网络副作用且可能拉错版本的操作，不应该由"打开一个卷"触发。
func (e *engine) helperImageID(ctx context.Context) (string, error) {
	var img struct {
		ID string `json:"Id"`
	}
	err := e.client.getJSON(ctx, pathImageInspect+url.PathEscape(DefaultHelperImage)+"/json", &img)
	if err != nil || img.ID == "" {
		return "", fmt.Errorf("%w: %s（请先在本机拉取该镜像）", ErrHelperImageMissing, DefaultHelperImage)
	}
	return img.ID, nil
}

// volumeMountpoint 取具名卷在宿主机上的真实路径（由 daemon 提供）。
//
// 关键：这个路径**来自 inspect 结果**，不是用户输入，Agent 也不去读它。
// Agent 只把这个字符串交给 daemon 做 bind mount，实际文件访问发生在
// 容器命名空间内。因此 Agent 容器即使看不到该路径也完全没问题。
//
// 首批只支持 local 驱动且无外部挂载选项的卷：
//   - 其它驱动的 Mountpoint 可能是远程路径（NFS/CIFS），BindOptions 未必可用；
//   - 有 Options 的 local 卷（如 o=bind 指向宿主机目录）会让"卷"退化成
//     任意宿主机路径，这就等于把 helper 变成了宿主机文件浏览器。
func (e *engine) volumeMountpoint(ctx context.Context, name string) (string, error) {
	var vol struct {
		Name       string            `json:"Name"`
		Driver     string            `json:"Driver"`
		Mountpoint string            `json:"Mountpoint"`
		Options    map[string]string `json:"Options"`
	}
	err := e.client.getJSON(ctx, pathVolumes+"/"+url.PathEscape(name), &vol)
	if err != nil {
		var se *statusError
		if errors.As(err, &se) && se.Status == http.StatusNotFound {
			return "", fmt.Errorf("%w: %s", ErrVolumeNotFound, name)
		}
		return "", err
	}
	if vol.Name != name {
		// inspect 返回的名字对不上，说明解析出了问题，宁可拒绝。
		return "", fmt.Errorf("%w: %s", ErrVolumeNotFound, name)
	}
	if vol.Driver != "local" || len(vol.Options) > 0 {
		return "", errors.New("首批仅支持无自定义挂载选项的 local 卷")
	}
	mp := strings.TrimSpace(vol.Mountpoint)
	if mp == "" {
		return "", errors.New("引擎未返回该卷的挂载点")
	}
	// Mountpoint 来自 engine 的 metadata（不是用户输入），但仍做形态校验：
	//   - 必须是绝对路径：相对路径会被 daemon 按自己的工作目录解释；
	//   - 必须已规范化：含 ".." 或 "//" 说明来源不可信；
	//   - 不能是 "/"：把它 bind 进 helper 等于把整个宿主机根交给容器。
	//
	// Agent 自己**从不访问**这个路径，它只是把字符串交给 daemon。
	if !path.IsAbs(mp) {
		return "", errors.New("引擎返回的卷挂载点不是绝对路径")
	}
	if path.Clean(mp) != mp {
		return "", errors.New("引擎返回的卷挂载点未规范化")
	}
	if mp == "/" {
		return "", errors.New("引擎返回的卷挂载点异常")
	}
	return mp, nil
}

// putArchiveStream 用 PUT /containers/{id}/archive?path= 上传 tar。
//
// 二进制与请求文件通过 io.Pipe **流式**写入 HTTP 请求体：
// 静态链接的 Agent 约 11MiB，若先打成一个 []byte 再发，
// 峰值会多出 11MiB+ 的常驻分配；而 helper 本身就跑在同一个宿主机上，
// 这种无谓放大在并发时会被放大 N 倍。
//
// 长度未知走 chunked 编码：Docker 的 archive 端点支持它
// （它只是把请求体当作 tar 解包），已验证。
func (e *engine) putArchiveStream(ctx context.Context, containerID, destPath, exePath string, reqJSON []byte) error {
	pr, pw := io.Pipe()

	// 生产端：写入 tar（二进制 + request.json）。
	go func() {
		tw := tar.NewWriter(pw)
		if err := writeHelperTar(tw, exePath, reqJSON); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if err := tw.Close(); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()

	path := pathContainerPrefix + url.PathEscape(containerID) + "/archive?path=" + url.QueryEscape(destPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, e.client.url(path), pr)
	if err != nil {
		_ = pr.CloseWithError(err)
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	// 不给 ContentLength：走 chunked。长度需要预先算，而流式的意义就是不预先读。
	resp, err := e.client.httpc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("引擎返回 HTTP %d", resp.StatusCode)
	}
	return nil
}

// writeHelperTar 把二进制与请求文件写进 tar 流。
func writeHelperTar(tw *tar.Writer, exePath string, reqJSON []byte) error {
	exe, err := os.Open(exePath)
	if err != nil {
		return err
	}
	defer func() { _ = exe.Close() }()
	info, err := exe.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name:     strings.TrimPrefix(helperBinaryPath, "/"),
		Mode:     0o755,
		Size:     info.Size(),
		Typeflag: tar.TypeReg,
	}); err != nil {
		return err
	}
	// 按 tar 头声明的大小精确拷贝，避免多读导致 tar 流错位。
	if _, err := io.CopyN(tw, exe, info.Size()); err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name:     strings.TrimPrefix(helperRequestPath, "/"),
		Mode:     0o600,
		Size:     int64(len(reqJSON)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		return err
	}
	_, err = tw.Write(reqJSON)
	return err
}

// maxHelperResponseBytes 是 helper 结果文件的上限。
//
// 最大合法结果是 4MiB 内容的 base64（约 5.6MiB）加上 JSON 外壳，
// 这里留到 8MiB。**必须**在读到上限后再多读 1 字节来判定超限，
// 否则 LimitReader 会静默截断成一个"看起来能解析"的半截 JSON
// ——那比直接报错危险得多。
const maxHelperResponseBytes = 8 << 20

// getArchiveFile 用 GET /containers/{id}/archive?path= 取单个文件内容。
func (e *engine) getArchiveFile(ctx context.Context, containerID, filePath string) ([]byte, error) {
	path := pathContainerPrefix + url.PathEscape(containerID) + "/archive?path=" + url.QueryEscape(filePath)
	rc, err := e.client.openStream(ctx, http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()

	tr := tar.NewReader(rc)
	hdr, err := tr.Next()
	if err != nil {
		return nil, fmt.Errorf("归档为空或格式不正确: %w", err)
	}
	// 校验头：必须是我们要的那个普通文件，且大小在限内。
	// 只看第一条就够了——我们每次只取一个已知路径的文件。
	wantName := strings.TrimPrefix(filePath, "/")
	if strings.TrimPrefix(hdr.Name, "/") != wantName {
		return nil, fmt.Errorf("归档内容不是预期的文件（得到 %q）", hdr.Name)
	}
	if hdr.Typeflag != tar.TypeReg {
		return nil, errors.New("归档内容不是普通文件")
	}
	if hdr.Size < 0 || hdr.Size > maxHelperResponseBytes {
		return nil, errors.New("helper 结果超过大小上限")
	}

	// 多读 1 字节：能读到就说明实际内容超过声明大小，明确报错而不是静默截断。
	data, err := io.ReadAll(io.LimitReader(tr, maxHelperResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxHelperResponseBytes {
		return nil, errors.New("helper 结果超过大小上限")
	}
	// tar 头声明的大小与实际读到的不一致，说明流被截断。
	if int64(len(data)) != hdr.Size {
		return nil, fmt.Errorf("helper 结果不完整（声明 %d 字节，实际 %d 字节）", hdr.Size, len(data))
	}
	return data, nil
}

// waitContainer 等容器退出（轮询 inspect，避免依赖 wait 端点的长连接）。
func (e *engine) waitContainer(ctx context.Context, containerID string, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for {
		var st struct {
			State struct {
				Running  bool   `json:"Running"`
				ExitCode int    `json:"ExitCode"`
				Status   string `json:"Status"`
			} `json:"State"`
		}
		if err := e.client.getJSON(ctx, pathContainerPrefix+url.PathEscape(containerID)+"/json", &st); err != nil {
			return 0, err
		}
		if !st.State.Running {
			// 非零退出码**不是**这里要判定的失败：
			// helper 业务失败时会写 response.json 并返回非零，
			// 必须把退出码交给调用方，由它优先读取结构化结果。
			return st.State.ExitCode, nil
		}
		if time.Now().After(deadline) {
			return 0, errors.New("helper 执行超时")
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// translateHelperError 把引擎错误转成可读中文。
func (e *engine) translateHelperError(err error) error {
	var se *statusError
	if errors.As(err, &se) {
		switch se.Status {
		case http.StatusNotFound:
			return ErrVolumeNotFound
		case http.StatusConflict:
			return errors.New("helper 容器创建冲突，请重试")
		}
	}
	return e.translate(err)
}

// helperArchive 构造注入用的 tar：Agent 二进制 + request.json。
//
// 二进制设为 0755，request.json 设为 0600（内容可能含用户数据）。
func helperArchive(exePath string, reqJSON []byte) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	exe, err := os.Open(exePath)
	if err != nil {
		return nil
	}
	defer func() { _ = exe.Close() }()
	exeInfo, err := exe.Stat()
	if err != nil {
		return nil
	}
	_ = tw.WriteHeader(&tar.Header{
		Name:     strings.TrimPrefix(helperBinaryPath, "/"),
		Mode:     0o755,
		Size:     exeInfo.Size(),
		Typeflag: tar.TypeReg,
	})
	// 二进制可能几十 MB，流式写入 tar。
	_, _ = io.Copy(tw, exe)

	_ = tw.WriteHeader(&tar.Header{
		Name:     strings.TrimPrefix(helperRequestPath, "/"),
		Mode:     0o600,
		Size:     int64(len(reqJSON)),
		Typeflag: tar.TypeReg,
	})
	_, _ = tw.Write(reqJSON)
	_ = tw.Close()
	return buf.Bytes()
}

// shortID 生成一个短随机串用于容器名。
func shortID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 10)
	n := time.Now().UnixNano()
	for i := range b {
		b[i] = chars[int(n>>(i*3))%len(chars)]
	}
	return string(b)
}
