package account

import (
	api "arkade/hub/api/account/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/store"
	"context"
	"log/slog"
)

// Registration is bound separately to the public group. Never expose the private
// profile/password controller through the public registration binding.
type Registration struct{ account *Controller }

func NewRegistration(db *store.DB, log *slog.Logger) *Registration {
	return &Registration{account: New(db, nil, log)}
}
func (c *Registration) Register(ctx context.Context, _ *api.RegisterReq) (*api.RegisterRes, error) {
	var p api.RegisterInput
	if err := decode(ctx, &p); err != nil {
		return nil, err
	}
	u, err := c.account.service.Register(ctx, p.Username, p.DisplayName, p.Password)
	if err != nil {
		return nil, c.account.failure(err)
	}
	controller.Status(ctx, 201)
	return &api.RegisterRes{User: view(u)}, nil
}
