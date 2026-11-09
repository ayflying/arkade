package handler

// compose_adopt.go 实现 compose.preview 与 compose.adopt 两个方法。
//
// 分层与其它 compose 方法一致：handler 只做"解码 → 校验参数 → 调 Manager → 转结果"，
// 所有安全判定都在 compose 包里（便于单测，也避免两处规则打架）。
//
// 两个方法的关键差异：
//   - preview 只读：不改任何文件，回答"能不能导入、为什么不能"；
//   - adopt 只落盘：写管理区的 compose.yml/.env，**绝不 up/down/pull**。
//
// 之所以把"绝不启动"写进方法语义而不是加个参数开关：
// 导入的对象是正在跑生产负载的外部项目，一旦"顺手 up 一下"，
// 就会用新配置重建那些容器——那是用户没有要求的破坏。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"arkade/agent/internal/compose"
	"arkade/protocol/envelope"
)

// decodePayloadInto 把请求载荷解码到给定结构体。
//
// 与 decodeComposeSpec 的区别：这里**不**校验 content 非空。
// 原因：preview 允许内容为空（结论会是"需要用户提供内容"而不是参数错误），
// 这样前端在用户还没粘贴内容时也能正常调预检、拿到"为什么不能导入"。
// adopt 对内容为空有自己的明确报错（由 Manager 给出）。
func decodePayloadInto(payload []byte, dst any) error {
	if len(payload) == 0 || string(payload) == "null" {
		return badRequestf("缺少载荷：需要 name 与 content")
	}
	if err := json.Unmarshal(payload, dst); err != nil {
		return badRequestf("载荷格式错误: %v", err)
	}
	return nil
}

// newComposePreviewHandler 处理 compose.preview。
//
// labelSrc 用于发现外部项目。为 nil 时（测试里的假 runtime）
// 仍然能工作：此时"引擎里没有该项目"，结论是 can_import=false，
// 原因写明"没有可导入的对象"——这是如实回答，不是异常。
func newComposePreviewHandler(mgr *compose.Manager, labelSrc containerLabelLister) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ComposePreviewPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		name, nerr := normalizeComposeName(p.Name)
		if nerr != nil {
			return nil, nerr
		}

		res, err := mgr.Preview(ctx, compose.PreviewInput{
			Name:       name,
			Content:    p.Content,
			EnvContent: p.EnvContent,
		})
		if err != nil {
			return nil, badRequestf("%v", err)
		}

		out := envelope.ComposePreviewResult{
			Name:            res.Name,
			Discovered:      res.Discovered,
			EngineExists:    res.EngineExists,
			WorkspaceExists: res.WorkspaceExists,
			Source:          res.Source,
			Running:         res.Running,
			Total:           res.Total,
			CanImport:       res.CanImport,
			ConfigHash:      res.ConfigHash,
			Blockers:        res.Blockers,
			Warnings:        res.Warnings,
		}
		// nil 切片转成空切片：前端拿到 [] 比拿到 null 更好处理，
		// 也避免"字段缺失"与"空列表"两种表示混在一起。
		if out.Blockers == nil {
			out.Blockers = []string{}
		}
		if out.Warnings == nil {
			out.Warnings = []string{}
		}
		return out, nil
	}
}

// newComposeAdoptHandler 处理 compose.adopt。
func newComposeAdoptHandler(mgr *compose.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ComposeAdoptPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		name, nerr := normalizeComposeName(p.Name)
		if nerr != nil {
			return nil, nerr
		}

		res, err := mgr.Adopt(ctx, compose.AdoptInput{
			Name:       name,
			Content:    p.Content,
			EnvContent: p.EnvContent,
			ExpectHash: p.ExpectedContentHash,
			Confirmed:  p.Confirmation,
		})
		if err != nil {
			return nil, badRequestf("%v", err)
		}

		// 状态回传当前项目状态；导入本身不启动任何东西，
		// 因此这里的状态反映的是"引擎里那些容器现在怎么样"，
		// 不是"我们刚刚启动了什么"。
		status := "unknown"
		if st, serr := mgr.Status(ctx, name); serr == nil {
			status = st.Status
		}

		return envelope.ComposeAdoptResult{
			Name:       res.Name,
			Status:     status,
			ConfigHash: res.ConfigHash,
			Output:     adoptSummary(res),
			Adopted:    res.Adopted,
			EnvWritten: res.EnvWritten,
		}, nil
	}
}

// adoptSummary 生成给用户看的一句话结论。
//
// 刻意强调"未启动任何容器"：这是导入与"新建并启动"的核心区别，
// 用户必须一眼看到，否则会以为导入顺带把服务重启了。
func adoptSummary(res compose.AdoptResult) string {
	parts := []string{fmt.Sprintf("已纳管编排 %s，未启动或重启任何容器", res.Name)}
	if res.EnvWritten {
		parts = append(parts, "已保存环境变量文件（权限 0600）")
	} else {
		parts = append(parts, "本次未设置环境变量文件")
	}
	parts = append(parts, "后续可用同一编排名进行启动/停止等操作")
	return strings.Join(parts, "；")
}
