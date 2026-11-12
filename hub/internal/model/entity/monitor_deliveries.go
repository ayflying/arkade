// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package entity

// MonitorDeliveries is the golang structure for table monitor_deliveries.
type MonitorDeliveries struct {
	Id         string `json:"id"         orm:"id"          ` //
	IncidentId string `json:"incidentId" orm:"incident_id" ` //
	Channel    string `json:"channel"    orm:"channel"     ` //
	Event      string `json:"event"      orm:"event"       ` //
	Status     string `json:"status"     orm:"status"      ` //
	Error      string `json:"error"      orm:"error"       ` //
	CreatedAt  int64  `json:"createdAt"  orm:"created_at"  ` //
}
