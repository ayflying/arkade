// ==========================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// ==========================================================================

package internal

import (
	"context"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
)

// AgentsDao is the data access object for the table agents.
type AgentsDao struct {
	table    string             // table is the underlying table name of the DAO.
	group    string             // group is the database configuration group name of the current DAO.
	columns  AgentsColumns      // columns contains all the column names of Table for convenient usage.
	handlers []gdb.ModelHandler // handlers for customized model modification.
}

// AgentsColumns defines and stores column names for the table agents.
type AgentsColumns struct {
	Id             string //
	Name           string //
	Status         string //
	Version        string //
	Runtime        string //
	RuntimeVersion string //
	LastSeen       string //
	Labels         string //
	CreatedAt      string //
	UpdatedAt      string //
	DisplayName    string //
	Note           string //
	Disabled       string //
	Preprovisioned string //
	DirectAddress  string //
}

// agentsColumns holds the columns for the table agents.
var agentsColumns = AgentsColumns{
	Id:             "id",
	Name:           "name",
	Status:         "status",
	Version:        "version",
	Runtime:        "runtime",
	RuntimeVersion: "runtime_version",
	LastSeen:       "last_seen",
	Labels:         "labels",
	CreatedAt:      "created_at",
	UpdatedAt:      "updated_at",
	DisplayName:    "display_name",
	Note:           "note",
	Disabled:       "disabled",
	Preprovisioned: "preprovisioned",
	DirectAddress:  "direct_address",
}

// NewAgentsDao creates and returns a new DAO object for table data access.
func NewAgentsDao(handlers ...gdb.ModelHandler) *AgentsDao {
	return &AgentsDao{
		group:    "default",
		table:    "agents",
		columns:  agentsColumns,
		handlers: handlers,
	}
}

// DB retrieves and returns the underlying raw database management object of the current DAO.
func (dao *AgentsDao) DB() gdb.DB {
	return g.DB(dao.group)
}

// Table returns the table name of the current DAO.
func (dao *AgentsDao) Table() string {
	return dao.table
}

// Columns returns all column names of the current DAO.
func (dao *AgentsDao) Columns() AgentsColumns {
	return dao.columns
}

// Group returns the database configuration group name of the current DAO.
func (dao *AgentsDao) Group() string {
	return dao.group
}

// Ctx creates and returns a Model for the current DAO. It automatically sets the context for the current operation.
func (dao *AgentsDao) Ctx(ctx context.Context) *gdb.Model {
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
func (dao *AgentsDao) Transaction(ctx context.Context, f func(ctx context.Context, tx gdb.TX) error) (err error) {
	return dao.Ctx(ctx).Transaction(ctx, f)
}
