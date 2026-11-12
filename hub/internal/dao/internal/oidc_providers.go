// ==========================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// ==========================================================================

package internal

import (
	"context"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
)

// OidcProvidersDao is the data access object for the table oidc_providers.
type OidcProvidersDao struct {
	table    string               // table is the underlying table name of the DAO.
	group    string               // group is the database configuration group name of the current DAO.
	columns  OidcProvidersColumns // columns contains all the column names of Table for convenient usage.
	handlers []gdb.ModelHandler   // handlers for customized model modification.
}

// OidcProvidersColumns defines and stores column names for the table oidc_providers.
type OidcProvidersColumns struct {
	Id                 string //
	DisplayName        string //
	Issuer             string //
	ClientId           string //
	ClientSecretCipher string //
	Scopes             string //
	Enabled            string //
	AutoRegister       string //
	DefaultGroupId     string //
	Version            string //
	CreatedAt          string //
	UpdatedAt          string //
}

// oidcProvidersColumns holds the columns for the table oidc_providers.
var oidcProvidersColumns = OidcProvidersColumns{
	Id:                 "id",
	DisplayName:        "display_name",
	Issuer:             "issuer",
	ClientId:           "client_id",
	ClientSecretCipher: "client_secret_cipher",
	Scopes:             "scopes",
	Enabled:            "enabled",
	AutoRegister:       "auto_register",
	DefaultGroupId:     "default_group_id",
	Version:            "version",
	CreatedAt:          "created_at",
	UpdatedAt:          "updated_at",
}

// NewOidcProvidersDao creates and returns a new DAO object for table data access.
func NewOidcProvidersDao(handlers ...gdb.ModelHandler) *OidcProvidersDao {
	return &OidcProvidersDao{
		group:    "default",
		table:    "oidc_providers",
		columns:  oidcProvidersColumns,
		handlers: handlers,
	}
}

// DB retrieves and returns the underlying raw database management object of the current DAO.
func (dao *OidcProvidersDao) DB() gdb.DB {
	return g.DB(dao.group)
}

// Table returns the table name of the current DAO.
func (dao *OidcProvidersDao) Table() string {
	return dao.table
}

// Columns returns all column names of the current DAO.
func (dao *OidcProvidersDao) Columns() OidcProvidersColumns {
	return dao.columns
}

// Group returns the database configuration group name of the current DAO.
func (dao *OidcProvidersDao) Group() string {
	return dao.group
}

// Ctx creates and returns a Model for the current DAO. It automatically sets the context for the current operation.
func (dao *OidcProvidersDao) Ctx(ctx context.Context) *gdb.Model {
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
func (dao *OidcProvidersDao) Transaction(ctx context.Context, f func(ctx context.Context, tx gdb.TX) error) (err error) {
	return dao.Ctx(ctx).Transaction(ctx, f)
}
