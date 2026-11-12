package store

import (
	"context"
	"database/sql"
	"net/url"
	"strings"
	"time"

	_ "github.com/gogf/gf/contrib/drivers/pgsql/v2"
	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/errors/gerror"
)

const driverName = "postgres"

// openGDB accepts PostgreSQL URL or libpq keyword DSNs, never local files.
// Extra values are URL-encoded for GoFrame then libpq-quoted for the driver.
func openGDB(dsn string) (gdb.DB, error) {
	params, err := parseDSN(dsn)
	if err != nil {
		return nil, err
	}
	// Schema is an application invariant, not a caller-controlled DSN option.
	params.Set("search_path", "public")
	for key, values := range params {
		if len(values) != 1 {
			return nil, gerror.Newf("PostgreSQL DSN 参数 %s 重复", key)
		}
		for _, c := range key {
			if !(c >= 'a' && c <= 'z' || c == '_') {
				return nil, gerror.New("PostgreSQL DSN 参数名无效")
			}
		}
		params.Set(key, "'"+strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(values[0])+"'")
	}
	inst, err := gdb.New(gdb.ConfigNode{
		Type: "pgsql", Host: "localhost", User: "postgres", Namespace: "public",
		Extra: params.Encode(), MaxOpenConnCount: 20, MaxIdleConnCount: 10,
		MaxConnLifeTime: 30 * time.Minute, TimeMaintainDisabled: true,
	})
	if err != nil {
		return nil, gerror.New("打开 PostgreSQL 数据库失败（请检查数据库配置）")
	}
	return inst, nil
}

// parseDSN parses URL and libpq keyword DSNs without embedding secrets in errors.
func parseDSN(dsn string) (url.Values, error) {
	params := url.Values{}
	invalid := func() (url.Values, error) {
		return nil, gerror.New("无效 PostgreSQL DSN（需要 postgres URL 或 libpq keyword DSN）")
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil || u.Hostname() == "" || strings.TrimPrefix(u.Path, "/") == "" {
			return invalid()
		}
		params, err = url.ParseQuery(u.RawQuery)
		if err != nil {
			return invalid()
		}
		params.Set("host", u.Hostname())
		if u.Port() != "" {
			params.Set("port", u.Port())
		}
		params.Set("dbname", strings.TrimPrefix(u.Path, "/"))
		if u.User != nil {
			params.Set("user", u.User.Username())
			if password, ok := u.User.Password(); ok {
				params.Set("password", password)
			}
		}
		return params, nil
	}
	for rest := strings.TrimSpace(dsn); rest != ""; {
		i := strings.IndexByte(rest, '=')
		if i <= 0 {
			return invalid()
		}
		key := strings.TrimSpace(rest[:i])
		rest = strings.TrimLeft(rest[i+1:], " \t\r\n")
		var value strings.Builder
		quoted := len(rest) > 0 && rest[0] == '\''
		if quoted {
			rest = rest[1:]
		}
		closed := !quoted
		for len(rest) > 0 {
			c := rest[0]
			rest = rest[1:]
			if c == '\\' {
				if rest == "" {
					return invalid()
				}
				value.WriteByte(rest[0])
				rest = rest[1:]
				continue
			}
			if quoted && c == '\'' {
				closed = true
				break
			}
			if !quoted && strings.ContainsRune(" \t\r\n", rune(c)) {
				break
			}
			value.WriteByte(c)
		}
		if !closed || (quoted && rest != "" && !strings.ContainsRune(" \t\r\n", rune(rest[0]))) {
			return invalid()
		}
		params.Set(key, value.String())
		rest = strings.TrimSpace(rest)
	}
	if params.Get("dbname") == "" {
		return invalid()
	}
	return params, nil
}

func masterOf(inst gdb.DB) (*sql.DB, error) {
	db, err := inst.Master()
	if err != nil {
		closeGDB(inst)
		return nil, gerror.Wrap(err, "取得数据库连接失败")
	}
	if db == nil {
		closeGDB(inst)
		return nil, gerror.New("未取得有效数据库连接")
	}
	return db, nil
}

func closeGDB(inst gdb.DB) {
	if inst != nil {
		_ = inst.Close(context.Background())
	}
}
