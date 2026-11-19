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

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// newSelfUpdateHandler 构造自更新处理器。
//
// 与卷文件/配置来源一样属于"有引擎就能提供"的能力，因此注册在
// composeMgr 分支之外；引擎不支持时由可选接口断言兜底成 unsupported。
func newSelfUpdateHandler(rt runtime.Runtime) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.SelfUpdatePayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		requestID := strings.TrimSpace(p.RequestID)
		if requestID == "" {
			return nil, badRequestf("request_id 不能为空")
		}

		cap, ok := rt.(runtime.SelfUpdateRuntime)
		if !ok {
			return nil, envelope.NewError(envelope.CodeUnsupported, "当前引擎不支持自更新")
		}

		info, err := cap.SelfContainerInfo(ctx)
		if err != nil {
			return selfUpdateBlockerResult(err)
		}

		// 先清掉上一次失败留下的容器：它们占着名字，也可能还在
		// 尝试重建。放在探测之后、创建之前，避免误删"正在跑的这一次"。
		// 清理失败不影响本次更新（残留最多留到下次再清）。
		_, _ = cap.CleanupSelfUpdateHelpers(ctx, "")

		name := runtime.SelfUpdateHelperName(requestID)
		id, err := cap.RunDetachedComposeUp(ctx, runtime.DetachedComposeUp{
			ImageID:    info.ImageID,
			Project:    info.Project,
			WorkingDir: info.WorkingDir,
			ConfigFile: info.ConfigFile,
			Service:    info.Service,
			Name:       name,
			RequestID:  requestID,
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
