package store

// errors.go 定义仓储层的哨兵错误，并对驱动错误做统一归一化。
//
// 为什么要归一化：上层（httpx）需要把「重名」「记录不存在」「外键冲突」映射成
// 409 / 404 / 400，如果让上层直接断言 modernc 驱动的错误码，等于把 SQLite 细节
// 泄漏到业务层，未来换 PostgreSQL 时所有判断都要重写。这里只认一次。

import (
	"errors"
	"fmt"
	"strings"
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
// 判定依据：modernc.org/sqlite 的 *sqlite.Error 携带原始 SQLite 扩展错误码，
// 其中 SQLITE_CONSTRAINT_UNIQUE = 2067、SQLITE_CONSTRAINT_PRIMARYKEY = 1555。
// 但这里刻意**不直接断言类型**，而是同时匹配错误文本里的 "UNIQUE constraint failed"：
//   - 驱动类型断言虽然精确，但对驱动版本敏感；
//   - 文本匹配在不同版本间稳定（SQLite 自身的报错文案多年未变）。
//
// 两者取或，既能识别常见情况，又不会因为某一侧变化而整体失效。
func normalizeExecError(err error, dueTo string) error {
	if err == nil {
		return nil
	}
	if isUniqueViolation(err) {
		return fmt.Errorf("%w: %s", ErrConflict, dueTo)
	}
	return fmt.Errorf("%s失败: %w", dueTo, err)
}

// isUniqueViolation 判断错误是否由唯一约束触发。
func isUniqueViolation(err error) bool {
	msg := err.Error()
	if strings.Contains(msg, "UNIQUE constraint failed") {
		return true
	}
	// SQLITE_CONSTRAINT_UNIQUE(2067) / SQLITE_CONSTRAINT_PRIMARYKEY(1555)
	if strings.Contains(msg, "constraint failed (2067)") || strings.Contains(msg, "constraint failed (1555)") {
		return true
	}
	return errors.Is(err, ErrConflict)
}
