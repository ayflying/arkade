package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/errors/gerror"

	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/do"
	"arkade/hub/internal/model/entity"
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

const maxComposeOperations = 2048

// composeOperationFields 返回查询列名。
func composeOperationFields() []string {
	c := dao.ComposeOperationRequests.Columns()
	return []string{c.AgentId, c.RequestId, c.Name, c.Action, c.Fingerprint,
		c.TaskId, c.StackId, c.StackVersion, c.ConfigHash, c.CreatedAt,
		c.Finalized, c.FinalError, c.RecordRemoved}
}

// composeOperationFromEntity 把 entity 行转成 ComposeOperation。
func composeOperationFromEntity(e entity.ComposeOperationRequests) ComposeOperation {
	return ComposeOperation{
		AgentID:       e.AgentId,
		RequestID:     e.RequestId,
		Name:          e.Name,
		Action:        e.Action,
		Fingerprint:   e.Fingerprint,
		TaskID:        e.TaskId,
		StackID:       e.StackId,
		StackVersion:  int64(e.StackVersion),
		ConfigHash:    e.ConfigHash,
		CreatedAt:     unixToTime(int64(e.CreatedAt)),
		Finalized:     e.Finalized != 0,
		FinalError:    e.FinalError,
		RecordRemoved: e.RecordRemoved != 0,
	}
}

func mustComposeOperation(ctx context.Context, m *gdb.Model, agentID, requestID string) (ComposeOperation, error) {
	c := dao.ComposeOperationRequests.Columns()
	// 不调 Fields()：事务内会触发元数据查询并死锁，见 record.go 的说明。
	var e entity.ComposeOperationRequests
	err := scanOne(ctx, m.
		Where(c.AgentId, agentID).
		Where(c.RequestId, requestID), &e)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ComposeOperation{}, ErrNotFound
		}
		return ComposeOperation{}, gerror.Wrap(err, "查询编排操作失败")
	}
	return composeOperationFromEntity(e), nil
}

// mustComposeStack 在某个事务里按 (agent_id, name) 取编排，查不到返回 ErrNotFound。
func mustComposeStack(ctx context.Context, tx gdb.TX, agentID, name string) (ComposeStack, error) {
	c := dao.ComposeStacks.Columns()
	// 不调 Fields()：事务内会触发元数据查询并死锁，见 record.go 的说明。
	var e entity.ComposeStacks
	err := scanOne(ctx, tx.Model(dao.ComposeStacks.Table()).
		Where(c.AgentId, agentID).
		Where(c.Name, name), &e)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ComposeStack{}, ErrNotFound
		}
		return ComposeStack{}, gerror.Wrap(err, "查询编排失败")
	}
	return composeStackFromEntity(e), nil
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

	err = d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		// --- 幂等键已存在：只校验一致性，绝不重复保存 ---
		existing, e := mustComposeOperation(ctx, tx.Model(dao.ComposeOperationRequests.Table()), req.AgentID, req.RequestID)
		if e == nil {
			duplicate, op = true, existing
			if op.Name != req.Name || op.Action != req.Action || op.Fingerprint != req.Fingerprint {
				return ErrConflict
			}
			if op.TaskID != "" || op.Finalized {
				return nil
			}
			stack, e = mustComposeStack(ctx, tx, req.AgentID, req.Name)
			if e != nil {
				return ErrConflict
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

		// --- 新请求：先做有界清理，再判容量 ---
		now := time.Now().UTC()
		oc := dao.ComposeOperationRequests.Columns()
		if _, e = tx.Model(dao.ComposeOperationRequests.Table()).Ctx(ctx).
			Where(oc.Finalized, 1).
			WhereLT(oc.CreatedAt, now.Add(-24*time.Hour).Unix()).
			Delete(); e != nil {
			return e
		}
		count, e := tx.Model(dao.ComposeOperationRequests.Table()).Ctx(ctx).Count()
		if e != nil {
			return e
		}
		if count >= maxComposeOperations {
			return gerror.Wrap(ErrConflict, "编排操作记录已满")
		}

		stack, e = mustComposeStack(ctx, tx, req.AgentID, req.Name)

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
			stack = ComposeStack{
				ID: req.StackID, AgentID: req.AgentID, Name: req.Name,
				Content: *req.SaveContent, ConfigHash: req.ConfigHash,
				Version: 1, CreatedAt: now, UpdatedAt: now,
			}
			if req.SaveEnv != nil {
				stack.EnvContent = *req.SaveEnv
			}
			sc := dao.ComposeStacks.Columns()
			if _, e = tx.Model(dao.ComposeStacks.Table()).Ctx(ctx).Data(do.ComposeStacks{
				Id:         stack.ID,
				AgentId:    stack.AgentID,
				Name:       stack.Name,
				Content:    stack.Content,
				EnvContent: stack.EnvContent,
				ConfigHash: stack.ConfigHash,
				Version:    stack.Version,
				CreatedAt:  timeToUnix(now),
				UpdatedAt:  timeToUnix(now),
				SyncedAt:   0,
			}).Insert(); e != nil {
				return normalizeExecError(e, "创建编排")
			}
			_ = sc
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
				// RETURNING 与写入必须同一条语句，见 composeStackViaReturning 的说明。
				// 事务内要用 tx 上的 GetOne，保证读到的是本事务未提交的写入。
				updated, uerr := composeStackViaReturningTx(ctx, tx, stack, req.ConfigHash, now, req.ExpectedVersion)
				if errors.Is(uerr, ErrNotFound) {
					return ErrConflict
				}
				if uerr != nil {
					return uerr
				}
				stack = updated
			}
		}

		op = ComposeOperation{
			AgentID: req.AgentID, RequestID: req.RequestID, Name: req.Name,
			Action: req.Action, Fingerprint: req.Fingerprint, TaskID: "",
			StackID: stack.ID, StackVersion: stack.Version,
			ConfigHash: stack.ConfigHash, CreatedAt: now,
		}
		_, e = tx.Model(dao.ComposeOperationRequests.Table()).Ctx(ctx).Data(do.ComposeOperationRequests{
			AgentId:       op.AgentID,
			RequestId:     op.RequestID,
			Name:          op.Name,
			Action:        op.Action,
			Fingerprint:   op.Fingerprint,
			TaskId:        "",
			StackId:       op.StackID,
			StackVersion:  op.StackVersion,
			ConfigHash:    op.ConfigHash,
			CreatedAt:     timeToUnix(now),
			Finalized:     0,
			FinalError:    "",
			RecordRemoved: 0,
		}).Insert()
		return normalizeExecError(e, "保存编排操作")
	})
	if err != nil {
		stack = ComposeStack{}
	}
	return
}

// composeStackViaReturningTx 在事务内执行带 RETURNING 的乐观锁 UPDATE。
//
// RETURNING 让"版本匹配 → 自增 → 读回新值"在一条语句里完成；
// 拆成 Update + Select 会在两条语句之间插入并发更新，
// 读回的 version 可能已失效。并发安全来自 WHERE version = ? 的原子匹配。
func composeStackViaReturningTx(ctx context.Context, tx gdb.TX, s ComposeStack, configHash string, now time.Time, expectedVersion int64) (ComposeStack, error) {
	c := dao.ComposeStacks.Columns()
	q := `UPDATE ` + dao.ComposeStacks.Table() + `
	      SET ` + c.Content + ` = ?, ` + c.EnvContent + ` = ?, ` + c.ConfigHash + ` = ?,
	          ` + c.Version + ` = ` + c.Version + ` + 1, ` + c.UpdatedAt + ` = ?
	      WHERE ` + c.Id + ` = ? AND ` + c.AgentId + ` = ? AND ` + c.Name + ` = ? AND ` + c.Version + ` = ?
	      RETURNING ` + strings.Join(composeStackFields(), ", ")
	one, err := tx.GetOne(q, s.Content, s.EnvContent, configHash, timeToUnix(now),
		s.ID, s.AgentID, s.Name, expectedVersion)
	if err != nil {
		return ComposeStack{}, gerror.Wrap(err, "更新编排失败")
	}
	if len(one) == 0 {
		return ComposeStack{}, ErrNotFound
	}
	return composeStackFromRecord(one)
}

func operationMatchesStack(op ComposeOperation, s ComposeStack) bool {
	return op.StackID == s.ID && op.StackVersion == s.Version && op.ConfigHash == s.ConfigHash
}

// AttachOperationTask binds a task exactly once. Reattaching the same ID is safe.
func (d *DB) AttachOperationTask(ctx context.Context, agentID, requestID, taskID string) error {
	if taskID == "" {
		return ErrConflict
	}
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		op, err := mustComposeOperation(ctx, tx.Model(dao.ComposeOperationRequests.Table()), agentID, requestID)
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
		c := dao.ComposeOperationRequests.Columns()
		n, err := tx.Model(dao.ComposeOperationRequests.Table()).Ctx(ctx).
			Where(c.AgentId, agentID).
			Where(c.TaskId, taskID).
			Count()
		if err != nil {
			return err
		}
		if n != 0 {
			return ErrConflict
		}
		_, err = tx.Model(dao.ComposeOperationRequests.Table()).Ctx(ctx).
			Where(c.AgentId, agentID).
			Where(c.RequestId, requestID).
			Data(do.ComposeOperationRequests{TaskId: taskID}).
			Update()
		return err
	})
}

// FindOperationByTask 按 (agent, 项目名, task) 找操作记录。
func (d *DB) FindOperationByTask(ctx context.Context, agentID, name, taskID string) (ComposeOperation, error) {
	if taskID == "" {
		return ComposeOperation{}, ErrNotFound
	}
	c := dao.ComposeOperationRequests.Columns()
	var e entity.ComposeOperationRequests
	err := scanOne(ctx, d.gdb.Model(dao.ComposeOperationRequests.Table()).Fields(composeOperationFields()).
		Where(c.AgentId, agentID).
		Where(c.Name, name).
		Where(c.TaskId, taskID), &e)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ComposeOperation{}, ErrNotFound
		}
		return ComposeOperation{}, gerror.Wrap(err, "查询编排操作失败")
	}
	return composeOperationFromEntity(e), nil
}

// FinalizeComposeOperation reloads trusted metadata and applies a conditional
// up/remove effect once. A stale successful task is finalized but never marks
// a newer stack synced or deletes a replacement. Failures retain the stack.
func (d *DB) FinalizeComposeOperation(ctx context.Context, metadata ComposeOperation, succeeded bool) (op ComposeOperation, err error) {
	err = d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		var e error
		op, e = mustComposeOperation(ctx, tx.Model(dao.ComposeOperationRequests.Table()), metadata.AgentID, metadata.RequestID)
		if e != nil {
			return e
		}
		if metadata.Name != op.Name || metadata.Action != op.Action ||
			metadata.Fingerprint != op.Fingerprint || metadata.TaskID != op.TaskID ||
			metadata.StackID != op.StackID || metadata.StackVersion != op.StackVersion ||
			metadata.ConfigHash != op.ConfigHash {
			return ErrConflict
		}
		if op.Finalized {
			return nil
		}

		if !succeeded {
			op.FinalError = "compose operation failed"
		} else {
			// 条件更新：只有 stack 仍停在被这次操作绑定的版本上才生效。
			// 用 id + version + config_hash 三重条件挡住"期间被人改过"的情况。
			sc := dao.ComposeStacks.Columns()
			var result sql.Result
			switch op.Action {
			case "up":
				r, uerr := tx.Model(dao.ComposeStacks.Table()).Ctx(ctx).
					Where(sc.AgentId, op.AgentID).
					Where(sc.Name, op.Name).
					Where(sc.Id, op.StackID).
					Where(sc.Version, op.StackVersion).
					Where(sc.ConfigHash, op.ConfigHash).
					Data(do.ComposeStacks{SyncedAt: time.Now().UTC().Unix()}).
					Update()
				result, e = r, uerr
			case "remove":
				r, derr := tx.Model(dao.ComposeStacks.Table()).Ctx(ctx).
					Where(sc.AgentId, op.AgentID).
					Where(sc.Name, op.Name).
					Where(sc.Id, op.StackID).
					Where(sc.Version, op.StackVersion).
					Where(sc.ConfigHash, op.ConfigHash).
					Delete()
				result, e = r, derr
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
		c := dao.ComposeOperationRequests.Columns()
		_, e = tx.Model(dao.ComposeOperationRequests.Table()).Ctx(ctx).
			Where(c.AgentId, op.AgentID).
			Where(c.RequestId, op.RequestID).
			Data(do.ComposeOperationRequests{
				Finalized:     1,
				FinalError:    op.FinalError,
				RecordRemoved: boolToInt(op.RecordRemoved),
			}).
			Update()
		return e
	})
	return
}
