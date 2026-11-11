package migrate

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrHelperCleanup = errors.New("迁移临时容器清理失败，临时归档已保留，请检查引擎后重试清理")

// Only remove a helper whose unguessable ownership label matches. If inspect
// fails, an independent successful exact-name query must prove it is gone.
func runArchiveHelper(ctx context.Context, engine func(context.Context, ...string) ([]byte, error), args ...string) (out []byte, err error) {
	token := newExportID()
	name := "arkade-migrate-" + token
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		owner, inspectErr := engine(cleanup, "inspect", "--format", `{{index .Config.Labels "io.arkade.migration.owner"}}`, name)
		if inspectErr == nil {
			if strings.TrimSpace(string(owner)) != token {
				err = errors.Join(err, ErrHelperCleanup)
				return
			}
			if _, removeErr := engine(cleanup, "rm", "-f", name); removeErr == nil {
				return
			}
		}
		names, listErr := engine(cleanup, "ps", "-a", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}")
		if listErr == nil && strings.TrimSpace(string(names)) == "" {
			return
		}
		err = errors.Join(err, ErrHelperCleanup)
	}()
	full := []string{"run", "--rm", "--network", "none", "--name", name, "--label", importOwnerLabel + "=" + token}
	return engine(ctx, append(full, args...)...)
}
