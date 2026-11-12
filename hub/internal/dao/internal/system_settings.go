// ==========================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// ==========================================================================

package internal

import (
	"context"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
)

// SystemSettingsDao is the data access object for the table system_settings.
type SystemSettingsDao struct {
	table    string                // table is the underlying table name of the DAO.
	group    string                // group is the database configuration group name of the current DAO.
	columns  SystemSettingsColumns // columns contains all the column names of Table for convenient usage.
	handlers []gdb.ModelHandler    // handlers for customized model modification.
}

// SystemSettingsColumns defines and stores column names for the table system_settings.
type SystemSettingsColumns struct {
	Id                  string //
	SiteName            string //
	LoginNotice         string //
	RegistrationEnabled string //
	RegistrationGroupId string //
	SessionTtlSeconds   string //
	PasswordMinLength   string //
	LoginMaxAttempts    string //
	LoginWindowSeconds  string //
	AuditRetentionDays  string //
	Version             string //
}

// systemSettingsColumns holds the columns for the table system_settings.
var systemSettingsColumns = SystemSettingsColumns{
	Id:                  "id",
	SiteName:            "site_name",
	LoginNotice:         "login_notice",
	RegistrationEnabled: "registration_enabled",
	RegistrationGroupId: "registration_group_id",
	SessionTtlSeconds:   "session_ttl_seconds",
	PasswordMinLength:   "password_min_length",
	LoginMaxAttempts:    "login_max_attempts",
	LoginWindowSeconds:  "login_window_seconds",
	AuditRetentionDays:  "audit_retention_days",
	Version:             "version",
}

// NewSystemSettingsDao creates and returns a new DAO object for table data access.
func NewSystemSettingsDao(handlers ...gdb.ModelHandler) *SystemSettingsDao {
	return &SystemSettingsDao{
		group:    "default",
		table:    "system_settings",
		columns:  systemSettingsColumns,
		handlers: handlers,
	}
}

// DB retrieves and returns the underlying raw database management object of the current DAO.
func (dao *SystemSettingsDao) DB() gdb.DB {
	return g.DB(dao.group)
}

// Table returns the table name of the current DAO.
func (dao *SystemSettingsDao) Table() string {
	return dao.table
}

// Columns returns all column names of the current DAO.
func (dao *SystemSettingsDao) Columns() SystemSettingsColumns {
	return dao.columns
}

// Group returns the database configuration group name of the current DAO.
func (dao *SystemSettingsDao) Group() string {
	return dao.group
}

// Ctx creates and returns a Model for the current DAO. It automatically sets the context for the current operation.
func (dao *SystemSettingsDao) Ctx(ctx context.Context) *gdb.Model {
	model := dao.DB().Model(dao.table)
	for _, handler := range dao.handlers {
		model = handler(model)
	}
	return model.Safe().Ctx(ctx)
}

// Transaction wraps the transaction logic using function f.
// It rolls back the transaction and returns the error if function f returns a non-nil error.
// It commits the transaction and returns nil if function f returns nil.
//
// Note: Do not commit or roll back the transaction in function f,
// as it is automatically handled by this function.
func (dao *SystemSettingsDao) Transaction(ctx context.Context, f func(ctx context.Context, tx gdb.TX) error) (err error) {
	return dao.Ctx(ctx).Transaction(ctx, f)
}
