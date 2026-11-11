package handler

import (
	"arkade/agent/internal/compose"
	"arkade/agent/internal/compose/migrate"
	"arkade/protocol/envelope"
	"log/slog"
	"path/filepath"
)

// registerMigrateHandlers 注册编排迁移的 Agent 侧方法。
//
// ============================ 为什么单独成函数 ============================
// 它需要的装配点比其它方法多（传输目录、docker 执行器、编排工作区适配器），
// 全部塞进 RegisterDefaultsWithTransfer 会让那个函数再长一截，
// 而它现在已经接近"读不完"的程度。
//
// 返回 nil 表示只完成了不需要临时区的那部分注册。
func registerMigrateHandlers(reg *Registry, composeMgr *compose.Manager, transferDir string, log *slog.Logger) any {
	// compose.import 只需要工作区，与临时区无关，因此先注册。
	reg.Register(envelope.MethodComposeImport, newComposeImportHandler(composeWorkspaceImporter{composeMgr}))
	reg.Register(envelope.MethodComposeMigrationPlan, newComposeMigrationPlanHandler(composeMgr))

	if transferDir == "" {
		return nil
	}

	exporter := migrate.NewVolumeExporter(filepath.Join(transferDir, "volumes"), composeMgr.MigrationEngineCommand)
	importer := migrate.NewImporter(filepath.Join(transferDir, "imports"), composeMgr.MigrationEngineCommand)

	reg.Register(envelope.MethodVolumeArchiveExport, newVolumeArchiveExportHandler(exporter))
	reg.Register(envelope.MethodVolumeArchiveOpen, newVolumeArchiveOpenHandler(exporter))
	reg.Register(envelope.MethodVolumeArchiveChunkGet, newVolumeArchiveChunkGetHandler(exporter))
	reg.Register(envelope.MethodVolumeArchiveClose, newVolumeArchiveCloseHandler(exporter))
	reg.Register(envelope.MethodVolumeImportBegin, newVolumeImportBeginHandler(importer))
	reg.Register(envelope.MethodVolumeImportChunkPut, newVolumeImportChunkPutHandler(importer))
	reg.Register(envelope.MethodVolumeImportCommit, newVolumeImportCommitHandler(importer))
	reg.Register(envelope.MethodVolumeImportStatus, newVolumeImportStatusHandler(importer))
	reg.Register(envelope.MethodVolumeImportCancel, newVolumeImportCancelHandler(importer))

	// 启动即清理：进程被 kill 时 defer 不会执行。
	// 与镜像传输同样的理由——重启后的登记表是空的，
	// 磁盘上的归档无法证明仍然有效，一律删除。
	exporter.CleanupOrphans()
	return exporter
}
