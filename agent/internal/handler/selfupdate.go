package handler

// selfupdate.go：self.update 处理器。
//
// ============================ 这个处理器只回答"已提交" ============================
//
// 真正的重建由一次性容器异步完成，本处理器返回后我方容器很快会被替换掉。
// 因此这里**没有**"等待结果"的分支——那个分支永远走不到，
// 写了只会让后来的人以为能等到。
//
// 返回分三种：
//   - Blockers 非空：这个部署方式不能自更新（结论，不是故障）；
//   - Accepted=true：已提交，容器即将被重建；
//   - error：真正的故障（引擎不可达、创建容器失败）。

import (
	"context"
	"errors"
	"strings"
	"sync"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// newSelfUpdateHandler 构造自更新处理器。
//
// 与卷文件/配置来源一样属于"有引擎就能提供"的能力，因此注册在
// composeMgr 分支之外；引擎不支持时由可选接口断言兜底成 unsupported。
func newSelfUpdateHandler(rt runtime.Runtime, allowManaged bool) Handler {
	var mu sync.Mutex
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.SelfUpdatePayload
		// JSON 类型错误可能带上不可信的配置字段，统一返回固定诊断。
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, badRequestf("自更新载荷无效")
		}
		requestID := strings.TrimSpace(p.RequestID)
		if p.Probe {
			if requestID != "" || p.Managed != nil {
				return nil, badRequestf("探测不能携带更新请求 ID 或纳管配置")
			}
		} else if requestID == "" {
			return nil, badRequestf("request_id 不能为空")
		}
		if p.Managed != nil && !allowManaged {
			return nil, envelope.NewError(envelope.CodeUnsupported, "纳管配置自更新只能由 Hub 内嵌执行器执行")
		}

		cap, ok := rt.(runtime.SelfUpdateRuntime)
		if !ok {
			return nil, envelope.NewError(envelope.CodeUnsupported, "当前引擎不支持自更新")
		}

		info, err := cap.SelfContainerInfo(ctx)
		if err != nil {
			return selfUpdateBlockerResult(err)
		}

		if p.Probe {
			return envelope.SelfUpdateResult{Project: info.Project, Service: info.Service, CurrentImage: info.ImageRef}, nil
		}
		if p.Managed != nil && p.Managed.Project != info.Project {
			return selfUpdateBlockerResult(&runtime.SelfUpdateBlocker{Reason: "纳管配置的项目与当前容器所属项目不一致，已拒绝自更新"})
		}

		// 探测不加锁；只有清理与创建需要串行，避免另一请求在
		// 本次 PUT 归档/启动的间隙把刚创建的助手当成残留。
		mu.Lock()
		defer mu.Unlock()
		if err := ctx.Err(); err != nil {
			return selfUpdateBlockerResult(err)
		}
		name := envelope.SelfUpdateHelperName(requestID)
		id, err := cap.RunDetachedComposeUp(ctx, runtime.DetachedComposeUp{
			ImageID:    info.ImageID,
			Project:    info.Project,
			WorkingDir: info.WorkingDir,
			ConfigFile: info.ConfigFile,
			Service:    info.Service,
			Name:       name,
			RequestID:  requestID,
			Managed:    p.Managed,
		})
		if err != nil {
			return selfUpdateBlockerResult(err)
		}

		return envelope.SelfUpdateResult{
			Accepted:            true,
			Project:             info.Project,
			Service:             info.Service,
			WorkingDir:          info.WorkingDir,
			ConfigFile:          info.ConfigFile,
			CurrentImage:        info.ImageRef,
			HelperContainerID:   id,
			HelperContainerName: name,
			Message:             "已开始更新：正在拉取镜像，随后重建容器，连接会短暂中断",
		}, nil
	}
}

// selfUpdateBlockerResult 把错误分流成"结论"或"故障"。
//
// "不能自更新"（*runtime.SelfUpdateBlocker）是**结论**，必须作为
// Blockers 原样回传：前端要能告诉用户"为什么这台机器点不了更新"，
// 而不是笼统报一句"更新失败"。其余才是真故障。
func selfUpdateBlockerResult(err error) (any, error) {
	var blocked *runtime.SelfUpdateBlocker
	if errors.As(err, &blocked) {
		return envelope.SelfUpdateResult{Blockers: []string{blocked.Reason}}, nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, envelope.NewError(envelope.CodeTimeout, "自更新超时")
	}
	return nil, envelope.NewError(envelope.CodeRuntimeFailed, "自更新失败："+err.Error())
}
