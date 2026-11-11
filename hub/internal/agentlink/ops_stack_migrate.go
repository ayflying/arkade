package agentlink

import (
	"context"
	"strings"

	"arkade/protocol/envelope"
)

// ComposeMigrationPlan asks the source Agent for its normalized compose model.
// No YAML interpretation, volume name guessing, or env filtering lives on Hub.
func (o *Ops) ComposeMigrationPlan(ctx context.Context, agent string, req envelope.ComposeMigrationPlanPayload) (envelope.ComposeMigrationPlanResult, *ConnError) {
	var out envelope.ComposeMigrationPlanResult
	if err := o.migrationCall(ctx, agent, envelope.MethodComposeMigrationPlan, req, &out); err != nil {
		return out, err
	}
	return out, validateComposeMigrationPlan(req, out)
}

// ComposeImport only writes configuration on the destination; never starts it.
// Migration cannot opt into replacing an existing project.
func (o *Ops) ComposeImport(ctx context.Context, agent string, req envelope.ComposeImportPayload) (envelope.ComposeImportResult, *ConnError) {
	var out envelope.ComposeImportResult
	if req.Overwrite {
		return out, transferInvalid("迁移不允许覆盖已有编排")
	}
	if err := o.migrationCall(ctx, agent, envelope.MethodComposeImport, req, &out); err != nil {
		return out, err
	}
	return out, validateComposeImport(req, out)
}

// ComposeProjectHash is SHA-256 truncated to 16 hex characters on both ends.
// Archive SHA-256 is a distinct 64-character contract and remains unchanged.
func isComposeProjectHash(hash string) bool {
	if len(hash) != 16 {
		return false
	}
	for _, c := range hash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
func validateComposeMigrationPlan(req envelope.ComposeMigrationPlanPayload, out envelope.ComposeMigrationPlanResult) *ConnError {
	if out.Name != req.Name || out.Config == "" || !isComposeProjectHash(out.ConfigHash) {
		return transferMalformed("Agent 返回的编排迁移规划无效")
	}
	return nil
}
func validateComposeImport(req envelope.ComposeImportPayload, out envelope.ComposeImportResult) *ConnError {
	if out.AlreadyExists {
		return &ConnError{Code: envelope.CodeConflict, Message: "目标服务器上已存在同名编排，请先改名后再迁移"}
	}
	if out.Name != req.Name || !isComposeProjectHash(out.ConfigHash) || (req.ExpectHash != "" && !strings.EqualFold(req.ExpectHash, out.ConfigHash)) {
		return transferMalformed("Agent 返回的编排配置确认信息不一致")
	}
	return nil
}
