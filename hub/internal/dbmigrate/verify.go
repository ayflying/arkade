package dbmigrate

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/gogf/gf/v2/errors/gerror"
)

// verifySchema 在同一 PG 的临时 schema 中重放已执行迁移，只读比较列、
// 约束、索引和触发器的规范化元数据。事务始终回滚，不保留临时对象；
// 不维护第二份手写 schema，也不解析或拆分 SQL。
func verifySchema(ctx context.Context, conn *sql.Conn, files []migration, upto int) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return gerror.Wrap(err, "begin schema verification")
	}
	defer tx.Rollback()
	schema := fmt.Sprintf("arkade_verify_%d", time.Now().UnixNano())
	if _, err = tx.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		return gerror.Wrap(err, "create rollback-only verification schema")
	}
	if _, err = tx.ExecContext(ctx, `SET LOCAL search_path TO "`+schema+`", pg_catalog`); err != nil {
		return gerror.Wrap(err, "set verification schema")
	}
	for _, f := range files {
		if f.version > upto {
			break
		}
		if _, err = tx.ExecContext(ctx, f.body); err != nil {
			return gerror.Wrapf(err, "rebuild expected schema at v%d", f.version)
		}
	}
	want, err := schemaSnapshot(ctx, tx, schema)
	if err != nil {
		return err
	}
	got, err := schemaSnapshot(ctx, tx, "public")
	if err != nil {
		return err
	}
	if strings.Join(want, "\n") != strings.Join(got, "\n") {
		for i := 0; i < len(want) && i < len(got); i++ {
			if want[i] != got[i] {
				return gerror.Newf("database schema drift at v%d: expected %s; got %s", upto, want[i], got[i])
			}
		}
		return gerror.Newf("database schema drift at v%d: expected %d objects; got %d", upto, len(want), len(got))
	}
	return nil
}
func schemaSnapshot(ctx context.Context, tx *sql.Tx, schema string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
 SELECT item FROM (
 SELECT 'table|'||c.relname||'|'||c.relkind::text AS item
 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname=$1 AND c.relname<>$2 AND c.relkind IN ('r','p','v','m','S','f')
 UNION ALL
 SELECT 'column|'||c.relname||'|'||a.attnum||'|'||a.attname||'|'||format_type(a.atttypid,a.atttypmod)||'|'||a.attnotnull||'|'||COALESCE(pg_get_expr(d.adbin,d.adrelid),'')
 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid
 LEFT JOIN pg_attrdef d ON d.adrelid=c.oid AND d.adnum=a.attnum
 WHERE n.nspname=$1 AND c.relname<>$2 AND c.relkind IN ('r','p','v','m','f') AND a.attnum>0 AND NOT a.attisdropped
 UNION ALL
 SELECT 'constraint|'||c.relname||'|'||con.conname||'|'||pg_get_constraintdef(con.oid,true)
 FROM pg_constraint con JOIN pg_class c ON c.oid=con.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname=$1 AND c.relname<>$2
 UNION ALL
 SELECT 'index|'||t.relname||'|'||i.relname||'|'||pg_get_indexdef(i.oid)
 FROM pg_index x JOIN pg_class i ON i.oid=x.indexrelid JOIN pg_class t ON t.oid=x.indrelid JOIN pg_namespace n ON n.oid=t.relnamespace
 WHERE n.nspname=$1 AND t.relname<>$2
 UNION ALL
 SELECT 'trigger|'||c.relname||'|'||t.tgname||'|'||pg_get_triggerdef(t.oid)
 FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname=$1 AND c.relname<>$2 AND NOT t.tgisinternal
 ) s ORDER BY item`, schema, MigrationsTable)
	if err != nil {
		return nil, gerror.Wrap(err, "read PostgreSQL schema metadata")
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var item string
		if err = rows.Scan(&item); err != nil {
			return nil, gerror.Wrap(err, "scan schema metadata")
		}
		item = strings.ReplaceAll(item, `"`+schema+`".`, "")
		item = strings.ReplaceAll(item, schema+".", "")
		out = append(out, item)
	}
	return out, gerror.Wrap(rows.Err(), "read schema metadata")
}
