// Package dbmigrate installs immutable, forward-only PostgreSQL migrations.
// It never adopts SQLite databases or attempts automatic dirty-state recovery.
package dbmigrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/gogf/gf/contrib/drivers/pgsql/v2"
	"github.com/gogf/gf/v2/errors/gerror"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations/*.up.sql
var migrationsFS embed.FS

const (
	MigrationsTable = "arkade_migrate_versions"
	ChecksumsTable  = "arkade_migrate_checksums"
	// Stable two-part application lock, scoped by PostgreSQL to the database.
	lockNamespace = 1095912257
	lockID        = 1
)

// Result reports this invocation; -1 means no previously applied version.
type Result struct {
	FromVersion int
	ToVersion   int
	Dirty       bool
	Applied     int
}

type migration struct {
	version              int
	name, body, checksum string
}

func scripts() ([]migration, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, gerror.Wrap(err, "read embedded migrations")
	}
	var out []migration
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		version, err := strconv.Atoi(prefix)
		if !ok || err != nil || version <= 0 {
			return nil, gerror.Newf("invalid migration filename %s", name)
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, gerror.Wrap(err, "read migration")
		}
		hash := sha256.Sum256(body)
		out = append(out, migration{version, name, string(body), hex.EncodeToString(hash[:])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	if len(out) == 0 {
		return nil, gerror.New("no embedded migrations")
	}
	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version {
			return nil, gerror.New("duplicate migration version")
		}
	}
	return out, nil
}

// Run accepts a lib/pq PostgreSQL URL or keyword DSN. The migration role needs
// CREATE on its database (for transactional schema verification), and owns public
// application objects. All application objects live in public, regardless of DSN
// search_path. A dedicated session holds an advisory lock across preflight,
// migration, checksums and verification. Waiting for that lock respects ctx.
func Run(ctx context.Context, dsn string) (*Result, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, gerror.New("PostgreSQL DSN is required")
	}
	files, err := scripts()
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, gerror.New("打开 PostgreSQL 迁移连接失败；请检查 DSN（详细参数已隐藏）")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, gerror.New("连接 PostgreSQL 迁移会话失败；请检查 DSN、凭据与网络（详细参数已隐藏）")
	}
	defer conn.Close()
	if err = acquireLock(ctx, conn); err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(cleanup, "SELECT pg_advisory_unlock($1,$2)", lockNamespace, lockID)
	}()
	if _, err = conn.ExecContext(ctx, "SET search_path TO public; SET lock_timeout TO '5s'; SET statement_timeout TO '5min'"); err != nil {
		return nil, gerror.Wrap(err, "set migration schema")
	}
	result := &Result{FromVersion: -1, ToVersion: -1}
	exists, err := tableExists(ctx, conn, MigrationsTable)
	if err != nil {
		return nil, err
	}
	if exists {
		rows, err := conn.QueryContext(ctx, "SELECT version,dirty FROM public."+MigrationsTable)
		if err != nil {
			return nil, gerror.Wrap(err, "read migration version")
		}
		count := 0
		for rows.Next() {
			count++
			if err = rows.Scan(&result.FromVersion, &result.Dirty); err != nil {
				rows.Close()
				return nil, gerror.Wrap(err, "scan migration version")
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, gerror.Wrap(err, "read migration version")
		}
		if count != 1 {
			return nil, gerror.Newf("migration version table has %d rows; expected one", count)
		}
		if result.Dirty {
			return nil, gerror.Newf("database is dirty at v%d; manual repair required", result.FromVersion)
		}
		known := false
		for _, f := range files {
			if f.version == result.FromVersion {
				known = true
			}
		}
		if !known {
			return nil, gerror.Newf("unsupported database migration version %d", result.FromVersion)
		}
		if err = verifyRecorded(ctx, conn, files, result.FromVersion); err != nil {
			return nil, err
		}
		if err = verifySchema(ctx, conn, files, result.FromVersion); err != nil {
			return nil, err
		}
	} else {
		var count int
		if err = conn.QueryRowContext(ctx, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p','v','m','S','f')`).Scan(&count); err != nil {
			return nil, gerror.Wrap(err, "inspect fresh schema")
		}
		if count != 0 {
			return nil, gerror.New("public schema has objects but no migration version; refusing unversioned database")
		}
	}
	driver, err := postgres.WithConnection(ctx, conn, &postgres.Config{MigrationsTable: MigrationsTable, SchemaName: "public"})
	if err != nil {
		return nil, gerror.Wrap(err, "initialize postgres migration driver")
	}
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return nil, gerror.Wrap(err, "open migration source")
	}
	defer src.Close()
	m, err := migrate.NewWithInstance("iofs", &forwardSource{Driver: src}, "postgres", &contextDriver{Postgres: driver, ctx: ctx, conn: conn, files: files})
	if err != nil {
		return nil, gerror.Wrap(err, "initialize migrator")
	}
	// The driver borrows conn; Run owns its lifecycle. Do not call m.Close here.
	if err = m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		if v, d, e := m.Version(); e == nil {
			result.ToVersion = int(v)
			result.Dirty = d
		}
		return result, gerror.Wrap(err, "apply PostgreSQL migrations; inspect dirty state before retry")
	}
	v, dirty, err := m.Version()
	if err != nil {
		return result, gerror.Wrap(err, "read final migration version")
	}
	result.ToVersion = int(v)
	result.Dirty = dirty
	for _, f := range files {
		if f.version > result.FromVersion && f.version <= result.ToVersion {
			result.Applied++
		}
	}
	if err = verifySchema(ctx, conn, files, result.ToVersion); err != nil {
		return result, err
	}
	if err = verifyRecorded(ctx, conn, files, result.ToVersion); err != nil {
		return result, err
	}
	return result, nil
}

func acquireLock(ctx context.Context, conn *sql.Conn) error {
	for {
		var locked bool
		if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1,$2)", lockNamespace, lockID).Scan(&locked); err != nil {
			return gerror.Wrap(err, "acquire migration advisory lock")
		}
		if locked {
			return nil
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return gerror.Wrap(ctx.Err(), "wait for migration advisory lock")
		case <-timer.C:
		}
	}
}
func tableExists(ctx context.Context, conn *sql.Conn, name string) (bool, error) {
	var yes bool
	err := conn.QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", "public."+name).Scan(&yes)
	return yes, gerror.Wrap(err, "inspect migration table")
}
func verifyRecorded(ctx context.Context, conn *sql.Conn, files []migration, upto int) error {
	exists, err := tableExists(ctx, conn, ChecksumsTable)
	if err != nil {
		return err
	}
	if !exists {
		return gerror.New("migration checksum table missing")
	}
	rows, err := conn.QueryContext(ctx, "SELECT version,checksum FROM public."+ChecksumsTable)
	if err != nil {
		return gerror.Wrap(err, "read migration checksums")
	}
	defer rows.Close()
	sums := map[int]string{}
	for rows.Next() {
		var v int
		var sum string
		if err = rows.Scan(&v, &sum); err != nil {
			return gerror.Wrap(err, "scan checksum")
		}
		sums[v] = sum
	}
	if err = rows.Err(); err != nil {
		return gerror.Wrap(err, "read checksums")
	}
	for _, f := range files {
		if f.version > upto {
			break
		}
		sum, ok := sums[f.version]
		if !ok {
			return gerror.Newf("checksum for applied migration v%d missing", f.version)
		}
		if sum != f.checksum {
			return gerror.Newf("checksum mismatch for migration v%d", f.version)
		}
		delete(sums, f.version)
	}
	if len(sums) > 0 {
		return gerror.New("unexpected migration checksum versions")
	}
	return nil
}

type forwardSource struct{ source.Driver }

func (s *forwardSource) ReadDown(version uint) (io.ReadCloser, string, error) {
	return nil, "", gerror.Newf("forward-only migrations: down v%d is forbidden", version)
}

// contextDriver 将 DDL 与不可变 SHA256 校验记录置于同一事务。
// 若提交成功后、migrate 清除 dirty 前崩溃，下次仍须人工检查，绝不自动恢复。
// Run 的外层会话级 advisory 锁覆盖校验和执行，跨进程同样互斥。
type contextDriver struct {
	*postgres.Postgres
	ctx   context.Context
	conn  *sql.Conn
	files []migration
}

// 已由 Run 在任何检查/建表之前获取数据库级会话锁；避免 migrate 再用
// background context 等待另一把锁，保证取消后不会遗留后台锁等待。
func (d *contextDriver) Lock() error   { return d.ctx.Err() }
func (d *contextDriver) Unlock() error { return nil }

func (d *contextDriver) Run(r io.Reader) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return gerror.Wrap(err, "read migration SQL")
	}
	hash := sha256.Sum256(body)
	sum := hex.EncodeToString(hash[:])
	version := 0
	for _, f := range d.files {
		if f.checksum == sum {
			version = f.version
			break
		}
	}
	if version == 0 {
		return gerror.New("migration SQL has no embedded checksum")
	}
	tx, err := d.conn.BeginTx(d.ctx, nil)
	if err != nil {
		return gerror.Wrap(err, "begin migration")
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(d.ctx, string(body)); err != nil {
		return gerror.Wrap(err, "execute migration SQL")
	}
	if _, err = tx.ExecContext(d.ctx, "INSERT INTO public."+ChecksumsTable+"(version,checksum) VALUES($1,$2)", version, sum); err != nil {
		return gerror.Wrap(err, "write migration checksum")
	}
	return gerror.Wrap(tx.Commit(), "commit migration")
}
