// ==========================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// ==========================================================================

package internal

import (
	"context"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
)

// ExternalComposeDraftsDao is the data access object for the table external_compose_drafts.
type ExternalComposeDraftsDao struct {
	table    string                       // table is the underlying table name of the DAO.
	group    string                       // group is the database configuration group name of the current DAO.
	columns  ExternalComposeDraftsColumns // columns contains all the column names of Table for convenient usage.
	handlers []gdb.ModelHandler           // handlers for customized model modification.
}

// ExternalComposeDraftsColumns defines and stores column names for the table external_compose_drafts.
type ExternalComposeDraftsColumns struct {
	AgentId    string //
	Name       string //
	Content    string //
	SourceHash string //
	Version    string //
	UpdatedAt  string //
}

// externalComposeDraftsColumns holds the columns for the table external_compose_drafts.
var externalComposeDraftsColumns = ExternalComposeDraftsColumns{
	AgentId:    "agent_id",
	Name:       "name",
	Content:    "content",
	SourceHash: "source_hash",
	Version:    "version",
	UpdatedAt:  "updated_at",
}

// NewExternalComposeDraftsDao creates and returns a new DAO object for table data access.
func NewExternalComposeDraftsDao(handlers ...gdb.ModelHandler) *ExternalComposeDraftsDao {
	return &ExternalComposeDraftsDao{
		group:    "default",
		table:    "external_compose_drafts",
		columns:  externalComposeDraftsColumns,
		handlers: handlers,
	}
}

// DB retrieves and returns the underlying raw database management object of the current DAO.
func (dao *ExternalComposeDraftsDao) DB() gdb.DB {
	return g.DB(dao.group)
}

// Table returns the table name of the current DAO.
func (dao *ExternalComposeDraftsDao) Table() string {
	return dao.table
}

// Columns returns all column names of the current DAO.
func (dao *ExternalComposeDraftsDao) Columns() ExternalComposeDraftsColumns {
	return dao.columns
}

// Group returns the database configuration group name of the current DAO.
func (dao *ExternalComposeDraftsDao) Group() string {
	return dao.group
}

// Ctx creates and returns a Model for the current DAO. It automatically sets the context for the current operation.
func (dao *ExternalComposeDraftsDao) Ctx(ctx context.Context) *gdb.Model {
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
func (dao *ExternalComposeDraftsDao) Transaction(ctx context.Context, f func(ctx context.Context, tx gdb.TX) error) (err error) {
	return dao.Ctx(ctx).Transaction(ctx, f)
}
