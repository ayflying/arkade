package agents

import (
	api "arkade/hub/api/agents/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"context"
	"sort"
	"strings"
)

func (c *Controller) List(ctx context.Context, _ *api.ListReq) (*api.ListRes, error) {
	u, err := c.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	records, err := c.d.DB.ListAgents(ctx)
	if err != nil {
		return nil, internal("查询服务器列表失败", err)
	}
	online := c.d.Registry.OnlineIDs()
	grants := c.access().Grants(ctx, u)
	views := make([]api.AgentView, 0, len(records))
	for _, rec := range records {
		if controller.CanAccess(u, grants, rec.ID, user.PermView) {
			views = append(views, ToAgentView(rec, online))
		}
	}
	sort.SliceStable(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return &api.ListRes{Agents: views}, nil
}

// ToAgentView is shared by list/mutations: database online flags are never authoritative.
func ToAgentView(rec store.AgentRecord, online map[string]struct{}) api.AgentView {
	status := "offline"
	if _, ok := online[rec.ID]; ok {
		status = "online"
	} else if rec.Status == store.StatusPending {
		status = store.StatusPending
	}
	labels := rec.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	name := strings.TrimSpace(rec.DisplayName)
	if name == "" {
		name = rec.Name
	}
	return api.AgentView{ID: rec.ID, Name: rec.Name, DisplayName: name, Note: rec.Note, DirectAddress: rec.DirectAddress,
		Disabled: rec.Disabled, Status: status, Version: rec.Version, Runtime: rec.Runtime, RuntimeVersion: rec.RuntimeVersion, LastSeen: rec.LastSeen, Labels: labels}
}
func (c *Controller) mutationView(rec store.AgentRecord) api.AgentView {
	online := map[string]struct{}{}
	if c.d.Registry.IsOnline(rec.ID) {
		online[rec.ID] = struct{}{}
	}
	return ToAgentView(rec, online)
}
func (c *Controller) viewByID(ctx context.Context, id string) (api.AgentView, error) {
	rec, err := c.d.DB.GetAgent(ctx, id)
	if err != nil {
		return api.AgentView{}, c.storeError(err, "读取服务器信息失败")
	}
	return c.mutationView(rec), nil
}
