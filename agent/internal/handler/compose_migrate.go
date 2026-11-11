package handler

// compose_migrate.go：编排迁移的 Agent 侧方法处理器。
//
// ============================ 分层 ============================
// handler 只做：解码 → 校验参数形态 → 调 compose/migrate → 转协议结构。
// 所有卷识别、归档生命周期、幂等、校验和都在 compose/migrate 包里。
//
// ============================ 与 compose_operation.go 的关系 ============================
// 这里**不引入新任务系统**：卷导出与导入都是"有明确终点的短任务"
// （导出：跑一次 tar；导入：收完字节再 commit），
// 用返回值同步表达即可，不需要 compose.operation 那套 queued/running 事件流。
//
// 真正需要长任务事件流的是 Hub 侧的迁移编排器（stackmigrate），
// 它在 Hub 侧复用 compose.operation 的事件结构对外呈现。
//
// ============================ 错误语义 ============================
//
//	not_found   归档/任务不存在（已释放或过期）→ 调用方重新导出
//	conflict    目标已有同名编排、数量达上限、内容校验不符 → 流程问题，重试无意义
//	bad_request 参数形态非法 → 客户端 bug

import (
	"context"
	"encoding/base64"
	"strings"

	"arkade/agent/internal/compose/migrate"
	"arkade/protocol/envelope"
)

// newVolumeArchiveExportHandler 处理 volume.archive.export。
func newVolumeArchiveExportHandler(exp *migrate.VolumeExporter) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.VolumeArchiveExportPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		vol := strings.TrimSpace(p.Volume)
		if vol == "" {
			return nil, badRequestf("volume 不能为空")
		}
		info, err := exp.Export(ctx, vol, p.Project)
		if err != nil {
			return nil, migrateError(err)
		}
		return envelope.VolumeArchiveExportResult{
			ExportID:  info.ExportID,
			Volume:    info.Volume,
			SizeBytes: info.SizeBytes,
			SHA256:    info.SHA256,
			ExpiresAt: info.ExpiresAt.UTC().Format(rfc3339Layout),
		}, nil
	}
}

// newVolumeArchiveOpenHandler 处理 volume.archive.open。
func newVolumeArchiveOpenHandler(exp *migrate.VolumeExporter) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.VolumeArchiveOpenPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		id, err := validateExportID(p.ExportID)
		if err != nil {
			return nil, err
		}
		info, oerr := exp.Open(ctx, id)
		if oerr != nil {
			return nil, migrateError(oerr)
		}
		return envelope.VolumeArchiveOpenResult{
			ExportID:  info.ExportID,
			Volume:    info.Volume,
			SizeBytes: info.SizeBytes,
			SHA256:    info.SHA256,
			ExpiresAt: info.ExpiresAt.UTC().Format(rfc3339Layout),
		}, nil
	}
}

// newVolumeArchiveChunkGetHandler 处理 volume.archive.chunk.get。
func newVolumeArchiveChunkGetHandler(exp *migrate.VolumeExporter) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.VolumeArchiveChunkGetPayload
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
		data, eof, gerr := exp.GetChunk(ctx, id, p.Offset, p.MaxBytes)
		if gerr != nil {
			return nil, migrateError(gerr)
		}
		// 复用 ImageChunkGetResult：Hub 侧复制代码因此能只换方法名。
		return envelope.ImageChunkGetResult{
			DataBase64: base64.StdEncoding.EncodeToString(data),
			Offset:     p.Offset,
			EOF:        eof,
		}, nil
	}
}

// newVolumeArchiveCloseHandler 处理 volume.archive.close。
func newVolumeArchiveCloseHandler(exp *migrate.VolumeExporter) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.VolumeArchiveClosePayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		id, err := validateExportID(p.ExportID)
		if err != nil {
			return nil, err
		}
		released, msg, cerr := exp.Close(ctx, id)
		if cerr != nil {
			return nil, migrateError(cerr)
		}
		// released=false 且无错误 = 本来就不存在，**这是成功**。
		return envelope.VolumeArchiveCloseResult{
			ExportID: id, Released: released, Message: msg,
		}, nil
	}
}
