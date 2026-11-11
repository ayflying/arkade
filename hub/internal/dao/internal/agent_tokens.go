// ==========================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// ==========================================================================

package internal

import (
	"context"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
)

// AgentTokensDao is the data access object for the table agent_tokens.
type AgentTokensDao struct {
	table    string             // table is the underlying table name of the DAO.
	group    string             // group is the database configuration group name of the current DAO.
	columns  AgentTokensColumns // columns contains all the column names of Table for convenient usage.
	handlers []gdb.ModelHandler // handlers for customized model modification.
}

// AgentTokensColumns defines and stores column names for the table agent_tokens.
type AgentTokensColumns struct {
	TokenHash string //
	AgentId   string //
	CreatedAt string //
}

// agentTokensColumns holds the columns for the table agent_tokens.
var agentTokensColumns = AgentTokensColumns{
	TokenHash: "token_hash",
	AgentId:   "agent_id",
	CreatedAt: "created_at",
}

// NewAgentTokensDao creates and returns a new DAO object for table data access.
func NewAgentTokensDao(handlers ...gdb.ModelHandler) *AgentTokensDao {
	return &AgentTokensDao{
		group:    "default",
		table:    "agent_tokens",
		columns:  agentTokensColumns,
		handlers: handlers,
	}
}

// DB retrieves and returns the underlying raw database management object of the current DAO.
func (dao *AgentTokensDao) DB() gdb.DB {
	return g.DB(dao.group)
}

// Table returns the table name of the current DAO.
func (dao *AgentTokensDao) Table() string {
	return dao.table
}

// Columns returns all column names of the current DAO.
func (dao *AgentTokensDao) Columns() AgentTokensColumns {
	return dao.columns
}

// Group returns the database configuration group name of the current DAO.
func (dao *AgentTokensDao) Group() string {
	return dao.group
}

// Ctx creates and returns a Model for the current DAO. It automatically sets the context for the current operation.
func (dao *AgentTokensDao) Ctx(ctx context.Context) *gdb.Model {
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
func (dao *AgentTokensDao) Transaction(ctx context.Context, f func(ctx context.Context, tx gdb.TX) error) (err error) {
	return dao.Ctx(ctx).Transaction(ctx, f)
}
