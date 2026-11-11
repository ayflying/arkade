// dbtool performs offline integrity checks and consistent, non-overwriting snapshots.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"arkade/hub/internal/dbbackup"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: dbtool <backup|restore|check> -source database [-destination new-file]")
		os.Exit(2)
	}
	flags := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	source := flags.String("source", "", "source SQLite database")
	destination := flags.String("destination", "", "new destination file (must not exist)")
	timeout := flags.Duration("timeout", 5*time.Minute, "operation timeout")
	_ = flags.Parse(os.Args[2:])
	if *source == "" || flags.NArg() != 0 {
		flags.Usage()
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	var err error
	switch os.Args[1] {
	case "check":
		err = dbbackup.Check(ctx, *source)
	case "backup", "restore":
		// Restore deliberately targets a NEW path. Stop Hub and configure that path
		// after checking it; never overwrite a live main file while WAL files exist.
		if *destination == "" {
			flags.Usage()
			os.Exit(2)
		}
		err = dbbackup.Snapshot(ctx, *source, *destination)
	default:
		fmt.Fprintln(os.Stderr, "unknown operation:", os.Args[1])
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("ok")
}
