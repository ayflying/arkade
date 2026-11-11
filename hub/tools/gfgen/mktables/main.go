// Command mktables 打印库中的业务表名（逗号分隔），供 DAO 生成使用。
//
// ============================ 为什么不手写表名清单 ============================
// 手写清单会漏：新增表时忘了更新，生成出的 DAO 就缺那几张，
// 而缺了不会报错——直到有人写代码时才发现 dao.X 不存在。
//
// 从库的真实结构读，保证"库里有几个业务表就生成几个"。
//
// 排除项：迁移体系自己建的表（版本表、哈希表）不是业务表，
// 给它们生成 DAO 只会在 store 里多出一堆没人用的代码。
//
// 用法：
//
//	go run ./tools/gfgen/mktables <库路径>
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"

	// 引入驱动以注册 "sqlite"（与 hub 使用同一个驱动）。
	_ "github.com/gogf/gf/contrib/drivers/sqlite/v2"

	"arkade/hub/internal/dbmigrate"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法: mktables <库路径>")
		os.Exit(2)
	}

	db, err := sql.Open("sqlite", os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开库失败: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.QueryContext(context.Background(), `
		SELECT name FROM sqlite_master
		WHERE type='table'
		  AND name NOT LIKE 'sqlite_%'
		ORDER BY name`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读表名失败: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	// 迁移体系自建的表，不生成 DAO。
	skip := map[string]bool{
		dbmigrate.MigrationsTable: true,
		dbmigrate.ChecksumsTable:  true,
		"schema_migrations":       true, // 旧体系的表，若存在也不是业务表
	}

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			fmt.Fprintf(os.Stderr, "扫描表名失败: %v\n", err)
			os.Exit(1)
		}
		if skip[name] {
			continue
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "遍历表名失败: %v\n", err)
		os.Exit(1)
	}

	sort.Strings(tables)
	fmt.Println(strings.Join(tables, ","))
}
