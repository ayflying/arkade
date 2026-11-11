// ==========================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// ==========================================================================

package internal

import (
	"context"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
)

// AuditLogsDao is the data access object for the table audit_logs.
type AuditLogsDao struct {
	table    string             // table is the underlying table name of the DAO.
	group    string             // group is the database configuration group name of the current DAO.
	columns  AuditLogsColumns   // columns contains all the column names of Table for convenient usage.
	handlers []gdb.ModelHandler // handlers for customized model modification.
}

// AuditLogsColumns defines and stores column names for the table audit_logs.
type AuditLogsColumns struct {
	Id         string //
	UserId     string //
	Username   string //
	AgentId    string //
	AgentName  string //
	Action     string //
	Target     string //
	Detail     string //
	Result     string //
	Error      string //
	DurationMs string //
	CreatedAt  string //
}

// auditLogsColumns holds the columns for the table audit_logs.
var auditLogsColumns = AuditLogsColumns{
	Id:         "id",
	UserId:     "user_id",
	Username:   "username",
	AgentId:    "agent_id",
	AgentName:  "agent_name",
	Action:     "action",
	Target:     "target",
	Detail:     "detail",
	Result:     "result",
	Error:      "error",
	DurationMs: "duration_ms",
	CreatedAt:  "created_at",
}

// NewAuditLogsDao creates and returns a new DAO object for table data access.
func NewAuditLogsDao(handlers ...gdb.ModelHandler) *AuditLogsDao {
	return &AuditLogsDao{
		group:    "default",
		table:    "audit_logs",
		columns:  auditLogsColumns,
		handlers: handlers,
	}
}

// DB retrieves and returns the underlying raw database management object of the current DAO.
func (dao *AuditLogsDao) DB() gdb.DB {
	return g.DB(dao.group)
}

// Table returns the table name of the current DAO.
func (dao *AuditLogsDao) Table() string {
	return dao.table
}

// Columns returns all column names of the current DAO.
func (dao *AuditLogsDao) Columns() AuditLogsColumns {
	return dao.columns
}

// Group returns the database configuration group name of the current DAO.
func (dao *AuditLogsDao) Group() string {
	return dao.group
}

// Ctx creates and returns a Model for the current DAO. It automatically sets the context for the current operation.
func (dao *AuditLogsDao) Ctx(ctx context.Context) *gdb.Model {
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
func (dao *AuditLogsDao) Transaction(ctx context.Context, f func(ctx context.Context, tx gdb.TX) error) (err error) {
	return dao.Ctx(ctx).Transaction(ctx, f)
}
