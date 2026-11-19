// ==========================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// ==========================================================================

package internal

import (
	"context"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
)

// AutoAdoptStatesDao is the data access object for the table auto_adopt_states.
type AutoAdoptStatesDao struct {
	table    string                    // table is the underlying table name of the DAO.
	group    string                    // group is the database configuration group name of the current DAO.
	columns  AutoAdoptStatesColumns    // columns contains all the column names of Table for convenient usage.
	handlers []gdb.ModelHandler        // handlers for customized model modification.
}

// AutoAdoptStatesColumns defines and stores column names for the table auto_adopt_states.
type AutoAdoptStatesColumns struct {
	AgentId       string //
	Name          string //
	Status        string //
	Reason        string //
	Attempts      string //
	NextCheckAt   string //
	LastScannedAt string //
}

// autoAdoptStatesColumns holds the columns for the table auto_adopt_states.
var autoAdoptStatesColumns = AutoAdoptStatesColumns{
	AgentId:       "agent_id",
	Name:          "name",
	Status:        "status",
	Reason:        "reason",
	Attempts:      "attempts",
	NextCheckAt:   "next_check_at",
	LastScannedAt: "last_scanned_at",
}

// NewAutoAdoptStatesDao creates and returns a new DAO object for table data access.
func NewAutoAdoptStatesDao(handlers ...gdb.ModelHandler) *AutoAdoptStatesDao {
	return &AutoAdoptStatesDao{
		group:    "default",
		table:    "auto_adopt_states",
		columns:  autoAdoptStatesColumns,
		handlers: handlers,
	}
}

// DB retrieves and returns the underlying raw database management object of the current DAO.
func (dao *AutoAdoptStatesDao) DB() gdb.DB {
	return g.DB(dao.group)
}

// Table returns the table name of this dao.
func (dao *AutoAdoptStatesDao) Table() string {
	return dao.table
}

// Columns returns all column names of current table by default.
func (dao *AutoAdoptStatesDao) Columns() AutoAdoptStatesColumns {
	return dao.columns
}

// Group returns the configuration group name of database of this dao.
func (dao *AutoAdoptStatesDao) Group() string {
	return dao.group
}

// Ctx creates and returns a Model for the current dao.
func (dao *AutoAdoptStatesDao) Ctx(ctx context.Context) *gdb.Model {
	model := dao.DB().Model(dao.table)
	for _, handler := range dao.handlers {
		model = handler(model)
	}
	return model.Safe().Ctx(ctx)
}

// Transaction wraps the transaction logic using function f.
func (dao *AutoAdoptStatesDao) Transaction(ctx context.Context, f func(ctx context.Context, tx gdb.TX) error) (err error) {
	return dao.Ctx(ctx).Transaction(ctx, f)
}
