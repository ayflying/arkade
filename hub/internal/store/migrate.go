package store

// migrate.go 是数据库迁移执行器（逻辑部分）。

import (
	"context"
	"fmt"
	"sort"
)

// migrate 按版本顺序执行未应用的迁移。
func (d *DB) migrate(ctx context.Context) error {
	if _, err := d.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("创建迁移记录表失败: %w", err)
	}

	applied, err := d.appliedVersions(ctx)
	if err != nil {
		return err
	}

	pending := make([]migration, 0, len(migrations))
	for _, m := range migrations {
		if _, ok := applied[m.version]; !ok {
			pending = append(pending, m)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].version < pending[j].version })

	for _, m := range pending {
		if err := d.applyMigration(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// appliedVersions 读取已执行的迁移版本号。
func (d *DB) appliedVersions(ctx context.Context) (map[int]struct{}, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("读取迁移记录失败: %w", err)
	}
	defer rows.Close()

	out := make(map[int]struct{})
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("扫描迁移记录失败: %w", err)
		}
		out[v] = struct{}{}
	}
	return out, rows.Err()
}

// applyMigration 在单个事务内执行一个迁移版本。
// 用事务包住的原因：迁移中途失败时不能留下「一半表建好了」的库，
// 否则下次启动会卡在不可预期的状态里。
func (d *DB) applyMigration(ctx context.Context, m migration) error {
	err := d.Tx(ctx, func(tx *sqlTx) error {
		for i, stmt := range m.stmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("迁移 v%d 第 %d 条语句失败: %w", m.version, i+1, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, unixepoch())`,
			m.version, m.name,
		); err != nil {
			return fmt.Errorf("记录迁移 v%d 失败: %w", m.version, err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("执行迁移 v%d(%s)失败: %w", m.version, m.name, err)
	}
	return nil
}
