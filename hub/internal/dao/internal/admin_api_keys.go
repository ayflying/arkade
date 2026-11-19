// ==========================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// ==========================================================================

package internal

import (
	"context"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
)

// AdminApiKeysDao is the data access object for the table admin_api_keys.
type AdminApiKeysDao struct {
	table    string              // table is the underlying table name of the DAO.
	group    string              // group is the database configuration group name of the current DAO.
	columns  AdminApiKeysColumns // columns contains all the column names of Table for convenient usage.
	handlers []gdb.ModelHandler  // handlers for customized model modification.
}

// AdminApiKeysColumns defines and stores column names for the table admin_api_keys.
type AdminApiKeysColumns struct {
	Id        string //
	UserId    string //
	Name      string //
	TokenHash string //
	CreatedAt string //
}

// adminApiKeysColumns holds the columns for the table admin_api_keys.
var adminApiKeysColumns = AdminApiKeysColumns{
	Id:        "id",
	UserId:    "user_id",
	Name:      "name",
	TokenHash: "token_hash",
	CreatedAt: "created_at",
}

// NewAdminApiKeysDao creates and returns a new DAO object for table data access.
func NewAdminApiKeysDao(handlers ...gdb.ModelHandler) *AdminApiKeysDao {
	return &AdminApiKeysDao{
		group:    "default",
		table:    "admin_api_keys",
		columns:  adminApiKeysColumns,
		handlers: handlers,
	}
}

// DB retrieves and returns the underlying raw database management object of the current DAO.
func (dao *AdminApiKeysDao) DB() gdb.DB {
	return g.DB(dao.group)
}

// Table returns the table name of the current DAO.
func (dao *AdminApiKeysDao) Table() string {
	return dao.table
}

// Columns returns all column names of the current DAO.
func (dao *AdminApiKeysDao) Columns() AdminApiKeysColumns {
	return dao.columns
}

// Group returns the database configuration group name of the current DAO.
func (dao *AdminApiKeysDao) Group() string {
	return dao.group
}

// Ctx creates and returns a Model for the current DAO. It automatically sets the context for the current operation.
func (dao *AdminApiKeysDao) Ctx(ctx context.Context) *gdb.Model {
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
func (dao *AdminApiKeysDao) Transaction(ctx context.Context, f func(ctx context.Context, tx gdb.TX) error) (err error) {
	return dao.Ctx(ctx).Transaction(ctx, f)
}
