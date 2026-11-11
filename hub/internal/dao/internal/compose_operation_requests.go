// ==========================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// ==========================================================================

package internal

import (
	"context"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
)

// ComposeOperationRequestsDao is the data access object for the table compose_operation_requests.
type ComposeOperationRequestsDao struct {
	table    string                          // table is the underlying table name of the DAO.
	group    string                          // group is the database configuration group name of the current DAO.
	columns  ComposeOperationRequestsColumns // columns contains all the column names of Table for convenient usage.
	handlers []gdb.ModelHandler              // handlers for customized model modification.
}

// ComposeOperationRequestsColumns defines and stores column names for the table compose_operation_requests.
type ComposeOperationRequestsColumns struct {
	AgentId       string //
	RequestId     string //
	Name          string //
	Action        string //
	Fingerprint   string //
	TaskId        string //
	StackId       string //
	StackVersion  string //
	ConfigHash    string //
	CreatedAt     string //
	Finalized     string //
	FinalError    string //
	RecordRemoved string //
}

// composeOperationRequestsColumns holds the columns for the table compose_operation_requests.
var composeOperationRequestsColumns = ComposeOperationRequestsColumns{
	AgentId:       "agent_id",
	RequestId:     "request_id",
	Name:          "name",
	Action:        "action",
	Fingerprint:   "fingerprint",
	TaskId:        "task_id",
	StackId:       "stack_id",
	StackVersion:  "stack_version",
	ConfigHash:    "config_hash",
	CreatedAt:     "created_at",
	Finalized:     "finalized",
	FinalError:    "final_error",
	RecordRemoved: "record_removed",
}

// NewComposeOperationRequestsDao creates and returns a new DAO object for table data access.
func NewComposeOperationRequestsDao(handlers ...gdb.ModelHandler) *ComposeOperationRequestsDao {
	return &ComposeOperationRequestsDao{
		group:    "default",
		table:    "compose_operation_requests",
		columns:  composeOperationRequestsColumns,
		handlers: handlers,
	}
}

// DB retrieves and returns the underlying raw database management object of the current DAO.
func (dao *ComposeOperationRequestsDao) DB() gdb.DB {
	return g.DB(dao.group)
}

// Table returns the table name of the current DAO.
func (dao *ComposeOperationRequestsDao) Table() string {
	return dao.table
}

// Columns returns all column names of the current DAO.
func (dao *ComposeOperationRequestsDao) Columns() ComposeOperationRequestsColumns {
	return dao.columns
}

// Group returns the database configuration group name of the current DAO.
func (dao *ComposeOperationRequestsDao) Group() string {
	return dao.group
}

// Ctx creates and returns a Model for the current DAO. It automatically sets the context for the current operation.
func (dao *ComposeOperationRequestsDao) Ctx(ctx context.Context) *gdb.Model {
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
func (dao *ComposeOperationRequestsDao) Transaction(ctx context.Context, f func(ctx context.Context, tx gdb.TX) error) (err error) {
	return dao.Ctx(ctx).Transaction(ctx, f)
}
