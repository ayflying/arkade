// ==========================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// ==========================================================================

package internal

import (
	"context"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
)

// ComposeStacksDao is the data access object for the table compose_stacks.
type ComposeStacksDao struct {
	table    string               // table is the underlying table name of the DAO.
	group    string               // group is the database configuration group name of the current DAO.
	columns  ComposeStacksColumns // columns contains all the column names of Table for convenient usage.
	handlers []gdb.ModelHandler   // handlers for customized model modification.
}

// ComposeStacksColumns defines and stores column names for the table compose_stacks.
type ComposeStacksColumns struct {
	Id         string //
	AgentId    string //
	Name       string //
	Content    string //
	ConfigHash string //
	Version    string //
	CreatedAt  string //
	UpdatedAt  string //
	SyncedAt   string //
	EnvContent string //
}

// composeStacksColumns holds the columns for the table compose_stacks.
var composeStacksColumns = ComposeStacksColumns{
	Id:         "id",
	AgentId:    "agent_id",
	Name:       "name",
	Content:    "content",
	ConfigHash: "config_hash",
	Version:    "version",
	CreatedAt:  "created_at",
	UpdatedAt:  "updated_at",
	SyncedAt:   "synced_at",
	EnvContent: "env_content",
}

// NewComposeStacksDao creates and returns a new DAO object for table data access.
func NewComposeStacksDao(handlers ...gdb.ModelHandler) *ComposeStacksDao {
	return &ComposeStacksDao{
		group:    "default",
		table:    "compose_stacks",
		columns:  composeStacksColumns,
		handlers: handlers,
	}
}

// DB retrieves and returns the underlying raw database management object of the current DAO.
func (dao *ComposeStacksDao) DB() gdb.DB {
	return g.DB(dao.group)
}

// Table returns the table name of the current DAO.
func (dao *ComposeStacksDao) Table() string {
	return dao.table
}

// Columns returns all column names of the current DAO.
func (dao *ComposeStacksDao) Columns() ComposeStacksColumns {
	return dao.columns
}

// Group returns the database configuration group name of the current DAO.
func (dao *ComposeStacksDao) Group() string {
	return dao.group
}

// Ctx creates and returns a Model for the current DAO. It automatically sets the context for the current operation.
func (dao *ComposeStacksDao) Ctx(ctx context.Context) *gdb.Model {
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
func (dao *ComposeStacksDao) Transaction(ctx context.Context, f func(ctx context.Context, tx gdb.TX) error) (err error) {
	return dao.Ctx(ctx).Transaction(ctx, f)
}
