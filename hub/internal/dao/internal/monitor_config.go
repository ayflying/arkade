// ==========================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// ==========================================================================

package internal

import (
	"context"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
)

// MonitorConfigDao is the data access object for the table monitor_config.
type MonitorConfigDao struct {
	table    string               // table is the underlying table name of the DAO.
	group    string               // group is the database configuration group name of the current DAO.
	columns  MonitorConfigColumns // columns contains all the column names of Table for convenient usage.
	handlers []gdb.ModelHandler   // handlers for customized model modification.
}

// MonitorConfigColumns defines and stores column names for the table monitor_config.
type MonitorConfigColumns struct {
	Id                string //
	ConfigJson        string //
	SecretsCiphertext string //
	Version           string //
}

// monitorConfigColumns holds the columns for the table monitor_config.
var monitorConfigColumns = MonitorConfigColumns{
	Id:                "id",
	ConfigJson:        "config_json",
	SecretsCiphertext: "secrets_ciphertext",
	Version:           "version",
}

// NewMonitorConfigDao creates and returns a new DAO object for table data access.
func NewMonitorConfigDao(handlers ...gdb.ModelHandler) *MonitorConfigDao {
	return &MonitorConfigDao{
		group:    "default",
		table:    "monitor_config",
		columns:  monitorConfigColumns,
		handlers: handlers,
	}
}

// DB retrieves and returns the underlying raw database management object of the current DAO.
func (dao *MonitorConfigDao) DB() gdb.DB {
	return g.DB(dao.group)
}

// Table returns the table name of the current DAO.
func (dao *MonitorConfigDao) Table() string {
	return dao.table
}

// Columns returns all column names of the current DAO.
func (dao *MonitorConfigDao) Columns() MonitorConfigColumns {
	return dao.columns
}

// Group returns the database configuration group name of the current DAO.
func (dao *MonitorConfigDao) Group() string {
	return dao.group
}

// Ctx creates and returns a Model for the current DAO. It automatically sets the context for the current operation.
func (dao *MonitorConfigDao) Ctx(ctx context.Context) *gdb.Model {
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
func (dao *MonitorConfigDao) Transaction(ctx context.Context, f func(ctx context.Context, tx gdb.TX) error) (err error) {
	return dao.Ctx(ctx).Transaction(ctx, f)
}
