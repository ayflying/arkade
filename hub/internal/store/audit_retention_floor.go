package store

// audit_retention_floor.go：把存量实例的审计保留天数收敛到当前策略。
//
// ============================ 为什么需要它 ============================
//
// 审计保留期的默认值写在初始迁移的种子数据里（历史上是 90 天），
// 管理员也可以在系统设置里把它调到 3650 天。仅把常量改成 30 天**不够**：
// 升级后旧库里的那一行仍然是 90，没人会再去点一次保存，于是"最多保存 30 天"
// 对存量实例根本不生效。
//
// ============================ 为什么不写成迁移脚本 ============================
//
// 迁移文件只读且不重放，用脚本改业务数据后，再往前回滚（或分裂库）
// 两边状态就不一致；而这里要表达的是"每次启动都把策略收敛一次"——
// 包括有人手工改库把值改大、或从别处导入了一份老配置。
// 放在 Open 里执行，语义就是："能连上库，就一定符合当前保留期策略"。
//
// ============================ 边界 ============================
//
// 只动超出上限的值（以及非法值 <=0）。小于上限的天数是管理员的真实选择，
// 一律保留——用户把保留期设成 7 天是有效的策略，不能被"修正"回 30 天。

import (
	"context"

	"github.com/gogf/gf/v2/errors/gerror"
)

// clampAuditRetention 把 audit_retention_days 收敛到 [AuditMinRetentionDays, AuditRetentionDays]。
//
// 幂等：符合范围时 UPDATE 影响 0 行。
func (d *DB) clampAuditRetention(ctx context.Context) error {
	_, err := d.gdb.Exec(ctx,
		`UPDATE system_settings SET audit_retention_days = ?
		  WHERE audit_retention_days > ? OR audit_retention_days < ?`,
		AuditRetentionDays, AuditRetentionDays, AuditMinRetentionDays)
	if err != nil {
		return gerror.Wrapf(err, "收敛审计保留天数失败")
	}
	return nil
}
