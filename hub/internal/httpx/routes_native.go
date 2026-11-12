package httpx

import (
	"arkade/hub/internal/controller"
	agentscontroller "arkade/hub/internal/controller/agents"
	externalcontroller "arkade/hub/internal/controller/external"
	imagescontroller "arkade/hub/internal/controller/imagetasks"
	monitorcontroller "arkade/hub/internal/controller/monitor"
	resourcescontroller "arkade/hub/internal/controller/resources"
	stackscontroller "arkade/hub/internal/controller/stacks"
	"context"
	"github.com/gogf/gf/v2/net/ghttp"
	"time"
)

func bindOperationalRoutes(s *ghttp.Server, d Deps) {
	session := newSessionAccessor()
	if d.MonitorService != nil && d.MonitorEngine != nil {
		bindNativeModule(s, d, monitorcontroller.New(d.MonitorService, d.MonitorEngine, d.DB, controller.Access{DB: d.DB, Session: session, Log: d.Log}))
	}
	bindNativeModule(s, d, agentscontroller.New(agentscontroller.Deps{DB: d.DB, Registry: d.Registry, Guard: d.Guard, SharedToken: d.AgentSharedToken, Session: session, Log: d.Log}))
	resourcesDeps := resourcescontroller.Deps{DB: d.DB, Ops: d.Ops, Session: session, Log: d.Log}
	if d.MonitorEngine != nil {
		resourcesDeps.SuppressMonitor = d.MonitorEngine.Suppress
	}
	bindNativeModule(s, d, resourcescontroller.New(resourcesDeps))
	bindNativeModule(s, d, imagescontroller.New(imagescontroller.Deps{DB: d.DB, Registry: d.Registry, Tasks: d.ImageTasks, Session: session, Log: d.Log}))
	bindNativeModule(s, d, externalcontroller.New(externalcontroller.Deps{DB: d.DB, Ops: d.Ops, Session: session, Log: d.Log, AutoAdoptState: func(agent, name string) (string, string) {
		if d.AutoAdopt == nil {
			return "pending", "等待后台扫描，兼容项目将自动登记配置；容器仍可直接管理"
		}
		state := d.AutoAdopt.state(agent, name)
		return state.Status, state.Reason
	}}))
	var suppressMonitor func(string, string, time.Duration)
	if d.MonitorEngine != nil {
		suppressMonitor = d.MonitorEngine.Suppress
	}
	stacks := stackscontroller.New(stackscontroller.Deps{SuppressMonitor: suppressMonitor, DB: d.DB, Ops: d.Ops, Session: session, Log: d.Log, Migrate: d.StackMigrate, AutoState: func(agent, name string) (string, string, bool) {
		if d.AutoAdopt == nil {
			return "pending", "等待后台扫描，兼容项目将自动登记配置；容器仍可直接管理", false
		}
		state, ok := d.AutoAdopt.recordedState(agent, name)
		if !ok {
			state = d.AutoAdopt.state(agent, name)
		}
		return state.Status, state.Reason, ok
	}})
	if d.RegisterBackgroundClose != nil {
		d.RegisterBackgroundClose(func(ctx context.Context) error { return stackscontroller.Close(ctx, stacks) })
	}
	bindNativeModule(s, d, stacks)
}
