package handler

import (
	"arkade/agent/internal/compose"
	"arkade/agent/internal/compose/migrate"
	"arkade/protocol/envelope"
	"errors"
)

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
	case errors.Is(err, migrate.ErrTargetVolumeExists):
		return envelope.NewError(envelope.CodeConflict, "目标数据卷已存在，已拒绝覆盖")
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
