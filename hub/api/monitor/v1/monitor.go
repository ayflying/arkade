package v1

import (
	"arkade/hub/internal/monitor"
	"arkade/hub/internal/store"
	"github.com/gogf/gf/v2/frame/g"
)

type ConfigReq struct {
	g.Meta `path:"/api/monitor/config" method:"get"`
}
type ConfigRes = monitor.ConfigView
type UpdateReq struct {
	g.Meta `path:"/api/monitor/config" method:"put"`
}
type UpdateRes = monitor.ConfigView
type TestReq struct {
	g.Meta `path:"/api/monitor/test" method:"post"`
}
type TestRes struct {
	OK bool `json:"ok"`
}
type StatusReq struct {
	g.Meta `path:"/api/monitor/status" method:"get"`
}
type StatusRes struct {
	Metrics    []monitor.Metric        `json:"metrics"`
	Incidents  []store.MonitorIncident `json:"incidents"`
	Deliveries []store.MonitorDelivery `json:"deliveries"`
}
