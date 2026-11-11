package resources

import (
	"context"
	"strconv"

	api "arkade/hub/api/resources/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/user"
)

// Logs is the existing bounded JSON transport, not an invented follow stream.
func (c *Controller) ContainerLogs(ctx context.Context, req *api.ContainerLogsReq) (*api.ContainerLogsRes, error) {
	if err := c.access.Require(ctx, req.AgentID, user.PermView); err != nil {
		return nil, err
	}
	tail, err := parseTail(req.Tail)
	if err != nil {
		return nil, controller.Failure("bad_request", "tail 必须是 1 到 2000 之间的整数")
	}
	out, cerr := c.deps.Ops.Logs(ctx, req.AgentID, req.ContainerID, tail)
	if cerr != nil {
		return nil, controller.ConnectionError(cerr)
	}
	return &api.ContainerLogsRes{Lines: out.Lines, Truncated: out.Truncated}, nil
}
func parseTail(raw string) (int, error) {
	if raw == "" {
		return 200, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if n < 1 || n > 2000 {
		return 0, strconv.ErrRange
	}
	return n, nil
}
