package compose

import (
	api "arkade/hub/api/stacks/v1"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
	"sort"
)

func StackViewOf(rec store.ComposeStack, remote *envelope.ComposeProjectSummary) api.StackView {
	v := api.StackView{AgentID: rec.AgentID, Name: rec.Name, Managed: true, ConfigHash: rec.ConfigHash, Version: rec.Version, UpdatedAt: rec.UpdatedAt.Unix(), Services: []string{}, Status: "offline"}
	if !rec.SyncedAt.IsZero() {
		v.SyncedAt = rec.SyncedAt.Unix()
	}
	if remote == nil {
		return v
	}
	v.Status = remote.Status
	v.Services = remote.Services
	v.Running = remote.Running
	v.Total = remote.Total
	v.RemoteHash = remote.ConfigHash
	v.Drift = remote.ConfigHash != "" && remote.ConfigHash != rec.ConfigHash
	if v.Services == nil {
		v.Services = []string{}
	}
	return v
}
func ExternalViews(externals []envelope.ComposeProjectSummary, known map[string]struct{}) []api.ExternalStackView {
	out := make([]api.ExternalStackView, 0, len(externals))
	for _, p := range externals {
		if _, ok := known[p.Name]; ok {
			continue
		}
		services := p.Services
		if services == nil {
			services = []string{}
		}
		status := p.Status
		if status == "" {
			status = "unknown"
		}
		out = append(out, api.ExternalStackView{Name: p.Name, Status: status, Services: services, Running: p.Running, Total: p.Total, Managed: false})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func OpResultView(agent string, res envelope.ComposeOpResult) api.OpResult {
	return api.OpResult{AgentID: agent, Name: res.Name, Status: res.Status, ConfigHash: res.ConfigHash, Output: TruncateOutput(res.Output)}
}
func SyncResult(res envelope.ComposeOpResult, ok bool) *api.SyncResult {
	if !ok {
		return nil
	}
	return &api.SyncResult{Status: res.Status, Output: TruncateOutput(res.Output)}
}
