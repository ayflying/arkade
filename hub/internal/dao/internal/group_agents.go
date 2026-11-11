// ==========================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// ==========================================================================

package internal

import (
	"context"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
)

// GroupAgentsDao is the data access object for the table group_agents.
type GroupAgentsDao struct {
	table    string             // table is the underlying table name of the DAO.
	group    string             // group is the database configuration group name of the current DAO.
	columns  GroupAgentsColumns // columns contains all the column names of Table for convenient usage.
	handlers []gdb.ModelHandler // handlers for customized model modification.
}

// GroupAgentsColumns defines and stores column names for the table group_agents.
type GroupAgentsColumns struct {
	GroupId    string //
	AgentId    string //
	Permission string //
}

// groupAgentsColumns holds the columns for the table group_agents.
var groupAgentsColumns = GroupAgentsColumns{
	GroupId:    "group_id",
	AgentId:    "agent_id",
	Permission: "permission",
}

// NewGroupAgentsDao creates and returns a new DAO object for table data access.
func NewGroupAgentsDao(handlers ...gdb.ModelHandler) *GroupAgentsDao {
	return &GroupAgentsDao{
		group:    "default",
		table:    "group_agents",
		columns:  groupAgentsColumns,
		handlers: handlers,
	}
}

// DB retrieves and returns the underlying raw database management object of the current DAO.
func (dao *GroupAgentsDao) DB() gdb.DB {
	return g.DB(dao.group)
}

// Table returns the table name of the current DAO.
func (dao *GroupAgentsDao) Table() string {
	return dao.table
}

// Columns returns all column names of the current DAO.
func (dao *GroupAgentsDao) Columns() GroupAgentsColumns {
	return dao.columns
}

// Group returns the database configuration group name of the current DAO.
func (dao *GroupAgentsDao) Group() string {
	return dao.group
}

// Ctx creates and returns a Model for the current DAO. It automatically sets the context for the current operation.
func (dao *GroupAgentsDao) Ctx(ctx context.Context) *gdb.Model {
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
func (dao *GroupAgentsDao) Transaction(ctx context.Context, f func(ctx context.Context, tx gdb.TX) error) (err error) {
	return dao.Ctx(ctx).Transaction(ctx, f)
}
