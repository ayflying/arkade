package monitor

import (
	api "arkade/hub/api/monitor/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/httpwire"
	service "arkade/hub/internal/monitor"
	"arkade/hub/internal/store"
	"context"
	"errors"
	"github.com/gogf/gf/v2/net/ghttp"
	"sync"
	"time"
)

type Controller struct {
	service  *service.Service
	engine   *service.Engine
	db       *store.DB
	access   controller.Access
	mu       sync.Mutex
	nextTest time.Time
}

func New(s *service.Service, e *service.Engine, db *store.DB, access controller.Access) *Controller {
	return &Controller{service: s, engine: e, db: db, access: access}
}
func (c *Controller) admin(ctx context.Context) error {
	_, err := c.access.Admin(ctx)
	if r := ghttp.RequestFromCtx(ctx); r != nil {
		r.Response.Header().Set("Cache-Control", "no-store")
	}
	return err
}
func mapped(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrConflict) {
		return controller.Failure("conflict", "监控配置已被修改，请刷新后重试")
	}
	return controller.Failure("bad_request", "监控配置或通道校验失败，请核对参数、密钥和通道网络")
}
func (c *Controller) Config(ctx context.Context, _ *api.ConfigReq) (*api.ConfigRes, error) {
	if err := c.admin(ctx); err != nil {
		return nil, err
	}
	value, err := c.service.Get(ctx)
	if err != nil {
		return nil, controller.ErrInternal
	}
	return &value, nil
}
func (c *Controller) Update(ctx context.Context, _ *api.UpdateReq) (*api.UpdateRes, error) {
	if err := c.admin(ctx); err != nil {
		return nil, err
	}
	var input service.Update
	if httpwire.DecodeJSON(httpwire.RestoreRawBody(ctx), &input) != nil {
		return nil, controller.Failure("bad_request", "监控配置请求无效")
	}
	value, err := c.service.Update(ctx, input)
	if err != nil {
		return nil, mapped(err)
	}
	return &value, nil
}
func (c *Controller) Test(ctx context.Context, _ *api.TestReq) (*api.TestRes, error) {
	if err := c.admin(ctx); err != nil {
		return nil, err
	}
	var input struct {
		Channel string `json:"channel"`
	}
	if httpwire.DecodeJSON(httpwire.RestoreRawBody(ctx), &input) != nil || (input.Channel != "dingtalk" && input.Channel != "email") {
		return nil, controller.Failure("bad_request", "请选择钉钉或邮件通道")
	}
	// 测试发送亦可能产生外部消息，限速且不接受任意临时目标。
	c.mu.Lock()
	if time.Now().Before(c.nextTest) {
		c.mu.Unlock()
		return nil, controller.Failure("bad_request", "测试发送过于频繁，请稍后重试")
	}
	c.nextTest = time.Now().Add(10 * time.Second)
	c.mu.Unlock()
	if err := c.service.Test(ctx, input.Channel); err != nil {
		return nil, controller.Failure("bad_request", "测试发送失败，请检查已保存的通道配置、凭据、TLS及网络")
	}
	return &api.TestRes{OK: true}, nil
}
func (c *Controller) Status(ctx context.Context, _ *api.StatusReq) (*api.StatusRes, error) {
	if err := c.admin(ctx); err != nil {
		return nil, err
	}
	incidents, err := c.db.ListMonitorIncidents(ctx, 200)
	if err != nil {
		return nil, controller.ErrInternal
	}
	deliveries, err := c.db.ListMonitorDeliveries(ctx, 200)
	if err != nil {
		return nil, controller.ErrInternal
	}
	return &api.StatusRes{Metrics: c.engine.Metrics(), Incidents: incidents, Deliveries: deliveries}, nil
}
