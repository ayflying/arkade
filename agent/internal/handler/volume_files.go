package handler

// volume_files.go：存储卷文件 CRUD 的处理器。
//
// 实现方式见 runtime/volume_helper.go。这里只做参数校验、调用可选能力、
// 以及把 helper 的结果转成协议载荷。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"arkade/agent/internal/helperfs"
	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// maxVolumeNameLen 限制卷名长度。
const maxVolumeNameLen = 255

// newVolumeFilesHandler 构造卷文件操作处理器。
func newVolumeFilesHandler(rt runtime.Runtime) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		// 载荷可以是空对象：write 写空文件时 DataBase64 省略也是合法的
		// （json omitempty 会让字段完全不出现）。
		// 因此这里不要求载荷非空，只要不是垃圾 JSON 即可。
		var p envelope.VolumeFilesPayload
		if len(payload) > 0 && string(payload) != "null" {
			if err := json.Unmarshal(payload, &p); err != nil {
				return nil, badRequestf("载荷格式错误")
			}
		}
		if err := validateVolumeFilesPayload(p); err != nil {
			return nil, err
		}
		cap, ok := rt.(runtime.VolumeFileRuntime)
		if !ok {
			return nil, fmt.Errorf("%w: 当前引擎不支持存储卷文件管理", runtime.ErrUnsupported)
		}

		req := map[string]any{
			"operation":   p.Operation,
			"path":        p.Path,
			"data_base64": p.DataBase64,
		}
		if p.Destination != "" {
			req["destination"] = p.Destination
		}
		if p.ExpectedSHA256 != "" {
			req["expected_sha256"] = p.ExpectedSHA256
		}
		if p.CreateOnly {
			req["create_only"] = true
		}

		rawResp, err := cap.RunVolumeFileHelper(ctx, p.Volume, req)
		if err != nil {
			return nil, translateVolumeError(err)
		}

		var wire helperfs.WireResponse
		if jerr := json.Unmarshal(rawResp, &wire); jerr != nil {
			return nil, errors.New("存储卷助手返回了无法解析的结果")
		}
		if wire.Error != "" {
			// helper 已给出固定的中文说明（见 helperfs 的 errText），
			// 不含任何宿主路径或 os.PathError 原文。
			return nil, wireToError(wire.Code, wire.Error)
		}

		out := envelope.VolumeFilesResult{
			Path:       wire.Path,
			DataBase64: wire.DataBase64,
			SHA256:     wire.SHA256,
			Size:       wire.Size,
			Message:    wire.Message,
		}
		for _, e := range wire.Entries {
			out.Entries = append(out.Entries, envelope.VolumeFileEntry{
				Name:       e.Name,
				Kind:       e.Kind,
				Size:       e.Size,
				ModifiedAt: e.ModifiedAt,
				Mode:       e.Mode,
			})
		}
		return out, nil
	}
}

// validateVolumeFilesPayload 做 Agent 侧的独立校验。
//
// 与 Hub 侧的规则重复是**故意**的：两端版本可能不同步，
// Agent 不能假设请求一定经过了新版 Hub 的校验。
func validateVolumeFilesPayload(p envelope.VolumeFilesPayload) error {
	if p.Volume == "" || len(p.Volume) > maxVolumeNameLen {
		return badRequestf("存储卷名称无效")
	}
	// 卷名会被拼进 inspect 的 URL 路径，不能含分隔符或 NUL。
	if strings.ContainsAny(p.Volume, "/\\\x00") {
		return badRequestf("存储卷名称无效")
	}
	switch p.Operation {
	case helperfs.OpList:
		// list 允许根路径（空串）。
	case helperfs.OpRead:
		if p.Path == "" {
			return badRequestf("读取必须指定文件路径")
		}
		if p.DataBase64 != "" {
			return badRequestf("读取操作不能携带内容")
		}
	case helperfs.OpWrite:
		if p.Path == "" {
			return badRequestf("写入必须指定文件路径")
		}
		// 两者语义互斥：前者要求"不存在"，后者要求"存在且匹配"。
		if p.CreateOnly && p.ExpectedSHA256 != "" {
			return badRequestf("create_only 与 expected_sha256 不能同时使用")
		}
		if !p.CreateOnly && p.ExpectedSHA256 == "" {
			return badRequestf("覆盖已有文件必须提供 expected_sha256")
		}
		if p.ExpectedSHA256 != "" && !isHexSHA(p.ExpectedSHA256) {
			return badRequestf("expected_sha256 必须是 64 位十六进制")
		}
	case helperfs.OpMkdir:
		if p.Path == "" {
			return badRequestf("创建目录必须指定路径")
		}
	case helperfs.OpRename:
		if p.Path == "" || p.Destination == "" {
			return badRequestf("重命名必须同时指定源与目标")
		}
		if p.Path == p.Destination {
			return badRequestf("源与目标不能相同")
		}
	case helperfs.OpDelete:
		if p.Path == "" {
			return badRequestf("不允许删除卷根目录")
		}
	default:
		return badRequestf("不支持的操作")
	}
	if p.Path != "" && !validVolumeRelPath(p.Path) {
		return badRequestf("路径不合法")
	}
	if p.Destination != "" && !validVolumeRelPath(p.Destination) {
		return badRequestf("目标路径不合法")
	}
	// base64 长度粗筛：4MiB 编码后约 5.6MB。
	if len(p.DataBase64) > helperfs.MaxFileBytes*2 {
		return badRequestf("内容超过大小上限")
	}
	return nil
}

// validVolumeRelPath 检查卷内相对路径形态（详细判定交给 helper 的 os.Root）。
func validVolumeRelPath(p string) bool {
	if p == "" {
		return true
	}
	if strings.ContainsAny(p, "\\\x00") {
		return false
	}
	if strings.HasPrefix(p, "/") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// wireToError 把 helper 的错误类别映射成协议错误。
//
// 直接 errors.New 会落到 toEnvelopeError 的兜底分支（runtime_failed → 503），
// 那会让"校验和冲突"被前端提示成"稍后重试"，而重试永远不会成功——
// 用户必须先重新读取文件。因此按类别精确映射：
//
//	checksum / exists / not_empty   → conflict（409，需要用户决策）
//	bad_path / too_large / symlink
//	/ hardlink / not_regular        → bad_request（400，请求就不该发）
//	not_found                       → not_found（404）
//	其余                            → runtime_failed（503，依赖侧问题）
func wireToError(code, msg string) error {
	switch code {
	case "checksum", "exists", "not_empty":
		return envelope.NewError(envelope.CodeConflict, msg)
	case "bad_path", "too_large", "symlink", "hardlink", "not_regular", "bad_request":
		return envelope.NewError(envelope.CodeBadRequest, msg)
	case "not_found":
		return envelope.NewError(envelope.CodeNotFound, msg)
	}
	return envelope.NewError(envelope.CodeRuntimeFailed, msg)
}

// translateVolumeError 把 runtime 错误转成带协议码的中文错误。
func translateVolumeError(err error) error {
	switch {
	case errors.Is(err, runtime.ErrHelperImageMissing):
		// 明确指出该做什么，而不是让用户猜原因。
		return envelope.NewError(envelope.CodeUnsupported,
			"文件助手镜像未安装，请先在本机拉取 "+runtime.DefaultHelperImage)
	case errors.Is(err, runtime.ErrVolumeNotFound):
		return envelope.NewError(envelope.CodeNotFound, "存储卷不存在（不会自动创建）")
	case errors.Is(err, runtime.ErrHelperBusy):
		// 并发已满：这是"暂时性、可重试"的真实语义。
		return envelope.NewError(envelope.CodeAgentBusy, "文件操作繁忙（同时最多 2 个），请稍后重试")
	case errors.Is(err, context.DeadlineExceeded):
		return envelope.NewError(envelope.CodeTimeout, "文件操作超时")
	case errors.Is(err, context.Canceled):
		return envelope.NewError(envelope.CodeRuntimeFailed, "文件操作已取消")
	}
	return envelope.NewError(envelope.CodeRuntimeFailed, "文件操作失败")
}
