package handler

// compose_source.go：compose.source 处理器。
//
// 只读：读回项目配置内容 + 哈希，**不执行 compose、不写工作区、不接管**。
// 接管动作全部由 Hub 负责（Hub 侧 Source → Preview → DB → Adopt）。
//
// ============================ 为什么这里还要再扫一次 ============================
//
// runtime 层只负责"安全地把文件读出来"，不懂 compose 语义。
// 而一份配置文件即使读得到，也可能含 env_file / build / 相对挂载
// 等导入后行为会变的写法。ScanImportSafety 就是判据。
//
// 在 Agent 侧**返回前**再过滤一次（而不是只让 Hub 判）：
//   - Hub 与 Agent 版本可能不同步，Agent 不能假设调用方会做这个检查；
//   - blocker 文案由我方固定生成（只含字段名与行号），
//     不会把用户配置里的标量值带进 Hub 数据库或日志。

import (
	"context"
	"errors"

	"arkade/agent/internal/compose"
	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// newComposeSourceHandler 构造配置来源读取处理器。
func newComposeSourceHandler(rt runtime.Runtime) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ComposeSourcePayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		name, nerr := compose.CleanNameForSource(p.Name)
		if nerr != nil {
			return nil, badRequestf("%v", nerr)
		}

		cap, ok := rt.(runtime.ComposeSourceRuntime)
		if !ok {
			return nil, envelope.NewError(envelope.CodeUnsupported, "当前引擎不支持读取编排配置来源")
		}

		content, err := cap.RunComposeSource(ctx, name)
		if err != nil {
			// 预期内的"这个项目当前不能接管"必须作为 blocker 如实返回，
			// 否则 Hub 只能显示"请检查版本/镜像/权限"这种通用提示，
			// 用户按它查一圈也找不到真正原因（多文件、标签缺失、软链…）。
			var blocked *runtime.SourceBlockerError
			if errors.As(err, &blocked) {
				return envelope.ComposeSourceResult{
					Name:     name,
					Blockers: []string{blocked.Reason},
				}, nil
			}
			return nil, translateSourceError(err)
		}

		out := envelope.ComposeSourceResult{Name: name}
		if content != "" {
			// 哈希只覆盖 content：本方法不读 .env。
			hash := compose.HashProject(content, "")
			// 返回前再过滤一次：导入后行为会变的写法在这里就明确列出。
			blockers := compose.ScanImportSafety(content)
			if len(blockers) > 0 {
				// 有阻塞时不回传内容：这份配置本来就不能接管，
				// 把原文传上去只会让敏感内容在 Hub 多存一份，
				// 而且前端拿到一份"不能用"的配置也没有意义。
				for _, b := range blockers {
					out.Blockers = append(out.Blockers, b.String())
				}
				out.ConfigHash = hash
				return out, nil
			}
			out.Content = content
			out.ConfigHash = hash
		}
		return out, nil
	}
}

// translateSourceError 把 runtime 错误转成带协议码的中文错误。
//
// 走到这里的一定是**真正的故障**（引擎不可达、超时、忙、缺镜像），
// "项目不能接管"的预期原因已经在调用处转成 Blockers。
func translateSourceError(err error) error {
	switch {
	case errors.Is(err, runtime.ErrHelperImageMissing):
		return envelope.NewError(envelope.CodeUnsupported,
			"文件助手镜像未安装，请先在本机拉取 "+runtime.DefaultHelperImage)
	case errors.Is(err, runtime.ErrHelperBusy):
		return envelope.NewError(envelope.CodeAgentBusy, "操作繁忙（同时最多 2 个），请稍后重试")
	case errors.Is(err, runtime.ErrComposeSourceUnavailable):
		// 兜底：若将来有新路径直接返回哨兵而非 SourceBlockerError，
		// 也不能落成"读取失败"，至少给出大类原因。
		return envelope.NewError(envelope.CodeNotFound, err.Error())
	case errors.Is(err, runtime.ErrComposeSourceUnsafePath):
		return envelope.NewError(envelope.CodeConflict, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return envelope.NewError(envelope.CodeTimeout, "读取编排配置超时")
	}
	return envelope.NewError(envelope.CodeRuntimeFailed, "读取编排配置失败")
}
