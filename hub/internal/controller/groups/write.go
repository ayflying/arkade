package groups

import (
	"context"
	"time"

	api "arkade/hub/api/groups/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/httpwire"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
)

func (c *Controller) Create(ctx context.Context, req *api.CreateReq) (*api.CreateRes, error) {
	if decoded, ok := httpwire.DecodedBody[api.CreateReq](ctx); ok {
		body := *decoded
		req = &body
	}
	operator, err := c.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	name := user.NormalizeGroupName(req.Name)
	if err := user.ValidateGroupName(name); err != nil {
		return nil, badRequest("%s", err.Error())
	}
	if err := user.ValidateDescription(req.Description); err != nil {
		return nil, badRequest("%s", err.Error())
	}
	grp := user.Group{ID: "g-" + randomSuffix(), Name: name, Description: req.Description, CreatedAt: time.Now().UTC()}
	if err := c.db.CreateGroup(ctx, grp); err != nil {
		return nil, c.storeError(err, "创建用户组失败")
	}
	c.log.Info("管理员创建了用户组", "operator", operator.Username, "group", grp.Name)
	return &api.CreateRes{Group: groupView(store.GroupRecord{
		ID: grp.ID, Name: grp.Name, Description: grp.Description, CreatedAt: grp.CreatedAt,
	})}, nil
}

func (c *Controller) Update(ctx context.Context, req *api.UpdateReq) (*api.UpdateRes, error) {
	// GF skips explicit null fields; use the guard's strict typed JSON decode
	// for PATCH presence while retaining only the framework-bound path identity.
	if decoded, ok := httpwire.DecodedBody[api.UpdateReq](ctx); ok {
		copy := *decoded
		copy.GroupID = req.GroupID
		req = &copy
	}
	if _, err := c.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if !req.Name.Set && !req.Description.Set {
		return nil, badRequest("没有需要修改的字段")
	}
	upd := store.GroupUpdate{}
	if req.Name.Set {
		name := user.NormalizeGroupName(req.Name.Value)
		if err := user.ValidateGroupName(name); err != nil {
			return nil, badRequest("%s", err.Error())
		}
		upd.Name = &name
	}
	if req.Description.Set {
		if err := user.ValidateDescription(req.Description.Value); err != nil {
			return nil, badRequest("%s", err.Error())
		}
		upd.Description = &req.Description.Value
	}
	// Existence must be checked independently of RowsAffected for unchanged values.
	if _, err := c.db.GetGroup(ctx, req.GroupID); err != nil {
		return nil, c.storeError(err, "用户组不存在")
	}
	if err := c.db.UpdateGroup(ctx, req.GroupID, upd); err != nil {
		return nil, c.storeError(err, "用户组不存在")
	}
	grp, err := c.db.GetGroup(ctx, req.GroupID)
	if err != nil {
		return nil, c.storeError(err, "用户组不存在")
	}
	members, err := c.db.ListGroupMembers(ctx, req.GroupID)
	if err != nil {
		return nil, c.storeError(err, "查询组成员失败")
	}
	grants, err := c.db.CountGroupGrants(ctx, req.GroupID)
	if err != nil {
		return nil, c.storeError(err, "统计组授权失败")
	}
	return &api.UpdateRes{Group: groupView(store.GroupRecord{
		ID: grp.ID, Name: grp.Name, Description: grp.Description, CreatedAt: grp.CreatedAt,
		MemberCount: len(members), GrantCount: grants,
	})}, nil
}

func (c *Controller) Delete(ctx context.Context, req *api.DeleteReq) (*api.DeleteRes, error) {
	operator, err := c.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	grp, err := c.db.GetGroup(ctx, req.GroupID)
	if err != nil {
		return nil, c.storeError(err, "用户组不存在")
	}
	if err := c.db.DeleteGroup(ctx, req.GroupID); err != nil {
		return nil, c.storeError(err, "用户组不存在")
	}
	c.log.Info("管理员删除了用户组", "operator", operator.Username, "group", grp.Name)
	return nil, controller.ErrNoContent
}
