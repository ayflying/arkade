// ==========================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// ==========================================================================

package internal

import (
	"context"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
)

// MonitorSamplesDao is the data access object for the table monitor_samples.
type MonitorSamplesDao struct {
	table    string               // table is the underlying table name of the DAO.
	group    string               // group is the database configuration group name of current DAO.
	columns  MonitorSamplesColumns // columns contains all the column names of Table for convenient usage.
	handlers []gdb.ModelHandler   // handlers for customized model modification.
}

// MonitorSamplesColumns defines and stores column names for the table monitor_samples.
type MonitorSamplesColumns struct {
	AgentId             string //
	ContainerId         string //
	Project             string //
	ContainerName       string //
	BucketAt            string //
	LastAt              string //
	State               string //
	StatsAvailable      string //
	CpuPercent          string //
	MemoryBytes         string //
	MemoryLimitBytes    string //
	MemoryPercent       string //
	NetworkAvailable    string //
	NetworkRxPerSecond  string //
	NetworkTxPerSecond  string //
	BlockAvailable      string //
	BlockReadPerSecond  string //
	BlockWritePerSecond string //
	RatesAvailable      string //
	SnapshotComplete    string //
}

// monitorSamplesColumns holds the columns for the table monitor_samples.
var monitorSamplesColumns = MonitorSamplesColumns{
	AgentId:             "agent_id",
	ContainerId:         "container_id",
	Project:             "project",
	ContainerName:       "container_name",
	BucketAt:            "bucket_at",
	LastAt:              "last_at",
	State:               "state",
	StatsAvailable:      "stats_available",
	CpuPercent:          "cpu_percent",
	MemoryBytes:         "memory_bytes",
	MemoryLimitBytes:    "memory_limit_bytes",
	MemoryPercent:       "memory_percent",
	NetworkAvailable:    "network_available",
	NetworkRxPerSecond:  "network_rx_per_second",
	NetworkTxPerSecond:  "network_tx_per_second",
	BlockAvailable:      "block_available",
	BlockReadPerSecond:  "block_read_per_second",
	BlockWritePerSecond: "block_write_per_second",
	RatesAvailable:      "rates_available",
	SnapshotComplete:    "snapshot_complete",
}

// NewMonitorSamplesDao creates and returns a new DAO object for table data access.
func NewMonitorSamplesDao(handlers ...gdb.ModelHandler) *MonitorSamplesDao {
	return &MonitorSamplesDao{
		group:    "default",
		table:    "monitor_samples",
		columns:  monitorSamplesColumns,
		handlers: handlers,
	}
}

// DB retrieves and returns the underlying raw database management object of current DAO.
func (dao *MonitorSamplesDao) DB() gdb.DB {
	return g.DB(dao.group)
}

// Table returns the table name of current DAO.
func (dao *MonitorSamplesDao) Table() string {
	return dao.table
}

// Columns returns all column names of current DAO.
func (dao *MonitorSamplesDao) Columns() MonitorSamplesColumns {
	return dao.columns
}

// Group returns the database configuration group name of current DAO.
func (dao *MonitorSamplesDao) Group() string {
	return dao.group
}

// Ctx creates and returns a Model for current DAO. It automatically sets the context for current operation.
func (dao *MonitorSamplesDao) Ctx(ctx context.Context) *gdb.Model {
	model := dao.DB().Model(dao.table)
	for _, handler := range dao.handlers {
		model = handler(model)
	}
	return model.Safe().Ctx(ctx)
}

// Transaction wraps the transaction logic using function f.
// It rollbacks the transaction and returns the error if function f returns a non-nil error.
// It commits the transaction and returns nil if function f returns nil.
//
// Note: Do not commit or rollback the transaction in function f,
// as it is automatically handled by this function.
func (dao *MonitorSamplesDao) Transaction(ctx context.Context, f func(ctx context.Context, tx gdb.TX) error) (err error) {
	return dao.Ctx(ctx).Transaction(ctx, f)
}
