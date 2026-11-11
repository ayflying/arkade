package store

import (
	"context"
	"errors"

	"github.com/gogf/gf/v2/errors/gerror"

	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/entity"
)

// FindOperationByRequest允许重试先确认已受理的任务，避免项目已删除时误判为新请求。
func (d *DB) FindOperationByRequest(ctx context.Context, agentID, requestID string) (ComposeOperation, error) {
	c := dao.ComposeOperationRequests.Columns()
	var e entity.ComposeOperationRequests
	err := scanOne(ctx, d.gdb.Model(dao.ComposeOperationRequests.Table()).Fields(composeOperationFields()).
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
