package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ComposeOperationRequest describes a request before any save or dispatch. Hashes
// and validation belong to HTTP. ConfigHash is the resulting saved configuration's
// hash; ExpectedHash (when nonempty) guards the original configuration.
// Save pointers distinguish omitted fields from intentional empty content.
type ComposeOperationRequest struct {
	AgentID, RequestID, Name, Action, Fingerprint string
	SaveContent, SaveEnv                          *string
	ExpectedVersion                               int64
	ExpectedHash                                  string
	Create                                        bool
	StackID, ConfigHash                           string
}

// ComposeOperation contains metadata only, never compose content, environment
// secrets, or raw Agent error output. StackID survives removal of its stack.
type ComposeOperation struct {
	AgentID, RequestID, Name, Action, Fingerprint string
	TaskID, StackID                               string
	StackVersion                                  int64
	ConfigHash                                    string
	CreatedAt                                     time.Time
	Finalized                                     bool
	FinalError                                    string
	RecordRemoved                                 bool
}

const composeOperationColumns = `agent_id, request_id, name, action, fingerprint, task_id, stack_id, stack_version, config_hash, created_at, finalized, final_error, record_removed`
const maxComposeOperations = 2048

func scanComposeOperation(row interface{ Scan(...any) error }) (ComposeOperation, error) {
	var op ComposeOperation
	var at int64
	err := row.Scan(&op.AgentID, &op.RequestID, &op.Name, &op.Action, &op.Fingerprint, &op.TaskID, &op.StackID, &op.StackVersion, &op.ConfigHash, &at, &op.Finalized, &op.FinalError, &op.RecordRemoved)
	if errors.Is(err, sql.ErrNoRows) {
		return op, ErrNotFound
	}
	if err != nil {
		return op, fmt.Errorf("查询编排操作失败: %w", err)
	}
	op.CreatedAt = unixToTime(at)
	return op, nil
}

// PrepareComposeOperation binds the idempotency key and saves configuration in
// one transaction. Duplicate requests never save again. Without a known task,
// only the exact bound stack can be returned for dispatch; newer content must
// never be substituted. With a task (or finalized result), stack is zero-valued.
// Retention is bounded: only finalized requests older than 24h can be pruned.
func (d *DB) PrepareComposeOperation(ctx context.Context, req ComposeOperationRequest) (op ComposeOperation, stack ComposeStack, duplicate bool, err error) {
	if req.AgentID == "" || req.RequestID == "" || req.Name == "" || req.Fingerprint == "" || req.Action == "" {
		return op, stack, false, ErrConflict
	}
	err = d.Tx(ctx, func(tx *sqlTx) error {
		existing, e := scanComposeOperation(tx.QueryRowContext(ctx, `SELECT `+composeOperationColumns+` FROM compose_operation_requests WHERE agent_id = ? AND request_id = ?`, req.AgentID, req.RequestID))
		if e == nil {
			duplicate, op = true, existing
			if op.Name != req.Name || op.Action != req.Action || op.Fingerprint != req.Fingerprint {
				return ErrConflict
			}
			if op.TaskID != "" || op.Finalized {
				return nil
			}
			stack, e = scanComposeStack(tx.QueryRowContext(ctx, `SELECT `+composeStackColumns+` FROM compose_stacks WHERE agent_id = ? AND name = ?`, req.AgentID, req.Name))
			if errors.Is(e, ErrNotFound) {
				return ErrConflict
			}
			if e != nil {
				return e
			}
			if !operationMatchesStack(op, stack) {
				stack = ComposeStack{}
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		now := time.Now().UTC()
		if _, e = tx.ExecContext(ctx, `DELETE FROM compose_operation_requests WHERE finalized = 1 AND created_at < ?`, now.Add(-24*time.Hour).Unix()); e != nil {
			return e
		}
		var count int
		if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM compose_operation_requests`).Scan(&count); e != nil {
			return e
		}
		if count >= maxComposeOperations {
			return fmt.Errorf("%w: 编排操作记录已满", ErrConflict)
		}
		stack, e = scanComposeStack(tx.QueryRowContext(ctx, `SELECT `+composeStackColumns+` FROM compose_stacks WHERE agent_id = ? AND name = ?`, req.AgentID, req.Name))
		if req.Create {
			if e == nil {
				return ErrConflict
			}
			if !errors.Is(e, ErrNotFound) {
				return e
			}
			if req.StackID == "" || req.SaveContent == nil || req.ConfigHash == "" {
				return ErrConflict
			}
			stack = ComposeStack{ID: req.StackID, AgentID: req.AgentID, Name: req.Name, Content: *req.SaveContent, ConfigHash: req.ConfigHash, Version: 1, CreatedAt: now, UpdatedAt: now}
			if req.SaveEnv != nil {
				stack.EnvContent = *req.SaveEnv
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO compose_stacks (`+composeStackColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, stack.ID, stack.AgentID, stack.Name, stack.Content, stack.EnvContent, stack.ConfigHash, stack.Version, timeToUnix(now), timeToUnix(now), 0)
			if e != nil {
				return normalizeExecError(e, "创建编排")
			}
		} else {
			if e != nil {
				return e
			}
			if req.ExpectedVersion != stack.Version || (req.ExpectedHash != "" && req.ExpectedHash != stack.ConfigHash) {
				return ErrConflict
			}
			if req.SaveContent != nil || req.SaveEnv != nil {
				if req.ConfigHash == "" {
					return ErrConflict
				}
				if req.SaveContent != nil {
					stack.Content = *req.SaveContent
				}
				if req.SaveEnv != nil {
					stack.EnvContent = *req.SaveEnv
				}
				stack, e = scanComposeStack(tx.QueryRowContext(ctx, `UPDATE compose_stacks SET content = ?, env_content = ?, config_hash = ?, version = version + 1, updated_at = ? WHERE id = ? AND agent_id = ? AND name = ? AND version = ? RETURNING `+composeStackColumns, stack.Content, stack.EnvContent, req.ConfigHash, timeToUnix(now), stack.ID, req.AgentID, req.Name, req.ExpectedVersion))
				if errors.Is(e, ErrNotFound) {
					return ErrConflict
				}
				if e != nil {
					return e
				}
			}
		}
		op = ComposeOperation{AgentID: req.AgentID, RequestID: req.RequestID, Name: req.Name, Action: req.Action, Fingerprint: req.Fingerprint, StackID: stack.ID, StackVersion: stack.Version, ConfigHash: stack.ConfigHash, CreatedAt: now}
		_, e = tx.ExecContext(ctx, `INSERT INTO compose_operation_requests (`+composeOperationColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, op.AgentID, op.RequestID, op.Name, op.Action, op.Fingerprint, "", op.StackID, op.StackVersion, op.ConfigHash, timeToUnix(now), false, "", false)
		return normalizeExecError(e, "保存编排操作")
	})
	if err != nil {
		stack = ComposeStack{}
	}
	return
}

func operationMatchesStack(op ComposeOperation, s ComposeStack) bool {
	return op.StackID == s.ID && op.StackVersion == s.Version && op.ConfigHash == s.ConfigHash
}

// AttachOperationTask binds a task exactly once. Reattaching the same ID is safe.
func (d *DB) AttachOperationTask(ctx context.Context, agentID, requestID, taskID string) error {
	if taskID == "" {
		return ErrConflict
	}
	return d.Tx(ctx, func(tx *sqlTx) error {
		op, err := scanComposeOperation(tx.QueryRowContext(ctx, `SELECT `+composeOperationColumns+` FROM compose_operation_requests WHERE agent_id = ? AND request_id = ?`, agentID, requestID))
		if err != nil {
			return err
		}
		if op.TaskID == taskID {
			return nil
		}
		if op.TaskID != "" || op.Finalized {
			return ErrConflict
		}
		// A task must identify only one operation, including across stack names.
		var n int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM compose_operation_requests WHERE agent_id = ? AND task_id = ?`, agentID, taskID).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return ErrConflict
		}
		_, err = tx.ExecContext(ctx, `UPDATE compose_operation_requests SET task_id = ? WHERE agent_id = ? AND request_id = ?`, taskID, agentID, requestID)
		return err
	})
}

func (d *DB) FindOperationByTask(ctx context.Context, agentID, name, taskID string) (ComposeOperation, error) {
	if taskID == "" {
		return ComposeOperation{}, ErrNotFound
	}
	return scanComposeOperation(d.db.QueryRowContext(ctx, `SELECT `+composeOperationColumns+` FROM compose_operation_requests WHERE agent_id = ? AND name = ? AND task_id = ?`, agentID, name, taskID))
}

// FinalizeComposeOperation reloads trusted metadata and applies a conditional
// up/remove effect once. A stale successful task is finalized but never marks
// a newer stack synced or deletes a replacement. Failures retain the stack.
func (d *DB) FinalizeComposeOperation(ctx context.Context, metadata ComposeOperation, succeeded bool) (op ComposeOperation, err error) {
	err = d.Tx(ctx, func(tx *sqlTx) error {
		var e error
		op, e = scanComposeOperation(tx.QueryRowContext(ctx, `SELECT `+composeOperationColumns+` FROM compose_operation_requests WHERE agent_id = ? AND request_id = ?`, metadata.AgentID, metadata.RequestID))
		if e != nil {
			return e
		}
		if metadata.Name != op.Name || metadata.Action != op.Action || metadata.Fingerprint != op.Fingerprint || metadata.TaskID != op.TaskID || metadata.StackID != op.StackID || metadata.StackVersion != op.StackVersion || metadata.ConfigHash != op.ConfigHash {
			return ErrConflict
		}
		if op.Finalized {
			return nil
		}
		if !succeeded {
			op.FinalError = "compose operation failed"
		} else {
			var result sql.Result
			switch op.Action {
			case "up":
				result, e = tx.ExecContext(ctx, `UPDATE compose_stacks SET synced_at = ? WHERE agent_id = ? AND name = ? AND id = ? AND version = ? AND config_hash = ?`, time.Now().UTC().Unix(), op.AgentID, op.Name, op.StackID, op.StackVersion, op.ConfigHash)
			case "remove":
				result, e = tx.ExecContext(ctx, `DELETE FROM compose_stacks WHERE agent_id = ? AND name = ? AND id = ? AND version = ? AND config_hash = ?`, op.AgentID, op.Name, op.StackID, op.StackVersion, op.ConfigHash)
			}
			if e != nil {
				return e
			}
			if result != nil {
				n, re := result.RowsAffected()
				if re != nil {
					return re
				}
				if n == 0 {
					op.FinalError = "stack changed or removed before finalization"
				} else if op.Action == "remove" {
					op.RecordRemoved = true
				}
			}
		}
		op.Finalized = true
		_, e = tx.ExecContext(ctx, `UPDATE compose_operation_requests SET finalized = 1, final_error = ?, record_removed = ? WHERE agent_id = ? AND request_id = ?`, op.FinalError, op.RecordRemoved, op.AgentID, op.RequestID)
		return e
	})
	return
}
