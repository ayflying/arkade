package store

import (
	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/do"
	"arkade/hub/internal/model/entity"
	"context"
	"github.com/gogf/gf/v2/database/gdb"
)

func protectLocalAdmin(ctx context.Context, tx gdb.TX, id string, removesLocal bool) error {
	if !removesLocal {
		return nil
	}
	var actor entity.Users
	if err := scanOne(ctx, tx.Model(dao.Users.Table()).Where("id", id), &actor); err != nil {
		return err
	}
	if actor.Role != "admin" || actor.Disabled != 0 || actor.PasswordHash == "" {
		return nil
	}
	n, err := tx.Model(dao.Users.Table()).Ctx(ctx).Where("role", "admin").Where("disabled", 0).WhereNot("password_hash", "").WhereNot("id", id).Count()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLastLocalAdmin
	}
	return nil
}
func (d *DB) ChangeLocalPassword(ctx context.Context, userID, expectedHash, newHash string, nowUnix int64, mustChange bool) error {
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		if err := protectLocalAdmin(ctx, tx, userID, newHash == ""); err != nil {
			return err
		}
		result, err := tx.Model(dao.Users.Table()).Ctx(ctx).Where("id", userID).Where("password_hash", expectedHash).Where("disabled", 0).Data(do.Users{PasswordHash: newHash, MustChangePassword: boolToInt(mustChange), UpdatedAt: nowUnix}).Update()
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrConflict
		}
		_, err = tx.Model(dao.Sessions.Table()).Ctx(ctx).Where("user_id", userID).Delete()
		return err
	})
}

// CreateOIDCSession validates the still-current provider, subject binding and
// actor in the same transaction that persists the resulting session.
func (d *DB) CreateOIDCSession(ctx context.Context, tokenHash, userID string, createdAt, expiresAt int64, providerID string, providerVersion int64, subject string) error {
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		if _, err := liveProvider(ctx, tx, providerID, providerVersion); err != nil {
			return err
		}
		var actor entity.Users
		if err := scanOne(ctx, tx.Model(dao.Users.Table()).Where("id", userID), &actor); err != nil {
			return err
		}
		if actor.Disabled != 0 {
			return ErrNotFound
		}
		n, err := tx.Model(dao.UserIdentities.Table()).Ctx(ctx).Where("provider_id", providerID).Where("subject", subject).Where("user_id", userID).Count()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrNotFound
		}
		_, err = tx.Model(dao.Sessions.Table()).Ctx(ctx).Data(do.Sessions{TokenHash: tokenHash, UserId: userID, CreatedAt: createdAt, ExpiresAt: expiresAt}).Insert()
		return normalizeExecError(err, "创建身份登录会话")
	})
}

func (d *DB) CreateUserSession(ctx context.Context, tokenHash, userID string, createdAt, expiresAt int64, expectedPasswordHash *string) error {
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		var actor entity.Users
		if err := scanOne(ctx, tx.Model(dao.Users.Table()).Where("id", userID), &actor); err != nil {
			return err
		}
		if actor.Disabled != 0 {
			return ErrNotFound
		}
		if expectedPasswordHash != nil && (actor.PasswordHash == "" || actor.PasswordHash != *expectedPasswordHash) {
			return ErrConflict
		}
		_, err := tx.Model(dao.Sessions.Table()).Ctx(ctx).Data(do.Sessions{TokenHash: tokenHash, UserId: userID, CreatedAt: createdAt, ExpiresAt: expiresAt}).Insert()
		return normalizeExecError(err, "创建会话")
	})
}
