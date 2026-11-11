// Package handler 是 Agent 的方法注册表与具体处理器实现。
//
// 关键设计（决策 D-05）：在线通道（Hub 下行请求）与本地 CLI（arkade-agent ps/start/...）
// 共用同一个 Registry。CLI 并不另写一套"读本地 Docker"的逻辑，而是构造一个和 Hub
// 下发的完全一样的 envelope，交给同一个 Dispatch 执行。这样保证：
//   - 离线自治行为与在线行为逐字节一致；
//   - 只有一处需要维护参数校验与错误翻译，不存在"线上修了、离线忘了"的分裂。
package handler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"

	"arkade/agent/internal/compose"
	"arkade/agent/internal/imagetransfer"
	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// Handler 处理方法调用并返回待序列化的载荷。
//
// payload 是请求信封的原始 JSON 载荷（可能为 null），由各处理器自行解码，
// 这样 Registry 不需要知道每种方法的载荷类型。
type Handler func(ctx context.Context, payload []byte) (any, error)

// Registry 是方法名 → 处理器的映射表。
//
// 并发说明：注册只在启动阶段完成（单线程），运行期只读，因此用普通 map 即可；
// 仍加 RWMutex 是为了防御未来可能的动态注册（如自升级后热加载能力），
// 让"读多写少"的语义显式可见。
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
	// closers 是随 Registry 一起关闭的子系统（当前是编排镜像更新任务表）。
	//
	// 为什么挂在 Registry 上而不是单独返回：main 已经持有 Registry 并负责
	// 它的生命周期，让清理动作跟着它走就不需要再给 main 增加一条接线，
	// 也就不会出现"某个部署路径忘了 defer Close"而留下孤儿进程。
	closers []func()
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string]Handler)}
}

// AddCloser 注册一个在 Registry 关闭时执行的清理动作。
func (r *Registry) AddCloser(fn func()) {
	if fn == nil {
		return
	}
	r.mu.Lock()
	r.closers = append(r.closers, fn)
	r.mu.Unlock()
}

// Close 执行全部清理动作（幂等）。
//
// Agent 优雅退出时必须调用：镜像更新任务里的 compose 子进程若不被取消，
// 会变成孤儿进程继续拉镜像，而此时 Agent 已经不在了，没人回收它。
func (r *Registry) Close() {
	r.mu.Lock()
	fns := r.closers
	r.closers = nil
	r.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// Register 注册一个方法。重复注册直接覆盖并在语义上视为配置错误——
// 但这里不做 panic：工具函数可能被多条初始化路径调用，覆盖反而更安全。
func (r *Registry) Register(method string, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[method] = h
}

// Has 判断方法是否已注册，用于能力清单生成。
func (r *Registry) Has(method string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.handlers[method]
	return ok
}

// Methods 返回已注册方法名（排序后），用于 hello 的能力上报。
// 排序是为了让日志与测试输出稳定，避免 map 遍历顺序带来的抖动。
func (r *Registry) Methods() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.handlers))
	for m := range r.handlers {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// Dispatch 执行一个请求信封，返回响应信封。
//
// 约定：无论处理器是否出错，都返回一个 response 信封（失败时带 Error 字段），
// 绝不返回 nil——调用方（Hub 或 CLI）依赖"每个请求都有回应"来避免永久等待。
func (r *Registry) Dispatch(ctx context.Context, req *envelope.Envelope) *envelope.Envelope {
	if req == nil {
		// 没有请求就没有可回填的 ID，只能构造一个独立的错误响应。
		return envelope.NewErrorResponse(&envelope.Envelope{ID: "unknown"}, envelope.NewError(envelope.CodeBadRequest, "请求为空"))
	}
	if e := req.Validate(); e != nil {
		return envelope.NewErrorResponse(req, e)
	}
	if req.Type != envelope.TypeRequest {
		return envelope.NewErrorResponse(req, envelope.NewError(envelope.CodeBadRequest,
			fmt.Sprintf("调度期望 request 类型，收到 %s", req.Type)))
	}

	r.mu.RLock()
	h, ok := r.handlers[req.Method]
	r.mu.RUnlock()
	if !ok {
		// 未注册的方法通常是版本不匹配（Hub 比 Agent 新），错误码用 unsupported
		// 而不是 not_found，便于 Hub 侧提示"请升级 Agent"。
		return envelope.NewErrorResponse(req, envelope.NewError(envelope.CodeUnsupported,
			fmt.Sprintf("Agent 不支持方法 %q（可能是 Agent 版本过旧，请升级）", req.Method)))
	}

	result, err := h(ctx, req.Payload)
	if err != nil {
		return envelope.NewErrorResponse(req, toEnvelopeError(err))
	}
	resp, merr := envelope.NewResponse(req, result)
	if merr != nil {
		return envelope.NewErrorResponse(req, envelope.NewError(envelope.CodeInternal,
			"序列化响应失败: "+merr.Error()))
	}
	return resp
}

// RegisterDefaults 把全部支持的方法一次性注册到注册表。
//
// 之所以用"一个注册函数"而不是让调用方逐个 Register：方法集合与能力清单
// 必须严格对应，集中注册可以保证 hello 上报的能力与实际能处理的方法不会脱节。
//
// composeMgr 为 nil 时不注册编排方法：没有数据目录就无法落盘编排文件，
// 注册了也只会在调用时失败。这种情况在本地 CLI 模式里可能出现，
// 此时 Hub 侧会得到 unsupported，提示比"下载后写文件失败"清楚得多。
// RegisterDefaults 注册全部内置方法。
//
// log 用于记录"降级但不致命"的失败（例如引擎标签探测失败）。
// 传 nil 时自动用静默日志器——测试里大多不关心日志，
// 强制每个调用点都构造一个日志器是没有必要的噪音。
func RegisterDefaults(reg *Registry, rt runtime.Runtime, composeMgr *compose.Manager, log *slog.Logger) {
	RegisterDefaultsWithTransfer(reg, rt, composeMgr, nil, log)
}

// RegisterDefaultsWithTransfer 与 RegisterDefaults 相同，但额外注册镜像传输方法。
//
// 为什么保留 RegisterDefaults 并让新函数接管实现：现有调用点（含大量测试）
// 一行都不用改——这正是"可选能力不绑架现有代码"的落地方式。
// 需要传输能力的调用方改用本函数并传入已构造好的 imagetransfer.Manager。
//
// imageMgr 为 nil 时不注册传输方法：没有传输临时区就无法落盘归档，
// 注册了也只会在调用时失败。
func RegisterDefaultsWithTransfer(reg *Registry, rt runtime.Runtime, composeMgr *compose.Manager,
	imageMgr *imagetransfer.Manager, log *slog.Logger) {
	registerAll(reg, rt, composeMgr, imageMgr, "", log)
}

// RegisterDefaultsFull 是完整的注册入口：在 WithTransfer 的基础上
// 额外接受传输临时区根目录，用于挂载编排迁移的卷归档与导入。
//
// ============================ 为什么用"再加一层"而不是改签名 ============================
// 现有调用点（含大量测试与 CLI）都在用前两个函数。直接给它们加参数
// 会让每一处都得改，而那些调用点根本不关心迁移。
// 可选能力因此保持可选：不传就没有对应方法，
// 而不是"注册了但调用时才失败"——后者会让 Hub 看到方法存在却永远失败。
//
// ============================ 委托关系（别再写"层层委托"） ============================
// 三个入口是**并列**的，不是链式的：
//
//	RegisterDefaults            → registerAll(..., imageMgr=nil, transferDir="")
//	RegisterDefaultsWithTransfer → registerAll(..., imageMgr, transferDir="")
//	RegisterDefaultsFull         → registerAll(..., imageMgr, transferDir)
//
// 前两个是"功能更少"的便捷入口，第三个是完整入口；
// 它们都直接落到 registerAll，谁都不经过谁。
// （早期注释写成"层层委托"是不准确的：Full 并不经过 WithTransfer。）
//
// ============================ 服务模式必须用这个函数 ============================
// 实测踩过：服务模式曾调 RegisterDefaultsWithTransfer，而它内部把
// transferDir 固定传空——于是卷归档/导入方法**根本没注册**，
// 界面上能点"同时迁移数据卷"，一点就报方法不存在，且极难归因。
// 测试全绿是因为测试自己传了值，覆盖不到真实启动路径。
// 因此真实启动路径必须走本函数，并由 cli 层保证传进来的是有效目录。
//
// transferDir 为空 → 不注册卷归档/导入（只注册 compose.import）。
func RegisterDefaultsFull(reg *Registry, rt runtime.Runtime, composeMgr *compose.Manager,
	imageMgr *imagetransfer.Manager, transferDir string, log *slog.Logger) {
	registerAll(reg, rt, composeMgr, imageMgr, transferDir, log)
}

// registerAll 是全部注册逻辑的唯一实现。
func registerAll(reg *Registry, rt runtime.Runtime, composeMgr *compose.Manager,
	imageMgr *imagetransfer.Manager, transferDir string, log *slog.Logger) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	}
	reg.Register(envelope.MethodRuntimeInfo, newRuntimeInfoHandler(rt))
	reg.Register(envelope.LocalMethodContainerList, newListHandler(rt))
	reg.Register(envelope.LocalMethodContainerStart, newStartHandler(rt))
	reg.Register(envelope.LocalMethodContainerStop, newStopHandler(rt))
	reg.Register(envelope.LocalMethodContainerRestart, newRestartHandler(rt))
	reg.Register(envelope.LocalMethodContainerRemove, newRemoveHandler(rt))
	reg.Register(envelope.LocalMethodContainerLogs, newLogsHandler(rt))
	// 容器详情：只读，能力由可选接口决定；引擎不支持时处理器内返回 unsupported。
	reg.Register(envelope.MethodContainerInspect, newInspectHandler(rt))

	// 镜像 / 网络 / 存储卷：纯代理能力，只要有运行时就能提供。
	reg.Register(envelope.MethodImageList, newImageListHandler(rt))
	reg.Register(envelope.MethodImageRemove, newImageRemoveHandler(rt))
	reg.Register(envelope.MethodNetworkList, newNetworkListHandler(rt))
	reg.Register(envelope.MethodNetworkRemove, newNetworkRemoveHandler(rt))
	reg.Register(envelope.MethodVolumeList, newVolumeListHandler(rt))
	reg.Register(envelope.MethodVolumeRemove, newVolumeRemoveHandler(rt))

	// 镜像传输（拉取 / 导出 / 导入）：与编排完全无关，因此**必须注册在
	// 下面的 composeMgr == nil 提前返回之前**——否则没有编排数据目录时，
	// 镜像传输会连带变成 unsupported，而它本来只需要 data_dir 建 transfers 目录。
	//
	// imageMgr 为 nil 表示没有可用的传输临时区（例如本地 CLI 未给数据目录）：
	// 此时不注册，Hub 得到 unsupported，比"上传到一半才发现没地方写"清楚。
	if imageMgr != nil {
		reg.Register(envelope.MethodImagePull, newImagePullHandler(imageMgr))
		reg.Register(envelope.MethodImageSave, newImageSaveHandler(imageMgr))
		reg.Register(envelope.MethodImageLoadBegin, newImageLoadBeginHandler(imageMgr))
		reg.Register(envelope.MethodImageChunkPut, newImageChunkPutHandler(imageMgr))
		reg.Register(envelope.MethodImageLoadCommit, newImageLoadCommitHandler(imageMgr))
		reg.Register(envelope.MethodImageChunkGet, newImageChunkGetHandler(imageMgr))
		reg.Register(envelope.MethodImageTaskStatus, newImageTaskStatusHandler(imageMgr))
		reg.Register(envelope.MethodImageTaskCancel, newImageTaskCancelHandler(imageMgr))
		reg.Register(envelope.MethodImageTaskRelease, newImageTaskReleaseHandler(imageMgr))

		// 可复用归档导出（编排迁移用）。
		//
		// 与上面的 image.save 同一处注册，理由相同：这些能力只需要
		// data_dir 建 transfers 目录，与编排工作区无关。
		// 但它们的归档**不随任务终态消失**，因此迁移流程能跨阶段持有。
		reg.Register(envelope.MethodImageArchiveExport, newImageArchiveExportHandler(imageMgr))
		reg.Register(envelope.MethodImageArchiveOpen, newImageArchiveOpenHandler(imageMgr))
		reg.Register(envelope.MethodImageArchiveChunkGet, newImageArchiveChunkGetHandler(imageMgr))
		reg.Register(envelope.MethodImageArchiveClose, newImageArchiveCloseHandler(imageMgr))
	}

	// 存储卷文件管理：同样是"有运行时就能提供"的纯代理能力，
	// 因此注册在 composeMgr 提前返回之前。
	// 引擎不支持时处理器内部返回 unsupported（可选接口断言）。
	reg.Register(envelope.MethodVolumeFiles, newVolumeFilesHandler(rt))
	// 编排配置来源读取：只读，不执行 compose、不写工作区，因此不依赖 composeMgr。
	reg.Register(envelope.MethodComposeSource, newComposeSourceHandler(rt))

	if composeMgr == nil {
		return
	}
	// 外部项目发现能力：只有真实引擎实现了它。
	//
	// 为什么用类型断言而不是把它加进 runtime.Runtime 接口：
	// 加进接口会让所有实现（包括测试里的假 runtime）都必须实现它，
	// 而那些假实现根本不关心外部项目。断言失败时降级为"没有发现能力"，
	// 列表仍然正常返回已纳管的编排——这是最小改动。
	var labelSrc containerLabelLister
	if src, ok := rt.(containerLabelLister); ok {
		labelSrc = src
	}
	reg.Register(envelope.MethodComposeList, newComposeListHandler(composeMgr, labelSrc, log))
	reg.Register(envelope.MethodComposeUp, newComposeUpHandler(composeMgr))
	reg.Register(envelope.MethodComposeDown, newComposeDownHandler(composeMgr))
	reg.Register(envelope.MethodComposeStop, newComposeStopHandler(composeMgr))
	reg.Register(envelope.MethodComposeRestart, newComposeRestartHandler(composeMgr))
	reg.Register(envelope.MethodComposePull, newComposePullHandler(composeMgr))
	// 编排镜像更新的**异步流式**版本（用户要看真实命令输出）。
	//
	// 与上面的同步 MethodComposePull 并存：同步版保留给不需要进度展示的
	// 既有调用方，异步版给"更新镜像"弹窗。
	//
	// ============================ 必须共用一个实例 ============================
	// start 与 get 必须操作**同一张任务表**。各自 NewPullManager 会让
	// start 写进 A 实例、get 去 B 实例里查，结果永远是 not_found。
	//
	// 任务的生命周期上下文来自 composeMgr.RootCtx（CLI 注入），
	// 因此 HTTP/WebSocket 断开**不会**取消正在跑的任务。
	// 生命周期：任务挂在 composeMgr.RootCtx（Agent 主循环的 ctx，SIGTERM 时取消）。
	// 落盘到 Registry，由 main 的 defer reg.Close() 触发取消子进程。
	//
	// 这条接线必须真实存在，而不是"方法看起来能关"：
	// 若 composeMgr.RootCtx 为 nil，任务只随进程退出而结束，
	// 优雅退出就**不会**取消正在跑的 compose pull，会留下孤儿进程。
	pullMgr := compose.NewPullManager(composeMgr, composeMgr.RootCtx)
	reg.Register(envelope.MethodComposePullStart, newComposePullStartHandler(pullMgr))
	reg.Register(envelope.MethodComposePullGet, newComposePullGetHandler(pullMgr))
	// 编排动作任务：与 pull 共用同一个任务引擎实例。
	reg.Register(envelope.MethodComposeOperationStart, newComposeOperationStartHandler(pullMgr))
	reg.Register(envelope.MethodComposeOperationGet, newComposeOperationGetHandler(pullMgr))
	reg.AddCloser(pullMgr.Close)
	reg.Register(envelope.MethodComposeRebuild, newComposeRebuildHandler(composeMgr))
	reg.Register(envelope.MethodComposeRemove, newComposeRemoveHandler(composeMgr))
	reg.Register(envelope.MethodComposeBackup, newComposeBackupHandler(composeMgr))
	reg.Register(envelope.MethodComposeLogs, newComposeLogsHandler(composeMgr))
	reg.Register(envelope.MethodComposePS, newComposePSHandler(composeMgr))
	// 外部编排的显式纳管：preview 只读、adopt 只落盘（绝不 up/down）。
	reg.Register(envelope.MethodComposePreview, newComposePreviewHandler(composeMgr, labelSrc))
	reg.Register(envelope.MethodComposeAdopt, newComposeAdoptHandler(composeMgr))

	// 编排跨服务器迁移的 Agent 侧原语。
	//
	// ============================ 为什么放在 composeMgr 分支内 ============================
	// 迁移的输入是"这个编排用了哪些卷"，而那需要读取已落盘的编排配置；
	// 没有 composeMgr 就没有编排，也就没有可迁移的东西。
	// 目标端只需要 compose.import，它同样依赖工作区。
	//
	// transferDir 为空时只注册 compose.import（不需要临时区），
	// 卷归档与导入需要落盘空间，因此依赖它存在。
	migrateReg := registerMigrateHandlers(reg, composeMgr, transferDir, log)
	if migrateReg == nil {
		// ============================ 这条警告必须说清后果与修法 ============================
		// 实测踩过：服务模式调错入口，transferDir 传了空，于是卷迁移方法
		// 根本没注册。界面上"同时迁移数据卷"能勾选、点了才报方法不存在，
		// 用户与运维都很难从中看出是**启动装配**的问题。
		//
		// 所以这里不只说"缺少目录"，而要点明：这是配置/装配问题、
		// 影响哪个功能、该去查哪里。
		log.Warn("未注册数据卷迁移方法（volume.archive.* / volume.import.*）："+
			"传输临时目录为空。勾选「同时迁移数据卷」的迁移会在调用时失败。",
			"提示", "请检查服务启动时是否调用了 RegisterDefaultsFull 并传入有效的 data_dir")
	}
}
