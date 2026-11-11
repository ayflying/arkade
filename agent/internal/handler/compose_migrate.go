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
	"errors"
	"strings"

	"arkade/agent/internal/compose"
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

// newVolumeImportBeginHandler 处理 volume.import.begin。
func newVolumeImportBeginHandler(imp *migrate.Importer) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.VolumeImportBeginPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		snap, reused, err := imp.Begin(ctx, strings.TrimSpace(p.Volume), p.Project,
			p.SizeBytes, strings.TrimSpace(p.SHA256), strings.TrimSpace(p.TaskID))
		if err != nil {
			return nil, migrateError(err)
		}
		return envelope.VolumeImportBeginResult{
			TaskID: snap.TaskID, Volume: snap.Volume,
			SizeBytes: snap.BytesTotal, SHA256: p.SHA256,
			ReceivedBytes: snap.Received, AlreadyReused: reused,
		}, nil
	}
}

// newVolumeImportChunkPutHandler 处理 volume.import.chunk.put。
func newVolumeImportChunkPutHandler(imp *migrate.Importer) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.VolumeImportChunkPutPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.TaskID) == "" {
			return nil, badRequestf("task_id 不能为空")
		}
		if p.Offset < 0 {
			return nil, badRequestf("offset 不能为负")
		}
		data, derr := base64.StdEncoding.DecodeString(p.DataBase64)
		if derr != nil {
			return nil, badRequestf("data_base64 不是合法的 base64")
		}
		if len(data) > migrate.ChunkSize {
			return nil, badRequestf("单个分块超过上限（%d 字节）", migrate.ChunkSize)
		}
		received, perr := imp.PutChunk(ctx, p.TaskID, p.Offset, data)
		if perr != nil {
			return nil, migrateError(perr)
		}
		return envelope.VolumeImportChunkPutResult{ReceivedBytes: received}, nil
	}
}

// newVolumeImportCommitHandler 处理 volume.import.commit。
func newVolumeImportCommitHandler(imp *migrate.Importer) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.VolumeImportCommitPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.TaskID) == "" {
			return nil, badRequestf("task_id 不能为空")
		}
		snap, err := imp.Commit(ctx, p.TaskID, strings.TrimSpace(p.SHA256))
		if err != nil {
			return nil, migrateError(err)
		}
		return envelope.VolumeImportResult{
			TaskID: snap.TaskID, Volume: snap.Volume, Status: snap.Status,
			Phase: snap.Phase, Message: snap.Message,
			BytesDone: snap.Received, SHA256: snap.SHA256,
		}, nil
	}
}

// newVolumeImportStatusHandler 处理 volume.import.status。
func newVolumeImportStatusHandler(imp *migrate.Importer) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.VolumeImportStatusPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		snap, err := imp.Status(strings.TrimSpace(p.TaskID))
		if err != nil {
			return nil, migrateError(err)
		}
		return envelope.VolumeImportResult{
			TaskID: snap.TaskID, Volume: snap.Volume, Status: snap.Status,
			Phase: snap.Phase, Message: snap.Message,
			BytesDone: snap.Received, SHA256: snap.SHA256,
		}, nil
	}
}

// newComposeImportHandler 处理 compose.import。
func newComposeImportHandler(importer migrate.CompositionImporter) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ComposeImportPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		res, err := migrate.ImportComposition(importer, migrate.ComposeArchive{
			Name: p.Name, Config: p.Config, Env: p.Env,
			ExpectHash: p.ExpectHash, Overwrite: false,
		})
		if err != nil {
			if errors.Is(err, migrate.ErrComposeExists) {
				// 同名冲突是**用户可见的产品行为**（提示改名），
				// 因此用 conflict 而不是内部错误。
				return nil, envelope.NewError(envelope.CodeConflict,
					"目标服务器上已存在同名编排，请先改名后再迁移")
			}
			return nil, migrateError(err)
		}
		return envelope.ComposeImportResult{
			Name: res.Name, ConfigHash: res.ConfigHash, AlreadyExists: res.AlreadyExists,
		}, nil
	}
}

// composeWorkspaceImporter 把 *compose.Manager 适配成 migrate.CompositionImporter。
//
// ============================ 为什么要这层适配 ============================
// migrate 包不能反过来 import compose 包：compose 已经 import 了它（同一模块内
// 形成 compose → migrate → compose 的循环依赖，Go 会直接编译失败）。
// 因此 migrate 只定义一个窄接口（Exists + Commit），由这里注入实现。
//
// 这样做的额外好处：migrate 的测试不需要构造一个真实的 compose.Manager，
// 用一个假对象就能覆盖全部分支。
type composeWorkspaceImporter struct {
	mgr *compose.Manager
}

// Exists 判断本机是否已有该编排。
func (c composeWorkspaceImporter) Exists(name string) bool {
	if c.mgr == nil {
		return false
	}
	return c.mgr.Workspace().Exists(name)
}

// Commit 原子落盘配置与 env。
//
// 直接复用 Workspace.CommitProject：它内部已经是原子写 + 失败回滚，
// 自己再实现一遍只会漏掉某个保护（例如 env 写入失败时回滚配置）。
func (c composeWorkspaceImporter) Commit(name, content, env string) error {
	if c.mgr == nil {
		return errors.New("编排管理器不可用")
	}
	_, _, err := c.mgr.Workspace().CommitProject(name, content, env)
	return err
}

// migrateError 把 migrate 包的领域错误翻成协议错误码 + 中文说明。
//
// 注意：所有文案都**不含路径、容器输出原文、registry 地址**。
// 那些信息会随错误流到 Hub 与浏览器，而它们对用户判断毫无帮助。
func migrateError(err error) error {
	switch {
	case errors.Is(err, migrate.ErrVolumeExportNotFound):
		return envelope.NewError(envelope.CodeNotFound, "卷归档不存在或已过期（请重新导出）")
	case errors.Is(err, migrate.ErrVolumeExportExpired):
		return envelope.NewError(envelope.CodeNotFound, "卷归档已超过保留时长（请重新导出）")
	case errors.Is(err, migrate.ErrImportNotFound):
		return envelope.NewError(envelope.CodeNotFound, "导入任务不存在或已过期")
	case errors.Is(err, migrate.ErrTooManyVolumeExports):
		return envelope.NewError(envelope.CodeConflict, "同时持有的卷归档数量已达上限，请稍后重试")
	case errors.Is(err, migrate.ErrTooManyImports):
		return envelope.NewError(envelope.CodeConflict, "同时进行的卷导入任务已达上限，请稍后重试")
	case errors.Is(err, migrate.ErrVolumeArchiveTooLarge):
		return badRequestf("数据卷归档超过单次传输上限")
	case errors.Is(err, migrate.ErrBadVolumeName):
		return badRequestf("数据卷名称不合法（只允许字母、数字、下划线、点与短横线）")
	case errors.Is(err, migrate.ErrBadOffset):
		return badRequestf("分块偏移不合法")
	case errors.Is(err, migrate.ErrImportParamMismatch):
		return envelope.NewError(envelope.CodeConflict,
			"该任务标识已用于另一次不同的卷导入")
	case errors.Is(err, migrate.ErrImportState):
		return envelope.NewError(envelope.CodeConflict, "导入任务当前状态不允许此操作")
	case errors.Is(err, migrate.ErrSizeMismatch):
		return envelope.NewError(envelope.CodeConflict,
			"卷归档字节数与声明不一致，已中止（目标数据卷未被修改）")
	case errors.Is(err, migrate.ErrChecksumMismatch):
		return envelope.NewError(envelope.CodeConflict,
			"卷归档校验和不一致，已中止导入（目标数据卷未被修改）")
	case errors.Is(err, migrate.ErrComposeExists):
		return envelope.NewError(envelope.CodeConflict,
			"目标服务器上已存在同名编排，请先改名后再迁移")
	case errors.Is(err, migrate.ErrComposeHashMismatch):
		return envelope.NewError(envelope.CodeConflict,
			"编排配置在传输过程中发生了变化，已拒绝写入")
	case errors.Is(err, migrate.ErrComposeContentInvalid):
		return badRequestf("编排配置不合法")
	case errors.Is(err, migrate.ErrComposeNameInvalid):
		return badRequestf("编排项目名不合法（只允许小写字母、数字、下划线、短横线，且不能以分隔符开头）")
	}
	return toEnvelopeError(err)
}
