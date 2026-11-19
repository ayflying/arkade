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

var ErrAPIKeyOwnerIneligible = gerror.New("账户已停用或须先修改密码，无法管理 MCP 密钥")

// APIKey has only public metadata. The digest is write/look-up only.
type APIKey struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

func (d *DB) InsertAPIKey(ctx context.Context, userID string, key APIKey, digest string) error {
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		// Serialize with owner updates even outside the application's advisory
		// lock, so the eligibility check cannot race ahead of trigger revocation.
		rec, err := tx.QueryContext(ctx, `SELECT id FROM users WHERE id = $1 AND disabled = 0 AND must_change_password = 0 FOR SHARE`, userID)
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
			return ErrAPIKeyOwnerIneligible
		}
		_, err = tx.Model(dao.AdminApiKeys.Table()).Ctx(ctx).Data(do.AdminApiKeys{
			Id: key.ID, UserId: userID, Name: key.Name, TokenHash: digest, CreatedAt: timeToUnix(key.CreatedAt),
		}).Insert()
		return normalizeExecError(err, "创建 MCP 密钥")
	})
}

func (d *DB) ListAPIKeys(ctx context.Context, userID string) ([]APIKey, error) {
	c := dao.AdminApiKeys.Columns()
	var rows []entity.AdminApiKeys
	if err := scanAll(ctx, d.gdb.Model(dao.AdminApiKeys.Table()).Fields(c.Id, c.Name, c.CreatedAt).
		Where(c.UserId, userID).OrderDesc(c.CreatedAt).OrderAsc(c.Id), &rows); err != nil {
		return nil, gerror.Wrap(err, "查询 MCP 密钥列表失败")
	}
	out := make([]APIKey, 0, len(rows))
	for _, row := range rows {
		out = append(out, APIKey{ID: row.Id, Name: row.Name, CreatedAt: unixToTime(int64(row.CreatedAt)).UTC()})
	}
	return out, nil
}

func (d *DB) RevokeAPIKey(ctx context.Context, userID, id string) error {
	c := dao.AdminApiKeys.Columns()
	res, err := d.gdb.Model(dao.AdminApiKeys.Table()).Ctx(ctx).Where(c.Id, id).Where(c.UserId, userID).Delete()
	if err != nil {
		return gerror.Wrap(err, "撤销 MCP 密钥失败")
	}
	return requireAffected(res, "撤销 MCP 密钥")
}

// AuthenticateAPIKey uses one snapshot for both the key and the live user
// state. Do not select the password hash: MCP has no password use case.
func (d *DB) AuthenticateAPIKey(ctx context.Context, id, digest string) (user.User, error) {
	var row entity.Users
	err := scanOne(ctx, d.gdb.Model(dao.Users.Table()).As("u").Fields("u.id", "u.username", "u.display_name", "u.role", "u.created_at", "u.updated_at").
		InnerJoin(dao.AdminApiKeys.Table()+" k", "k.user_id = u.id").
		Where("k.id", id).Where("k.token_hash", digest).
		Where("u.disabled", 0).Where("u.must_change_password", 0), &row)
	if errors.Is(err, ErrNotFound) {
		return user.User{}, ErrNotFound
	}
	if err != nil {
		return user.User{}, gerror.Wrap(err, "验证 MCP 密钥失败")
	}
	return userFromEntity(row), nil
}
