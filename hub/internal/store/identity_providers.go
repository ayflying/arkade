package store

import (
	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/do"
	"arkade/hub/internal/model/entity"
	"context"
	"github.com/gogf/gf/v2/database/gdb"
)

func providerFrom(e entity.OidcProviders) OIDCProvider {
	return OIDCProvider{ID: e.Id, DisplayName: e.DisplayName, Issuer: e.Issuer, ClientID: e.ClientId, ClientSecretCipher: e.ClientSecretCipher, Scopes: e.Scopes, DefaultGroupID: e.DefaultGroupId, Enabled: e.Enabled != 0, AutoRegister: e.AutoRegister != 0, Version: int64(e.Version), CreatedAt: unixToTime(int64(e.CreatedAt)), UpdatedAt: unixToTime(int64(e.UpdatedAt))}
}
func (d *DB) GetOIDCProvider(ctx context.Context, id string) (OIDCProvider, error) {
	var e entity.OidcProviders
	err := scanOne(ctx, d.gdb.Model(dao.OidcProviders.Table()).Where("id", id), &e)
	return providerFrom(e), err
}
func (d *DB) ListOIDCProviders(ctx context.Context) ([]OIDCProvider, error) {
	var rows []entity.OidcProviders
	err := scanAll(ctx, d.gdb.Model(dao.OidcProviders.Table()).Order("id"), &rows)
	out := make([]OIDCProvider, 0, len(rows))
	for _, e := range rows {
		out = append(out, providerFrom(e))
	}
	return out, err
}
func (d *DB) CountProviderIdentities(ctx context.Context, id string) (int, error) {
	return d.gdb.Model(dao.UserIdentities.Table()).Ctx(ctx).Where("provider_id", id).Count()
}
func (d *DB) SaveOIDCProvider(ctx context.Context, p OIDCProvider, expectedVersion int64) error {
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		if p.DefaultGroupID != "" {
			n, err := tx.Model(dao.Groups.Table()).Ctx(ctx).Where("id", p.DefaultGroupID).Count()
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrRegistrationGroupUnavailable
			}
		}
		// Subject identifiers are scoped to the exact issuer and client. Never
		// reinterpret existing bindings under a different identity authority.
		if expectedVersion > 0 {
			var previous entity.OidcProviders
			if err := scanOne(ctx, tx.Model(dao.OidcProviders.Table()).Where("id", p.ID), &previous); err != nil {
				return err
			}
			if int64(previous.Version) != expectedVersion {
				return ErrConflict
			}
			if previous.Issuer != p.Issuer || previous.ClientId != p.ClientID {
				n, err := tx.Model(dao.UserIdentities.Table()).Ctx(ctx).Where("provider_id", p.ID).Count()
				if err != nil {
					return err
				}
				if n > 0 {
					return ErrProviderIdentityLocked
				}
			}
		}
		if expectedVersion > 0 && !p.Enabled {
			if err := protectProviderLogin(ctx, tx, p.ID); err != nil {
				return err
			}
		}
		var group any = gdb.Raw("NULL")
		if p.DefaultGroupID != "" {
			group = p.DefaultGroupID
		}
		data := do.OidcProviders{DisplayName: p.DisplayName, Issuer: p.Issuer, ClientId: p.ClientID, ClientSecretCipher: p.ClientSecretCipher, Scopes: p.Scopes, DefaultGroupId: group, Enabled: boolToInt(p.Enabled), AutoRegister: boolToInt(p.AutoRegister), Version: expectedVersion + 1, UpdatedAt: timeToUnix(p.UpdatedAt)}
		m := tx.Model(dao.OidcProviders.Table()).Ctx(ctx)
		if expectedVersion == 0 {
			data.Id = p.ID
			data.CreatedAt = timeToUnix(p.CreatedAt)
			_, err := m.Data(data).Insert()
			return normalizeExecError(err, "创建身份提供方")
		}
		result, err := m.Where("id", p.ID).Where("version", expectedVersion).Data(data).Update()
		if err != nil {
			return normalizeExecError(err, "更新身份提供方")
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrConflict
		}
		return nil
	})
}
func (d *DB) DeleteOIDCProvider(ctx context.Context, id string) error {
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		if err := protectProviderLogin(ctx, tx, id); err != nil {
			return err
		}
		result, err := tx.Model(dao.OidcProviders.Table()).Ctx(ctx).Where("id", id).Delete()
		if err != nil {
			return err
		}
		return requireAffected(result, "删除身份提供方")
	})
}

// Disabling/deleting a provider must not strand an account that has no local password.
func protectProviderLogin(ctx context.Context, tx gdb.TX, id string) error {
	value, err := tx.Ctx(ctx).GetValue(`SELECT count(*) FROM users u JOIN user_identities i ON i.user_id=u.id WHERE i.provider_id=? AND u.password_hash='' AND NOT EXISTS (SELECT 1 FROM user_identities j JOIN oidc_providers p ON p.id=j.provider_id WHERE j.user_id=u.id AND j.provider_id<>? AND p.enabled=1)`, id, id)
	if err != nil {
		return err
	}
	if value.Int() > 0 {
		return ErrLastLoginMethod
	}
	return nil
}
