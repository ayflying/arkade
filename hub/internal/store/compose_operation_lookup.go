package store

import "context"

// FindOperationByRequest允许重试先确认已受理的任务，避免项目已删除时误判为新请求。
func (d *DB) FindOperationByRequest(ctx context.Context, agentID, requestID string) (ComposeOperation, error) {
	return scanComposeOperation(d.db.QueryRowContext(ctx, `SELECT `+composeOperationColumns+` FROM compose_operation_requests WHERE agent_id = ? AND request_id = ?`, agentID, requestID))
}
