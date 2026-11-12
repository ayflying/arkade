package store

import (
	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/do"
	"arkade/hub/internal/model/entity"
	"context"
	"github.com/gogf/gf/v2/database/gdb"
)

func identityFrom(e entity.UserIdentities) Identity {
	return Identity{ID: e.Id, UserID: e.UserId, ProviderID: e.ProviderId, Subject: e.Subject, CreatedAt: unixToTime(int64(e.CreatedAt))}
}
func (d *DB) GetIdentity(ctx context.Context, provider, subject string) (Identity, error) {
	var e entity.UserIdentities
	err := scanOne(ctx, d.gdb.Model(dao.UserIdentities.Table()).Where("provider_id", provider).Where("subject", subject), &e)
	return identityFrom(e), err
}
func (d *DB) ListUserIdentities(ctx context.Context, userID string) ([]Identity, error) {
	var rows []entity.UserIdentities
	err := scanAll(ctx, d.gdb.Model(dao.UserIdentities.Table()).Where("user_id", userID).Order("id"), &rows)
	out := make([]Identity, 0, len(rows))
	for _, e := range rows {
		out = append(out, identityFrom(e))
	}
	return out, err
}
func (d *DB) CreateIdentity(ctx context.Context, i Identity) error {
	p, err := d.GetOIDCProvider(ctx, i.ProviderID)
	if err != nil {
		return err
	}
	return d.BindOIDCIdentity(ctx, i.ProviderID, p.Version, i)
}
func (d *DB) BindOIDCIdentity(ctx context.Context, provider string, version int64, i Identity) error {
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		if _, err := liveProvider(ctx, tx, provider, version); err != nil {
			return err
		}
		var actor entity.Users
		if err := scanOne(ctx, tx.Model(dao.Users.Table()).Where("id", i.UserID), &actor); err != nil {
			return err
		}
		if actor.Disabled != 0 {
			return ErrNotFound
		}
		i.ProviderID = provider
		return insertIdentity(ctx, tx, i)
	})
}
func insertIdentity(ctx context.Context, tx gdb.TX, i Identity) error {
	_, err := tx.Model(dao.UserIdentities.Table()).Ctx(ctx).Data(do.UserIdentities{Id: i.ID, UserId: i.UserID, ProviderId: i.ProviderID, Subject: i.Subject, CreatedAt: timeToUnix(i.CreatedAt)}).Insert()
	return normalizeExecError(err, "绑定身份")
}
func liveProvider(ctx context.Context, tx gdb.TX, id string, version int64) (entity.OidcProviders, error) {
	var p entity.OidcProviders
	err := scanOne(ctx, tx.Model(dao.OidcProviders.Table()).Where("id", id), &p)
	if err != nil {
		return p, err
	}
	if p.Enabled == 0 || int64(p.Version) != version {
		return p, ErrProviderUnavailable
	}
	return p, nil
}
func (d *DB) DeleteIdentity(ctx context.Context, userID, id string) error {
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		var i entity.UserIdentities
		if err := scanOne(ctx, tx.Model(dao.UserIdentities.Table()).Where("id", id).Where("user_id", userID), &i); err != nil {
			return err
		}
		var actor entity.Users
		if err := scanOne(ctx, tx.Model(dao.Users.Table()).Where("id", userID), &actor); err != nil {
			return err
		}
		if actor.PasswordHash == "" {
			v, err := tx.Ctx(ctx).GetValue(`SELECT count(*) FROM user_identities i JOIN oidc_providers p ON p.id=i.provider_id WHERE i.user_id=? AND i.id<>? AND p.enabled=1`, userID, id)
			if err != nil {
				return err
			}
			if v.Int() == 0 {
				return ErrLastLoginMethod
			}
		}
		result, err := tx.Model(dao.UserIdentities.Table()).Ctx(ctx).Where("id", id).Where("user_id", userID).Delete()
		if err != nil {
			return err
		}
		return requireAffected(result, "解绑身份")
	})
}
