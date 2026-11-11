// ==========================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// ==========================================================================

package internal

import (
	"context"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
)

// GroupsDao is the data access object for the table groups.
type GroupsDao struct {
	table    string             // table is the underlying table name of the DAO.
	group    string             // group is the database configuration group name of the current DAO.
	columns  GroupsColumns      // columns contains all the column names of Table for convenient usage.
	handlers []gdb.ModelHandler // handlers for customized model modification.
}

// GroupsColumns defines and stores column names for the table groups.
type GroupsColumns struct {
	Id          string //
	Name        string //
	Description string //
	CreatedAt   string //
}

// groupsColumns holds the columns for the table groups.
var groupsColumns = GroupsColumns{
	Id:          "id",
	Name:        "name",
	Description: "description",
	CreatedAt:   "created_at",
}

// NewGroupsDao creates and returns a new DAO object for table data access.
func NewGroupsDao(handlers ...gdb.ModelHandler) *GroupsDao {
	return &GroupsDao{
		group:    "default",
		table:    "groups",
		columns:  groupsColumns,
		handlers: handlers,
	}
}

// DB retrieves and returns the underlying raw database management object of the current DAO.
func (dao *GroupsDao) DB() gdb.DB {
	return g.DB(dao.group)
}

// Table returns the table name of the current DAO.
func (dao *GroupsDao) Table() string {
	return dao.table
}

// Columns returns all column names of the current DAO.
func (dao *GroupsDao) Columns() GroupsColumns {
	return dao.columns
}

// Group returns the database configuration group name of the current DAO.
func (dao *GroupsDao) Group() string {
	return dao.group
}

// Ctx creates and returns a Model for the current DAO. It automatically sets the context for the current operation.
func (dao *GroupsDao) Ctx(ctx context.Context) *gdb.Model {
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
func (dao *GroupsDao) Transaction(ctx context.Context, f func(ctx context.Context, tx gdb.TX) error) (err error) {
	return dao.Ctx(ctx).Transaction(ctx, f)
}
