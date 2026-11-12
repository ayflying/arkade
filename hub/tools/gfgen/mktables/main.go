// Command mktables prints public business tables for official gf generation.
package main

import (
	"arkade/hub/internal/dbmigrate"
	"context"
	"database/sql"
	"fmt"
	_ "github.com/gogf/gf/contrib/drivers/pgsql/v2"
	"os"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: mktables <PostgreSQL DSN>")
		os.Exit(2)
	}
	db, err := sql.Open("postgres", os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer db.Close()
	rows, err := db.QueryContext(context.Background(), `SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ($1,$2) ORDER BY tablename`, dbmigrate.MigrationsTable, dbmigrate.ChecksumsTable)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		tables = append(tables, name)
	}
	if err = rows.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(strings.Join(tables, ","))
}
