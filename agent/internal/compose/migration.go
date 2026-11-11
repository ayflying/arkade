package compose

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"sort"
	"strings"

	"arkade/agent/internal/compose/migrate"
)

// MigrationPlan holds exact source files plus engine-normalized resource identities.
type MigrationPlan struct {
	Name, Config, Env, ConfigHash string
	Images                        []string
	Volumes                       migrate.Plan
	Busy                          bool
}

func (m *Manager) MigrationPlan(ctx context.Context, name string) (MigrationPlan, error) {
	n, err := cleanName(name)
	if err != nil {
		return MigrationPlan{}, err
	}
	plan := MigrationPlan{Name: n, Images: []string{}}
	var readErr error
	if !m.tryWithProject(n, func() {
		plan.Config, readErr = m.ws.Read(n)
		if readErr != nil {
			return
		}
		plan.Env, readErr = m.ws.ReadEnv(n)
		if readErr != nil {
			return
		}
		plan.ConfigHash = HashProject(plan.Config, plan.Env)
		res := m.run(ctx, n, TimeoutShort, "config", "--format", "json")
		if res.ExitCode != 0 || res.TimedOut {
			readErr = errors.New("无法规范化源编排配置，已拒绝迁移")
			return
		}
		var doc struct {
			Services map[string]struct {
				Image string `json:"image"`
			}
		}
		if json.Unmarshal([]byte(res.Stdout), &doc) != nil {
			readErr = errors.New("引擎返回的规范化编排无效")
			return
		}
		seen := map[string]bool{}
		for _, svc := range doc.Services {
			if image := strings.TrimSpace(svc.Image); image != "" && !seen[image] {
				seen[image] = true
				plan.Images = append(plan.Images, image)
			}
		}
		sort.Strings(plan.Images)
		plan.Volumes, readErr = migrate.ResolvePlan(n, res.Stdout)
	}) {
		plan.Busy = true
	}
	return plan, readErr
}

// ImportMigration shares the same lock as up/adopt/remove: check and commit cannot race.
func (m *Manager) ImportMigration(ctx context.Context, req migrate.ComposeArchive) (migrate.ComposeImportResult, error) {
	n, err := cleanName(req.Name)
	if err != nil {
		return migrate.ComposeImportResult{}, err
	}
	var result migrate.ComposeImportResult
	err = m.withProject(ctx, n, func() error {
		if m.ws.Exists(n) {
			return migrate.ErrComposeExists
		}
		if m.labelSource != nil {
			labels, err := m.labelSource.ListContainerLabels(ctx)
			if err != nil {
				return errors.New("无法确认目标引擎同名编排，已拒绝导入")
			}
			for _, container := range labels {
				if container.Labels["com.docker.compose.project"] == n {
					return migrate.ErrComposeExists
				}
			}
		}
		check := m.checkConfig(ctx, n, req.Config, req.Env)
		if !check.OK {
			return migrate.ErrComposeContentInvalid
		}
		req.Overwrite = false
		var err error
		result, err = migrate.ImportComposition(workspaceMigrationImporter{m.ws}, req)
		return err
	})
	return result, err
}

type workspaceMigrationImporter struct{ ws *Workspace }

func (w workspaceMigrationImporter) Exists(name string) bool { return w.ws.Exists(name) }
func (w workspaceMigrationImporter) Commit(name, config, env string) error {
	_, _, err := w.ws.CommitProject(name, config, env)
	return err
}

// MigrationEngineCommand uses the exact configured engine endpoint and sanitised environment.
func (m *Manager) MigrationEngineCommand(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, m.runner.binary(), args...)
	cmd.Env = m.runner.environ()
	return cmd.CombinedOutput()
}
