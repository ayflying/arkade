package store

// PostgreSQL SQLSTATE errors are normalized once at the repository boundary.

import (
	"errors"

	"github.com/lib/pq"

	"github.com/gogf/gf/v2/errors/gerror"
)

// ErrNotFound 是统一的「记录不存在」错误。
// 用哨兵错误而不是把 sql.ErrNoRows 泄漏到上层，避免各层重复判断驱动细节。
var ErrNotFound = errors.New("记录不存在")

// ErrConflict 表示唯一约束冲突（用户名/组名重复）。
// 由 ExecContext 的错误归一化成这个哨兵，上层映射为 409 conflict。
var ErrConflict = errors.New("记录已存在")

// ErrDuplicate 是 ErrConflict 的语义化别名，仅用于「字段级」判断。
// 上层用 errors.Is(err, ErrConflict) 即可，不需要知道是哪个字段冲突；
// 冲突字段名由消息文案带出（例如「用户名已存在」）。
var ErrDuplicate = ErrConflict

// normalizeExecError 把驱动返回的错误转换成仓储层哨兵错误。
//
// 仅唯一约束 SQLSTATE 23505 映射为重复记录；外键、非空、CHECK 和事务故障
// 可能代表程序错误或需要重试，保留原始错误链，不能伪装成「记录已存在」。
func normalizeExecError(err error, dueTo string) error {
	if err == nil {
		return nil
	}
	switch sqlState(err) {
	case "23505":
		return gerror.Wrap(ErrConflict, dueTo)
	}
	if errors.Is(err, ErrConflict) {
		return gerror.Wrap(ErrConflict, dueTo)
	}
	return gerror.Wrapf(err, "%s失败", dueTo)
}

// sqlState recognizes structured PostgreSQL errors through GoFrame wrapping.
func sqlState(err error) string {
	var pg *pq.Error
	if errors.As(err, &pg) {
		return string(pg.Code)
	}
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		return state.SQLState()
	}
	return ""
}

// isUniqueViolation 判断错误是否由唯一约束触发。
func isUniqueViolation(err error) bool {
	return sqlState(err) == "23505" || errors.Is(err, ErrConflict)
}
