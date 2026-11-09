package handler

// compose.go 是编排类方法的处理器。
//
// 职责划分：这里只做「载荷解码 + 参数校验 + 把 Manager 结果翻译成协议结构」，
// 不碰命令也不碰文件——那些都在 internal/compose 里。
// 这样 CLI 与 Hub 两条调用路径共用同一份业务逻辑，行为不会分裂。

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"arkade/agent/internal/compose"
	"arkade/protocol/envelope"
)

// composeManager 由注册函数注入。
//
// 为什么不用全局变量：测试需要构造指向临时目录的 Manager，
// 全局变量会让测试之间互相污染（并行跑时尤其明显）。

// composeSpecPayload 解码 compose.up 的载荷。
func decodeComposeSpec(payload []byte) (envelope.ComposeSpecPayload, error) {
	var p envelope.ComposeSpecPayload
	if len(payload) == 0 || string(payload) == "null" {
		return p, badRequestf("缺少载荷：需要 name 与 content")
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return p, badRequestf("载荷格式错误: %v", err)
	}
	if strings.TrimSpace(p.Content) == "" {
		return p, badRequestf("编排内容不能为空")
	}
	return p, nil
}

// decodeComposeOp 解码编排操作载荷（除 up 以外的动作）。
func decodeComposeOp(payload []byte) (envelope.ComposeOpPayload, error) {
	var p envelope.ComposeOpPayload
	if len(payload) == 0 || string(payload) == "null" {
		return p, badRequestf("缺少载荷：需要 name")
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return p, badRequestf("载荷格式错误: %v", err)
	}
	return p, nil
}

// newComposeUpHandler 处理 compose.up。
//
// 语义：把内容落盘 + 启动。返回值包含本机算出的哈希，
// Hub 用它和库里存的哈希比对以判定 drift——所以这个哈希必须来自
// **实际落盘的内容**而不是请求里的声明值。
func newComposeUpHandler(mgr *compose.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		p, err := decodeComposeSpec(payload)
		if err != nil {
			return nil, err
		}
		name, nerr := normalizeComposeName(p.Name)
		if nerr != nil {
			return nil, nerr
		}
		res, err := mgr.Apply(ctx, name, p.Content, p.ContentHash)
		if err != nil {
			return nil, badRequestf("%v", err)
		}
		return composeOpResult(ctx, mgr, name), opErrorFromResult(name, res)
	}
}

// newComposeDownHandler 处理 compose.down。
//
// remove_volumes 默认 false：停止编排不该顺手删数据卷。
// 删除编排（remove）才强制带上。
func newComposeDownHandler(mgr *compose.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		p, err := decodeComposeOp(payload)
		if err != nil {
			return nil, err
		}
		name, nerr := normalizeComposeName(p.Name)
		if nerr != nil {
			return nil, nerr
		}
		res, err := mgr.Down(ctx, name, p.RemoveVolumes)
		if err != nil {
			return nil, badRequestf("%v", err)
		}
		return composeOpResult(ctx, mgr, name), opErrorFromResult(name, res)
	}
}

// newComposeStopHandler 处理 compose.stop。
func newComposeStopHandler(mgr *compose.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		p, err := decodeComposeOp(payload)
		if err != nil {
			return nil, err
		}
		name, nerr := normalizeComposeName(p.Name)
		if nerr != nil {
			return nil, nerr
		}
		res, err := mgr.Stop(ctx, name, p.Services)
		if err != nil {
			return nil, badRequestf("%v", err)
		}
		return composeOpResult(ctx, mgr, name), opErrorFromResult(name, res)
	}
}

// newComposeRestartHandler 处理 compose.restart。
func newComposeRestartHandler(mgr *compose.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		p, err := decodeComposeOp(payload)
		if err != nil {
			return nil, err
		}
		name, nerr := normalizeComposeName(p.Name)
		if nerr != nil {
			return nil, nerr
		}
		res, err := mgr.Restart(ctx, name, p.Services)
		if err != nil {
			return nil, badRequestf("%v", err)
		}
		return composeOpResult(ctx, mgr, name), opErrorFromResult(name, res)
	}
}

// newComposePullHandler 处理 compose.pull。
func newComposePullHandler(mgr *compose.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		p, err := decodeComposeOp(payload)
		if err != nil {
			return nil, err
		}
		name, nerr := normalizeComposeName(p.Name)
		if nerr != nil {
			return nil, nerr
		}
		res, err := mgr.Pull(ctx, name)
		if err != nil {
			return nil, badRequestf("%v", err)
		}
		return composeOpResult(ctx, mgr, name), opErrorFromResult(name, res)
	}
}

// newComposeRebuildHandler 处理 compose.rebuild。
func newComposeRebuildHandler(mgr *compose.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		p, err := decodeComposeOp(payload)
		if err != nil {
			return nil, err
		}
		name, nerr := normalizeComposeName(p.Name)
		if nerr != nil {
			return nil, nerr
		}
		res, err := mgr.Rebuild(ctx, name)
		if err != nil {
			return nil, badRequestf("%v", err)
		}
		return composeOpResult(ctx, mgr, name), opErrorFromResult(name, res)
	}
}

// newComposeRemoveHandler 处理 compose.remove。
//
// 这是唯一会删数据卷的编排动作。Hub 侧在派发前已校验 manage 权限，
// 这里不再重复做权限判断（Agent 没有用户概念，只有连接身份）。
func newComposeRemoveHandler(mgr *compose.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		p, err := decodeComposeOp(payload)
		if err != nil {
			return nil, err
		}
		name, nerr := normalizeComposeName(p.Name)
		if nerr != nil {
			return nil, nerr
		}
		res, err := mgr.Remove(ctx, name)
		if err != nil {
			return nil, badRequestf("%v", err)
		}
		out := envelope.ComposeOpResult{
			Name:   name,
			Status: compose.StatusUnknown,
			Output: res.Output,
		}
		// 删除后项目已不存在，状态无从探测，直接返回 unknown 即可。
		return out, opErrorFromResult(name, res)
	}
}

// newComposeLogsHandler 处理 compose.logs。
func newComposeLogsHandler(mgr *compose.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ComposeLogsPayload
		if len(payload) == 0 || string(payload) == "null" {
			return nil, badRequestf("缺少载荷：需要 name")
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, badRequestf("载荷格式错误: %v", err)
		}
		name, nerr := normalizeComposeName(p.Name)
		if nerr != nil {
			return nil, nerr
		}
		res, err := mgr.Logs(ctx, name, p.Service, p.TailLines)
		if err != nil {
			return nil, badRequestf("%v", err)
		}
		if res.ExitCode != 0 {
			// 日志拉取失败要如实报错：返回空日志会让用户以为"服务没输出"。
			return nil, wrapComposeFailure(name, res)
		}
		lines := splitLines(res.Output)
		return envelope.ComposeLogsResult{
			Lines:     lines,
			Truncated: strings.HasPrefix(res.Output, "…"),
		}, nil
	}
}

// newComposePSHandler 处理 compose.ps。
//
// 返回结构复用 ContainerListResult：编排下的容器与普通容器是同一类东西，
// 前端可以复用同一套展示逻辑，协议也不必为它再定义一套平行结构。
func newComposePSHandler(mgr *compose.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		p, err := decodeComposeOp(payload)
		if err != nil {
			return nil, err
		}
		name, nerr := normalizeComposeName(p.Name)
		if nerr != nil {
			return nil, nerr
		}
		res, err := mgr.PS(ctx, name)
		if err != nil {
			return nil, badRequestf("%v", err)
		}
		if res.ExitCode != 0 {
			return nil, wrapComposeFailure(name, res)
		}
		return composePSResult(res.Stdout), nil
	}
}

// newComposeListHandler 处理 compose.list。
//
// 一次返回本机全部项目及其状态。Hub 用它和数据库记录做合并，
// 因此**必须包含本机实际算出的哈希**——那是 drift 判定的依据。
//
// 另返回「引擎里有、但 Arkade 工作区没有」的外部项目（External）。
// 这些项目只能看不能改：不读它们的配置文件、不执行任何 up/down、
// 不导入、不接管。见 compose/external.go 的设计边界说明。
//
// 外部发现失败**不能**让整个列表失败：用户要的首先是"我管理的编排"，
// 那部分数据是好的，不该因为另一个查询出错就整体消失。
// 因此外部发现失败时降级为空列表并记日志——但**不能假装机器上真的没有**
// （那正是本次 bug 的同类错误），所以把错误写进日志让运维能查到。
func newComposeListHandler(mgr *compose.Manager, src containerLabelLister, log *slog.Logger) Handler {
	return func(ctx context.Context, _ []byte) (any, error) {
		projects, err := mgr.List(ctx)
		if err != nil {
			return nil, badRequestf("%v", err)
		}
		managed := make([]envelope.ComposeProjectSummary, 0, len(projects))
		managedNames := make(map[string]struct{}, len(projects))
		for _, p := range projects {
			managed = append(managed, toProjectSummary(p))
			managedNames[p.Name] = struct{}{}
		}

		// 外部项目：只读标签发现。
		var external []envelope.ComposeProjectSummary
		if src != nil {
			found, derr := compose.DiscoverExternal(ctx, src)
			if derr != nil {
				// 降级但不掩埋：日志里留痕，便于运维发现"引擎探测失败"。
				log.Warn("发现外部编排项目失败，本次只返回已纳管的编排", "err", derr)
			} else {
				external = toExternalSummaries(found, managedNames)
			}
		}

		return envelope.ComposeListResult{Projects: managed, External: external}, nil
	}
}

// containerLabelLister 是发现外部项目所需的最小能力。
//
// 用窄接口而不是 *runtime.Engine：这个 handler 只读容器标签，
// 不该依赖整个引擎接口，测试里也只需要一个假实现。
type containerLabelLister interface {
	ListContainerLabels(ctx context.Context) ([]compose.ContainerLabels, error)
}

// toExternalSummaries 把外部项目转成摘要，并剔除与已纳管项目重名的项。
//
// 为什么要去重：如果某个项目既是 Arkade 工作区里的（有目录），
// 又在引擎里有容器，它会同时出现在两侧。**必须以已纳管为准**——
// 否则界面会显示两条同名项目，一条能编辑一条不能，
// 用户完全无法判断该点哪一个，而且容易对生产环境误操作。
func toExternalSummaries(found []compose.ExternalProject, managed map[string]struct{}) []envelope.ComposeProjectSummary {
	out := make([]envelope.ComposeProjectSummary, 0, len(found))
	for _, p := range found {
		if _, ok := managed[p.Name]; ok {
			continue
		}
		out = append(out, envelope.ComposeProjectSummary{
			Name: p.Name,
			// Status 与已纳管项目同一套取值，前端可复用渲染逻辑。
			Status: p.Status(),
			// Hash 恒空：外部项目没有我们能算的配置，
			// 而且**不能拿它参与 drift 判定**——空哈希与任何值比较
			// 都只会产生误报。Hub 侧据此跳过 drift 检查。
			ConfigHash: "",
			Services:   p.Services,
			Running:    p.Running,
			Total:      p.Total,
			// UpdatedAt 为 0：我们没读过它的文件，不该编造时间。
			UpdatedAt: 0,
			Managed:   false,
		})
	}
	return out
}

// newComposeBackupHandler 处理 compose.backup。
func newComposeBackupHandler(mgr *compose.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ComposeBackupPayload
		if len(payload) == 0 || string(payload) == "null" {
			return nil, badRequestf("缺少载荷：需要 name")
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, badRequestf("载荷格式错误: %v", err)
		}
		name, nerr := normalizeComposeName(p.Name)
		if nerr != nil {
			return nil, nerr
		}
		res, err := mgr.Backup(ctx, name, p.IncludeVolumes)
		if err != nil {
			return nil, badRequestf("%v", err)
		}
		return envelope.ComposeBackupResult{
			Name:      res.Name,
			Path:      res.Path,
			SizeBytes: res.SizeBytes,
			Files:     res.Files,
			CreatedAt: res.CreatedAt,
		}, nil
	}
}

// normalizeComposeName 校验并规范化项目名。
//
// 单独一层是为了把 compose 包的错误信息统一加上"编排项目名"前缀，
// 让用户在界面上一眼看出是哪一类参数错了。
func normalizeComposeName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if err := compose.ValidateName(name); err != nil {
		return "", badRequestf("%v", err)
	}
	return name, nil
}

// opErrorFromResult 把命令执行结果转成错误。
//
// 关键约定：compose 命令退出码非 0 时，**必须向 Hub 报错**而不是返回成功。
// 否则用户会在界面上看到"操作成功"但服务实际没起来——
// 这是最伤信任的一类 bug。
//
// 同时把命令输出放进错误的 Message 里：用户需要看到真实的失败原因
// （端口被占用、镜像拉不到、YAML 语法错），而不是一句笼统的"操作失败"。
func opErrorFromResult(name string, res compose.Result) error {
	if res.ExitCode == 0 {
		return nil
	}
	return wrapComposeFailure(name, res)
}

// wrapComposeFailure 构造带命令输出的错误。
func wrapComposeFailure(name string, res compose.Result) error {
	reason := "命令执行失败"
	if res.TimedOut {
		reason = "命令执行超时"
	}
	out := strings.TrimSpace(res.Output)
	if out == "" {
		out = "（命令没有输出任何内容）"
	}
	return fmt.Errorf("%w: 编排 %s %s：%s", errErrComposeFailed, name, reason, out)
}

// errErrComposeFailed 是编排命令失败的哨兵，供错误翻译层识别。
var errErrComposeFailed = errComposeFailed{}

// errComposeFailed 是编排执行失败的错误类型。
//
// 用独立类型而不是 errors.New：翻译层要把它映射成 runtime_failed
// 而不是 internal——失败原因是"被管理的机器上命令没跑通"，
// 不是 Agent 自身的程序缺陷，这个区分决定了运维会不会被误报警。
type errComposeFailed struct{}

func (errComposeFailed) Error() string { return "编排命令执行失败" }

// composeOpResult 在操作后回读一次状态。
//
// 与容器操作同样的理由：命令返回 0 只代表"命令跑完了"，
// 服务实际是否起来了要看 ps。回读失败不算操作失败（操作本身已成功），
// 此时状态留空由 Hub 侧下次列表补齐。
func composeOpResult(ctx context.Context, mgr *compose.Manager, name string) envelope.ComposeOpResult {
	out := envelope.ComposeOpResult{Name: name}
	// 给容器启动留一点时间，避免刚 up 完立刻查 ps 拿到还没起来的中间态。
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	st, err := mgr.Status(readCtx, name)
	if err != nil {
		return out
	}
	out.Status = st.Status
	out.ConfigHash = st.Hash
	return out
}

// toProjectSummary 把本机项目状态转成协议结构。
func toProjectSummary(p compose.ProjectStatus) envelope.ComposeProjectSummary {
	services := p.Services
	if services == nil {
		services = []string{}
	}
	return envelope.ComposeProjectSummary{
		Name:       p.Name,
		Status:     p.Status,
		ConfigHash: p.Hash,
		Services:   services,
		Running:    p.Running,
		Total:      p.Total,
		UpdatedAt:  p.UpdatedAt.Unix(),
		// 工作区里的项目都由 Arkade 管理：有目录、配置由 Hub 下发、
		// 可编辑可删除。
		Managed: true,
	}
}

// splitLines 把文本按行拆分并去掉空行噪声。
func splitLines(s string) []string {
	s = strings.TrimRight(s, "\r\n")
	if s == "" {
		return []string{}
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, strings.TrimRight(l, "\r"))
	}
	return out
}
