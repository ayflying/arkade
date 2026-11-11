package audit

import (
	api "arkade/hub/api/audit/v1"
	"arkade/hub/internal/audit"
)

func itemView(rec audit.Record) api.Item {
	return api.Item{
		ID: rec.ID, UserID: rec.UserID, Username: rec.Username,
		AgentID: rec.AgentID, AgentName: rec.AgentName,
		Action: rec.Action, ActionLabel: audit.ActionLabel(rec.Action),
		Target: rec.Target, Detail: rec.Detail, Result: rec.Result, Error: rec.Error,
		DurationMs: rec.DurationMs, CreatedAt: rec.CreatedAt.Unix(),
	}
}

func actionViews() []api.ActionOption {
	all := audit.All()
	opts := make([]api.ActionOption, 0, len(all))
	// Keep the action registry's order, exactly as the old handler did.
	for _, a := range all {
		opts = append(opts, api.ActionOption{Action: a, Label: audit.ActionLabel(a)})
	}
	return opts
}
