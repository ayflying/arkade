package store

import (
	"context"
	"errors"
	"time"

	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/do"
	"arkade/hub/internal/model/entity"
	"arkade/hub/internal/user"
	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/errors/gerror"
)

var ErrAdminAPIKeyOwnerIneligible = gerror.New("MCP 密钥持有人必须是有效管理员")

// AdminAPIKey has only public metadata. The digest is write/look-up only.
type AdminAPIKey struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

func (d *DB) InsertAdminAPIKey(ctx context.Context, userID string, key AdminAPIKey, digest string) error {
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		// Serialize with owner updates even outside the application's advisory
		// lock, so the eligibility check cannot race ahead of trigger revocation.
		rec, err := tx.QueryContext(ctx, `SELECT id FROM users WHERE id = $1 AND role = $2 AND disabled = 0 AND must_change_password = 0 FOR SHARE`, userID, string(user.RoleAdmin))
		if err != nil {
			return gerror.Wrap(err, "检查 MCP 密钥持有人失败")
		}
		eligible := rec.Next()
		if err := rec.Close(); err != nil {
			return gerror.Wrap(err, "检查 MCP 密钥持有人失败")
		}
		if err := rec.Err(); err != nil {
			return gerror.Wrap(err, "检查 MCP 密钥持有人失败")
		}
		if !eligible {
			return ErrAdminAPIKeyOwnerIneligible
		}
		_, err = tx.Model(dao.AdminApiKeys.Table()).Ctx(ctx).Data(do.AdminApiKeys{
			Id: key.ID, UserId: userID, Name: key.Name, TokenHash: digest, CreatedAt: timeToUnix(key.CreatedAt),
		}).Insert()
		return normalizeExecError(err, "创建 MCP 密钥")
	})
}

func (d *DB) ListAdminAPIKeys(ctx context.Context, userID string) ([]AdminAPIKey, error) {
	c := dao.AdminApiKeys.Columns()
	var rows []entity.AdminApiKeys
	if err := scanAll(ctx, d.gdb.Model(dao.AdminApiKeys.Table()).Fields(c.Id, c.Name, c.CreatedAt).
		Where(c.UserId, userID).OrderDesc(c.CreatedAt).OrderAsc(c.Id), &rows); err != nil {
		return nil, gerror.Wrap(err, "查询 MCP 密钥列表失败")
	}
	out := make([]AdminAPIKey, 0, len(rows))
	for _, row := range rows {
		out = append(out, AdminAPIKey{ID: row.Id, Name: row.Name, CreatedAt: unixToTime(int64(row.CreatedAt)).UTC()})
	}
	return out, nil
}

func (d *DB) RevokeAdminAPIKey(ctx context.Context, userID, id string) error {
	c := dao.AdminApiKeys.Columns()
	res, err := d.gdb.Model(dao.AdminApiKeys.Table()).Ctx(ctx).Where(c.Id, id).Where(c.UserId, userID).Delete()
	if err != nil {
		return gerror.Wrap(err, "撤销 MCP 密钥失败")
	}
	return requireAffected(res, "撤销 MCP 密钥")
}

// AuthenticateAdminAPIKey uses one snapshot for both the key and the live user
// state. Do not select the password hash: MCP has no password use case.
func (d *DB) AuthenticateAdminAPIKey(ctx context.Context, id, digest string) (user.User, error) {
	var row entity.Users
	err := scanOne(ctx, d.gdb.Model(dao.Users.Table()).As("u").Fields("u.id", "u.username", "u.display_name", "u.role", "u.created_at", "u.updated_at").
		InnerJoin(dao.AdminApiKeys.Table()+" k", "k.user_id = u.id").
		Where("k.id", id).Where("k.token_hash", digest).
		Where("u.role", string(user.RoleAdmin)).Where("u.disabled", 0).Where("u.must_change_password", 0), &row)
	if errors.Is(err, ErrNotFound) {
		return user.User{}, ErrNotFound
	}
	if err != nil {
		return user.User{}, gerror.Wrap(err, "验证 MCP 密钥失败")
	}
	return userFromEntity(row), nil
}
