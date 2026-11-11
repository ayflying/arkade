package compose

import (
	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
	"context"
	"log/slog"
)

func DispatchUp(ctx context.Context, db *store.DB, ops *agentlink.Ops, log *slog.Logger, agent string, rec store.ComposeStack) (envelope.ComposeOpResult, bool, string) {
	res, cerr := ops.ComposeUp(ctx, agent, envelope.ComposeSpecPayload{Name: rec.Name, Content: rec.Content, EnvContent: rec.EnvContent, ContentHash: rec.ConfigHash})
	if cerr != nil {
		msg := "编排已保存，但下发到 Agent 失败：" + cerr.Message
		if cerr.Code == envelope.CodeAgentOffline {
			msg = "编排已保存，但 Agent 当前离线，稍后可重新下发"
		}
		log.Warn("下发编排失败，记录已保留", "agent", agent, "name", rec.Name, "err", cerr)
		return envelope.ComposeOpResult{}, false, msg
	}
	if err := db.MarkComposeStackSynced(ctx, agent, rec.Name); err != nil {
		log.Error("记录编排同步时间失败", "agent", agent, "name", rec.Name, "err", err)
	}
	return res, true, ""
}
func SimpleAction(ctx context.Context, ops *agentlink.Ops, agent, action string, p envelope.ComposeOpPayload) (envelope.ComposeOpResult, *agentlink.ConnError) {
	switch action {
	case "stop":
		return ops.ComposeStop(ctx, agent, p)
	case "restart":
		return ops.ComposeRestart(ctx, agent, p)
	case "pull":
		return ops.ComposePull(ctx, agent, p)
	case "rebuild":
		return ops.ComposeRebuild(ctx, agent, p)
	}
	return envelope.ComposeOpResult{}, &agentlink.ConnError{Code: envelope.CodeBadRequest, Message: "不支持的动作: " + action}
}
