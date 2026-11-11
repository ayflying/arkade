package store

// legacy_helper_test.go 给测试提供"底层的 *sql.DB"，供仍需要裸 SQL 的用例使用
// （以 SQL 直接灌数据后核对磁盘内容、迁移就绪检查等）。
//
// 生产代码不再持有 *sql.DB 字段，所以这里从 gdb 的 Master() 现取：
// 它与 ORM 用的是同一个底层池，只是拿到句柄本身。
// 只放在测试文件里，避免生产 DB 为了"顺手"重新长出第二个连接句柄。

import (
	"database/sql"
	"testing"
)

// rawDBOf 返回 d 底层的 *sql.DB，仅测试可用。
//
// 取不到连接时直接 panic：这些 helper 只被测试调用，
// 取不到说明前置状态已经坏了，没必要为它设计一套错误返回。
func rawDBOf(t *testing.T, d *DB) *sql.DB {
	t.Helper()
	return d.mustRaw()
}

// mustRaw 返回底层的 *sql.DB。仅供 legacy 测试用的辅助方法。
func (d *DB) mustRaw() *sql.DB {
	raw, err := d.gdb.Master()
	if err != nil {
		panic("取底层连接失败: " + err.Error())
	}
	return raw
}
