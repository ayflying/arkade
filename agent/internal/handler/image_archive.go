package handler

// image_archive.go 是可复用归档导出的方法处理器（编排迁移用）。
//
// ============================ 分层与 image_transfer.go 一致 ============================
//
// handler 只做四件事：解码 → 校验 ExportID/引用 → 调 imagetransfer → 转协议结构。
// 状态机、TTL、配额、读者计数全在 imagetransfer.archive.go 里。
//
// ============================ 错误语义（对 Hub 的契约） ============================
//
// 交付给 Hub 的关键区分——**不存在 / 已关闭 / 已过期**三者刻意不同：
//
//	not_found  ExportID 从未导出过，或已过期被清理，或文件被外部删掉
//	conflict   归档曾经存在但已被显式关闭（现在的状态不允许读取）
//	bad_request ExportID 形态非法（不是 32 位小写 hex）
//
// 为什么不用同一个错误码：调用方的反应不同。
//   - not_found → 重新导出（可能这次导出/清理周期已过）；
//   - conflict  → 说明有人调用了 close，是**流程错误**，该报给用户看，
//                 而不是静默重试（重试只会再拿到一次 conflict）；
//   - bad_request → 客户端 bug，重试无意义。
//
// 但"过期"与"从未存在"都用 not_found：两者的正确处理方式完全相同
// （重新导出），而区分它们需要保留一条永久记录，代价不值得。
// 若将来需要区分，Close 返回的 Message 里已经如实说明了情况。

import (
	"context"
	"errors"
	"strings"

	"arkade/agent/internal/imagetransfer"
	"arkade/protocol/envelope"
)

// exportIDLen 是 ExportID 的固定长度（32 位小写十六进制）。
//
// 与 imagetransfer 内部派生规则一致。在这里再校验一次不是重复劳动：
// 它让非法输入在**进入任何文件系统路径拼装之前**就被拒绝，
// 而不依赖下游 validTaskID 的善意。
const exportIDLen = 32

// newImageArchiveExportHandler 处理 image.archive.export。
func newImageArchiveExportHandler(mgr *imagetransfer.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ImageArchiveExportPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		ref, err := validateImageRef(p.Reference)
		if err != nil {
			return nil, err
		}
		// Hold=false 目前与 hold=true 走同一条同步导出路径：
		// 协议保留这个字段是为了让调用方能明确表达意图，
		// 而"不持有"的旧行为由 image.save 提供，不必在这里再分叉。
		//
		// 但要如实告诉调用方：传了 false 也仍然会持有到 close/TTL。
		// 静默把 false 当 true 会让调用方以为"不用管它"，然后被配额拦住。
		info, eerr := mgr.ExportArchive(ctx, ref)
		if eerr != nil {
			return nil, archiveError(eerr)
		}
		return toArchiveExportResult(info), nil
	}
}

// newImageArchiveOpenHandler 处理 image.archive.open。
func newImageArchiveOpenHandler(mgr *imagetransfer.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		id, err := decodeExportID(payload, func(raw []byte) (string, error) {
			var p envelope.ImageArchiveOpenPayload
			if derr := decodePayloadInto(raw, &p); derr != nil {
				return "", derr
			}
			return p.ExportID, nil
		})
		if err != nil {
			return nil, err
		}
		info, eerr := mgr.OpenExport(ctx, id)
		if eerr != nil {
			return nil, archiveError(eerr)
		}
		return envelope.ImageArchiveOpenResult{
			ExportID:  info.ExportID,
			Reference: info.Reference,
			SizeBytes: info.SizeBytes,
			SHA256:    info.SHA256,
			ExpiresAt: info.ExpiresAt.UTC().Format(rfc3339Layout),
		}, nil
	}
}

// newImageArchiveChunkGetHandler 处理 image.archive.chunk.get。
func newImageArchiveChunkGetHandler(mgr *imagetransfer.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ImageArchiveChunkGetPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		id, err := validateExportID(p.ExportID)
		if err != nil {
			return nil, err
		}
		if p.Offset < 0 {
			return nil, badRequestf("offset 不能为负")
		}
		data, offset, eof, size, gerr := mgr.GetExportChunk(ctx, id, p.Offset, p.MaxBytes)
		if gerr != nil {
			return nil, archiveError(gerr)
		}
		// 复用 ImageChunkGetResult：Hub 侧因此只需一套解析，
		// 而 TaskID 字段留空（这里没有任务概念）。
		return envelope.ImageChunkGetResult{
			DataBase64: data,
			Offset:     offset,
			EOF:        eof,
			SizeBytes:  size,
		}, nil
	}
}

// newImageArchiveCloseHandler 处理 image.archive.close。
func newImageArchiveCloseHandler(mgr *imagetransfer.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		id, err := decodeExportID(payload, func(raw []byte) (string, error) {
			var p envelope.ImageArchiveClosePayload
			if derr := decodePayloadInto(raw, &p); derr != nil {
				return "", derr
			}
			return p.ExportID, nil
		})
		if err != nil {
			return nil, err
		}
		released, msg, cerr := mgr.CloseExport(ctx, id)
		if cerr != nil {
			return nil, archiveError(cerr)
		}
		// released=false 且无错误 = 本来就不存在。
		// 这是**成功**：调用方要的结果（不再占盘）已达成。
		return envelope.ImageArchiveCloseResult{
			ExportID: id, Released: released, Message: msg,
		}, nil
	}
}

// decodeExportID 解出并校验 ExportID（载荷形态由 decode 回调决定）。
func decodeExportID(payload []byte, decode func([]byte) (string, error)) (string, error) {
	raw, err := decode(payload)
	if err != nil {
		return "", err
	}
	return validateExportID(raw)
}

// validateExportID 校验 ExportID 形态。
//
// 只接受 32 位小写十六进制——与 Agent 内部派生规则完全一致。
// 任何其它形态都当成"客户端 bug"直接拒绝：它绝不该被拼进文件路径。
func validateExportID(id string) (string, error) {
	v := strings.TrimSpace(id)
	if v == "" {
		return "", badRequestf("export_id 不能为空")
	}
	if len(v) != exportIDLen {
		return "", badRequestf("export_id 格式不正确（应为 %d 位十六进制）", exportIDLen)
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", badRequestf("export_id 格式不正确（只允许小写十六进制）")
		}
	}
	return v, nil
}

// toArchiveExportResult 把内部信息转成协议结构。
func toArchiveExportResult(i imagetransfer.ExportInfo) envelope.ImageArchiveExportResult {
	return envelope.ImageArchiveExportResult{
		ExportID:  i.ExportID,
		Reference: i.Reference,
		SizeBytes: i.SizeBytes,
		SHA256:    i.SHA256,
		ExpiresAt: i.ExpiresAt.UTC().Format(rfc3339Layout),
	}
}

// rfc3339Layout 是时间字段统一的格式。
const rfc3339Layout = "2006-01-02T15:04:05Z07:00"

// archiveError 把可复用归档的领域错误翻成协议错误码 + 中文说明。
//
// ============================ 三种"不可读"必须区分开 ============================
//
// 见文件头的错误语义说明。这里额外注意一点：
// 返回的错误文本**不含任何路径或内部标识**，避免把宿主机布局
// 通过错误消息泄露给 Hub 与浏览器。
func archiveError(err error) error {
	switch {
	case errors.Is(err, imagetransfer.ErrExportNotFound):
		return envelope.NewError(envelope.CodeNotFound,
			"归档不存在或已过期（请重新导出）")
	case errors.Is(err, imagetransfer.ErrExportClosed):
		return envelope.NewError(envelope.CodeConflict,
			"归档已被关闭，无法再读取（这是流程错误，请检查调用顺序）")
	case errors.Is(err, imagetransfer.ErrExportExpired):
		return envelope.NewError(envelope.CodeNotFound,
			"归档已超过保留时长被清理（请重新导出）")
	case errors.Is(err, imagetransfer.ErrExportNotReady):
		return envelope.NewError(envelope.CodeConflict,
			"归档尚未导出完成，请稍后重试")
	case errors.Is(err, imagetransfer.ErrTooManyExports):
		return envelope.NewError(envelope.CodeConflict,
			"同时持有的归档数量已达上限，请先关闭不再需要的归档")
	case errors.Is(err, imagetransfer.ErrQuotaExceeded):
		return badRequestf("传输临时区可用配额不足（总量上限 %d MiB），请先释放已有任务或归档",
			imagetransfer.DefaultMaxTotalArchiveBytes>>20)
	case errors.Is(err, imagetransfer.ErrArchiveTooLarge):
		return badRequestf("归档超过单次传输上限（当前限制 %d MiB）",
			imagetransfer.DefaultMaxArchiveBytes>>20)
	case errors.Is(err, imagetransfer.ErrBadOffset):
		return badRequestf("分块偏移不正确（offset 不能为负）")
	case errors.Is(err, imagetransfer.ErrChecksumMismatch):
		return envelope.NewError(envelope.CodeConflict,
			"归档内容与登记的大小不一致（可能被外部修改），已拒绝读取")
	}
	// 其余错误走统一的领域错误翻译（含"引擎不支持镜像传输"等）。
	return transferError(err)
}
