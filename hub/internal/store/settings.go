package store

import (
	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/do"
	"arkade/hub/internal/model/entity"
	"context"
	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/errors/gerror"
)

func settingsFrom(e entity.SystemSettings) SystemSettings {
	return SystemSettings{ID: e.Id, SiteName: e.SiteName, LoginNotice: e.LoginNotice, RegistrationEnabled: e.RegistrationEnabled != 0, RegistrationGroupID: e.RegistrationGroupId, SessionTTLSeconds: int64(e.SessionTtlSeconds), PasswordMinLength: int64(e.PasswordMinLength), LoginMaxAttempts: int64(e.LoginMaxAttempts), LoginWindowSeconds: int64(e.LoginWindowSeconds), AuditRetentionDays: int64(e.AuditRetentionDays), Version: int64(e.Version)}
}
func (d *DB) GetSystemSettings(ctx context.Context) (SystemSettings, error) {
	var e entity.SystemSettings
	err := scanOne(ctx, d.gdb.Model(dao.SystemSettings.Table()).Where("id", 1), &e)
	return settingsFrom(e), err
}
func (d *DB) UpdateSystemSettings(ctx context.Context, s SystemSettings, expectedVersion int64) error {
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		if s.RegistrationGroupID != "" {
			n, err := tx.Model(dao.Groups.Table()).Ctx(ctx).Where("id", s.RegistrationGroupID).Count()
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrRegistrationGroupUnavailable
			}
		}
		var group any = gdb.Raw("NULL")
		if s.RegistrationGroupID != "" {
			group = s.RegistrationGroupID
		}
		result, err := tx.Model(dao.SystemSettings.Table()).Ctx(ctx).Where("id", 1).Where("version", expectedVersion).Data(do.SystemSettings{SiteName: s.SiteName, LoginNotice: s.LoginNotice, RegistrationEnabled: boolToInt(s.RegistrationEnabled), RegistrationGroupId: group, SessionTtlSeconds: s.SessionTTLSeconds, PasswordMinLength: s.PasswordMinLength, LoginMaxAttempts: s.LoginMaxAttempts, LoginWindowSeconds: s.LoginWindowSeconds, AuditRetentionDays: s.AuditRetentionDays, Version: expectedVersion + 1}).Update()
		if err != nil {
			return normalizeExecError(err, "更新系统设置")
		}
		n, err := result.RowsAffected()
		if err != nil {
			return gerror.Wrap(err, "确认系统设置更新失败")
		}
		if n == 0 {
			return ErrConflict
		}
		return nil
	})
}
