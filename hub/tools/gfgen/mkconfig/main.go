// Command mkconfig renders a GF configuration from a PostgreSQL URL for codegen.
package main

import (
	"encoding/json"
	"fmt"
	"github.com/gogf/gf/v2/database/gdb"
	"net/url"
	"os"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: mkconfig <PostgreSQL URL>")
		os.Exit(2)
	}
	u, err := url.Parse(os.Args[1])
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		fmt.Fprintln(os.Stderr, "invalid PostgreSQL URL")
		os.Exit(2)
	}
	user, pass := "", ""
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	query := u.Query()
	query.Set("search_path", "public")
	node := gdb.ConfigNode{Type: "pgsql", Host: u.Hostname(), Port: port, User: user, Pass: pass, Name: strings.TrimPrefix(u.Path, "/"), Namespace: "public", Extra: query.Encode()}
	config := struct {
		Database map[string]gdb.ConfigNode `json:"database"`
	}{map[string]gdb.ConfigNode{"default": node}}
	if err = json.NewEncoder(os.Stdout).Encode(config); err != nil {
		fmt.Fprintln(os.Stderr, "cannot render GF config")
		os.Exit(1)
	}
}
