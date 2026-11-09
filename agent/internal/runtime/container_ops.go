package runtime

// container_ops.go 承载容器的重启与删除两个操作。
//
// 为什么单独成文件而不并入 engine.go：这两个操作都有"危险的默认值"问题
// （force 会直接杀掉进程、v=true 会删掉数据卷），相关的中文错误翻译
// 也需要成段说明理由。放在一处便于将来审查"危险操作"时一眼找全。

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

// Restart 重启容器。
//
// 为什么用 ?t= 而不是无参调用：运行时的 restart 默认超时是 10 秒，
// 但某些应用（数据库、Java 服务）优雅退出需要更久。显式传超时让这个
// 数值成为我们自己的决策而不是隐含在引擎默认值里，将来要调也只改一处。
//
// 注意 restart 的真实语义：运行时先 stop 再 start。如果容器本来就没运行，
// Docker 会报 304 Not Modified（不算错误）；这个状态 client 视为 2xx 处理，
// 因此"重启一个已停止的容器"对用户表现为成功——这是符合直觉的：
// 用户要的结果是"它现在跑起来了"。
func (e *engine) Restart(ctx context.Context, idOrName string) error {
	if err := validateRef(idOrName); err != nil {
		return err
	}
	p := containerPath(idOrName) + actionRestart + "?t=" + strconv.Itoa(defaultStopTimeoutSec)
	return e.translateContainerOp(e.client.post(ctx, p), "重启")
}

// RemoveContainer 删除容器。
//
// 参数语义（两个都必须显式传，不给"隐式危险默认"的机会）：
//   - force：容器正在运行时必须为 true 才能删除，否则运行时返回 409。
//     这正是本任务强调的点——不支持 force 的话，运行中的容器根本删不掉。
//   - removeVolumes：对应运行时的 v=true，会**连同匿名卷一起删除**。
//     这是数据丢失操作，调用方必须显式要求，默认绝不开。
//
// 为什么方法名带 Container 后缀：Runtime 接口里已经有一个抽象的 Remove
// （历史遗留，签名只有 force），两个同义方法并存极易误用。这里用
// RemoveContainer 把"容器"这个对象写进名字，避免调用方选错
// （同样的教训在 Hub 侧 ListUserGrants/ListGroupGrants 上踩过）。
func (e *engine) RemoveContainer(ctx context.Context, idOrName string, force, removeVolumes bool) error {
	if err := validateRef(idOrName); err != nil {
		return err
	}
	// 参数按需拼接而不是恒定带两个 false：让运行时的 URL 与用户意图一致，
	// 排查日志时一看就知道这次删有没有带 force。
	params := make([]string, 0, 2)
	if force {
		params = append(params, "force=1")
	}
	if removeVolumes {
		// Docker 用 v=1 表示"同时删除关联的匿名卷"。
		params = append(params, "v=1")
	}
	p := containerPath(idOrName)
	if len(params) > 0 {
		p += "?" + joinParams(params)
	}
	return e.translateContainerOp(e.client.delete(ctx, p), "删除")
}

// joinParams 用 & 拼接查询参数。
func joinParams(params []string) string {
	out := ""
	for i, p := range params {
		if i > 0 {
			out += "&"
		}
		out += p
	}
	return out
}

// errContainerInUse 表示容器正在运行导致删除被拒（运行时返回 409）。
var errContainerInUse = errors.New("容器正在运行")

// translateContainerOp 把容器操作的状态错误翻译成可读中文。
//
// 为什么要单独一层而不复用 engine.translate：
//   - translate 只处理 404，而容器操作最常见的失败恰恰是 409（运行中不能删）
//     与 304/500（引擎内部错误）；
//   - 用户看到的必须是「容器正在运行，请先停止或强制删除」这种能照做的提示，
//     而不是 Docker 原文 "You cannot remove a running container ... Stop the
//     container before attempting removal or force remove"。
//
// op 是操作名（"删除"/"重启"），用于拼出"删除容器失败：..."的完整句式。
func (e *engine) translateContainerOp(err error, op string) error {
	if err == nil {
		return nil
	}
	var se *statusError
	if !errors.As(err, &se) {
		// 网络层错误（连不上引擎）原样返回，上层会翻成 runtime_failed。
		return err
	}

	switch se.Status {
	case 404:
		return fmt.Errorf("%w: 容器不存在或已被删除", ErrNotFound)
	case 409:
		if op == "删除" {
			// 这是用户最常撞上的错误，必须给可操作的建议。
			// 用 %w 包上 errContainerInUse，让上层能用 errors.Is 判断，
			// 也给前端留出"一键改成强制删除"的可能。
			return fmt.Errorf("%w：%w，请先停止容器或勾选「强制删除」", ErrContainerConflict, errContainerInUse)
		}
		return fmt.Errorf("%w：容器当前状态不允许此操作（%s）", ErrContainerConflict, se.Body)
	case 500:
		// 500 常见于容器已被别的进程删除但有残留引用，或引擎自身异常。
		// 附上运行时原文——这类错误的具体原因只有引擎知道。
		return fmt.Errorf("%s容器失败：%s", op, se.Body)
	}
	return err
}
