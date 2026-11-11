package stackmigration

import (
	"context"
	"errors"
	"sort"

	"arkade/hub/internal/service/compose"
	"arkade/hub/internal/stackmigrate"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
	"github.com/gogf/gf/v2/errors/gerror"
)

func (b *Backend) CanManage(ctx context.Context, agent, name string) (bool, error) {
	if _, err := b.deps.DB.GetAgent(ctx, agent); err != nil {
		return false, err
	}
	id := stackmigrate.OwnerIDFromContext(ctx)
	if id == "" {
		return false, nil
	}
	actor, err := b.deps.DB.GetUserByID(ctx, id)
	if err != nil {
		return false, err
	}
	if actor.Disabled {
		return false, nil
	}
	if actor.Role == user.RoleAdmin {
		return true, nil
	}
	grants, err := b.deps.DB.ListUserGrants(ctx, id)
	if err != nil {
		return false, err
	}
	for _, grant := range grants {
		if grant.AgentID == agent && grant.Permission.Satisfies(user.PermManage) {
			return true, nil
		}
	}
	return false, nil
}
func (b *Backend) AgentOnline(agent string) bool { return b.deps.Registry.IsOnline(agent) }
func (b *Backend) StackExists(ctx context.Context, agent, name string) (bool, error) {
	_, err := b.deps.DB.GetComposeStack(ctx, agent, name)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	out, cerr := b.deps.Ops.ComposeList(ctx, agent)
	if cerr != nil {
		return false, cerr
	}
	for _, list := range [][]envelope.ComposeProjectSummary{out.Projects, out.External} {
		for _, p := range list {
			if p.Name == name {
				return true, nil
			}
		}
	}
	return false, nil
}
func (b *Backend) StackBusy(ctx context.Context, agent, name string) (bool, error) {
	p, err := b.deps.Ops.ComposeMigrationPlan(ctx, agent, envelope.ComposeMigrationPlanPayload{Name: name})
	if err != nil {
		return false, err
	}
	return p.Busy, nil
}
func (b *Backend) plan(ctx context.Context, agent, name string) (envelope.ComposeMigrationPlanResult, error) {
	p, err := b.deps.Ops.ComposeMigrationPlan(ctx, agent, envelope.ComposeMigrationPlanPayload{Name: name})
	if err != nil {
		return p, err
	}
	if p.Busy {
		return p, gerror.New("源端编排正在操作")
	}
	saved, e := b.deps.DB.GetComposeStack(ctx, agent, name)
	if e != nil {
		return p, gerror.New("源编排未由Hub管理，无法安全迁移")
	}
	hash := compose.ProjectHash(p.Config, p.Env)
	if p.Name != name || hash != p.ConfigHash || saved.ConfigHash != hash || saved.Content != p.Config || saved.EnvContent != p.Env {
		return p, gerror.New("源编排配置尚未同步或发生变化，请同步后重新迁移")
	}
	if err := compose.ValidateContent(p.Config); err != nil {
		return p, gerror.New("源编排配置无效")
	}
	if err := compose.ValidateEnv(p.Env); err != nil {
		return p, gerror.New("源编排环境配置无效")
	}
	return p, nil
}
func (b *Backend) ReadCompose(ctx context.Context, agent, name string) (string, string, string, error) {
	p, err := b.plan(ctx, agent, name)
	if err != nil {
		return "", "", "", err
	}
	return p.Config, p.Env, p.ConfigHash, nil
}
func (b *Backend) ImagesOf(ctx context.Context, agent, name string) ([]string, error) {
	p, err := b.plan(ctx, agent, name)
	if err != nil {
		return nil, err
	}
	return unique(p.Images), nil
}
func (b *Backend) VolumesOf(ctx context.Context, agent, name string) (stackmigrate.VolumePlan, error) {
	p, err := b.plan(ctx, agent, name)
	if err != nil {
		return stackmigrate.VolumePlan{}, err
	}
	out := stackmigrate.VolumePlan{BindDirs: len(p.BindMounts), Anonymous: len(p.AnonymousMounts)}
	for _, v := range p.Volumes {
		if v.Name == "" {
			return out, gerror.New("源端返回无效卷计划")
		}
		switch v.Kind {
		case "named", "explicit":
		case "external":
			out.External = append(out.External, v.Name)
		default:
			return out, gerror.New("源端返回未知卷类型")
		}
		out.Volumes = append(out.Volumes, v.Name)
	}
	out.Volumes = unique(out.Volumes)
	out.External = unique(out.External)
	return out, nil
}
func unique(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
