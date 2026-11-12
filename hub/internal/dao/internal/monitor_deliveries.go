// ==========================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// ==========================================================================

package internal

import (
	"context"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
)

// MonitorDeliveriesDao is the data access object for the table monitor_deliveries.
type MonitorDeliveriesDao struct {
	table    string                   // table is the underlying table name of the DAO.
	group    string                   // group is the database configuration group name of the current DAO.
	columns  MonitorDeliveriesColumns // columns contains all the column names of Table for convenient usage.
	handlers []gdb.ModelHandler       // handlers for customized model modification.
}

// MonitorDeliveriesColumns defines and stores column names for the table monitor_deliveries.
type MonitorDeliveriesColumns struct {
	Id         string //
	IncidentId string //
	Channel    string //
	Event      string //
	Status     string //
	Error      string //
	CreatedAt  string //
}

// monitorDeliveriesColumns holds the columns for the table monitor_deliveries.
var monitorDeliveriesColumns = MonitorDeliveriesColumns{
	Id:         "id",
	IncidentId: "incident_id",
	Channel:    "channel",
	Event:      "event",
	Status:     "status",
	Error:      "error",
	CreatedAt:  "created_at",
}

// NewMonitorDeliveriesDao creates and returns a new DAO object for table data access.
func NewMonitorDeliveriesDao(handlers ...gdb.ModelHandler) *MonitorDeliveriesDao {
	return &MonitorDeliveriesDao{
		group:    "default",
		table:    "monitor_deliveries",
		columns:  monitorDeliveriesColumns,
		handlers: handlers,
	}
}

// DB retrieves and returns the underlying raw database management object of the current DAO.
func (dao *MonitorDeliveriesDao) DB() gdb.DB {
	return g.DB(dao.group)
}

// Table returns the table name of the current DAO.
func (dao *MonitorDeliveriesDao) Table() string {
	return dao.table
}

// Columns returns all column names of the current DAO.
func (dao *MonitorDeliveriesDao) Columns() MonitorDeliveriesColumns {
	return dao.columns
}

// Group returns the database configuration group name of the current DAO.
func (dao *MonitorDeliveriesDao) Group() string {
	return dao.group
}

// Ctx creates and returns a Model for the current DAO. It automatically sets the context for the current operation.
func (dao *MonitorDeliveriesDao) Ctx(ctx context.Context) *gdb.Model {
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
func (dao *MonitorDeliveriesDao) Transaction(ctx context.Context, f func(ctx context.Context, tx gdb.TX) error) (err error) {
	return dao.Ctx(ctx).Transaction(ctx, f)
}
