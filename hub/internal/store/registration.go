package store

import (
	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/do"
	"arkade/hub/internal/model/entity"
	"arkade/hub/internal/user"
	"context"
	"errors"
	"github.com/gogf/gf/v2/database/gdb"
)

func (d *DB) RegisterLocalUser(ctx context.Context, u user.User, expectedSettingsVersion int64) error {
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		var s entity.SystemSettings
		if err := scanOne(ctx, tx.Model(dao.SystemSettings.Table()).Where("id", 1), &s); err != nil {
			return err
		}
		if s.RegistrationEnabled == 0 {
			return ErrRegistrationDisabled
		}
		if int64(s.Version) != expectedSettingsVersion {
			return ErrPasswordPolicyChanged
		}
		if u.PasswordHash == "" {
			return ErrConflict
		}
		u.Role = user.RoleUser
		u.Disabled = false
		u.MustChangePassword = false
		if err := insertRegisteredUser(ctx, tx, u); err != nil {
			return err
		}
		return addRegistrationGroup(ctx, tx, u.ID, s.RegistrationGroupId)
	})
}
func insertRegisteredUser(ctx context.Context, tx gdb.TX, u user.User) error {
	_, err := tx.Model(dao.Users.Table()).Ctx(ctx).Data(do.Users{Id: u.ID, Username: u.Username, DisplayName: u.DisplayName, PasswordHash: u.PasswordHash, Role: string(u.Role), Disabled: boolToInt(u.Disabled), MustChangePassword: boolToInt(u.MustChangePassword), CreatedAt: timeToUnix(u.CreatedAt), UpdatedAt: timeToUnix(u.UpdatedAt)}).Insert()
	return normalizeExecError(err, "注册用户")
}
func addRegistrationGroup(ctx context.Context, tx gdb.TX, userID, groupID string) error {
	if groupID == "" {
		return nil
	}
	n, err := tx.Model(dao.Groups.Table()).Ctx(ctx).Where("id", groupID).Count()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrRegistrationGroupUnavailable
	}
	_, err = tx.Model(dao.UserGroups.Table()).Ctx(ctx).Data(do.UserGroups{UserId: userID, GroupId: groupID}).Insert()
	return normalizeExecError(err, "加入注册用户组")
}
func (d *DB) ResolveOIDCIdentity(ctx context.Context, providerID string, version int64, subject string, newUser user.User, identityID string) (user.User, error) {
	var out user.User
	err := d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		p, err := liveProvider(ctx, tx, providerID, version)
		if err != nil {
			return err
		}
		var i entity.UserIdentities
		err = scanOne(ctx, tx.Model(dao.UserIdentities.Table()).Where("provider_id", providerID).Where("subject", subject), &i)
		if err == nil {
			var actor entity.Users
			if err := scanOne(ctx, tx.Model(dao.Users.Table()).Where("id", i.UserId), &actor); err != nil {
				return err
			}
			if actor.Disabled != 0 {
				return ErrNotFound
			}
			out = userFromEntity(actor)
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		if p.AutoRegister == 0 {
			return ErrRegistrationDisabled
		}
		newUser.PasswordHash = ""
		newUser.Role = user.RoleUser
		newUser.Disabled = false
		newUser.MustChangePassword = false
		if err := insertRegisteredUser(ctx, tx, newUser); err != nil {
			return err
		}
		if err := addRegistrationGroup(ctx, tx, newUser.ID, p.DefaultGroupId); err != nil {
			return err
		}
		if err := insertIdentity(ctx, tx, Identity{ID: identityID, UserID: newUser.ID, ProviderID: providerID, Subject: subject, CreatedAt: newUser.CreatedAt}); err != nil {
			return err
		}
		out = newUser
		return nil
	})
	return out, err
}
