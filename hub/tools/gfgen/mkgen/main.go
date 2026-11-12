// Command mkgen applies the authoritative PostgreSQL migrations for gf generation.
package main

import (
	"arkade/hub/internal/dbmigrate"
	"context"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: mkgen <PostgreSQL DSN>")
		os.Exit(2)
	}
	res, err := dbmigrate.Run(context.Background(), os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "migration failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("migrated to v%d (%d applied)\n", res.ToVersion, res.Applied)
}
