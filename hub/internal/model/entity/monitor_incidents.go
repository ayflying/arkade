// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package entity

// MonitorIncidents is the golang structure for table monitor_incidents.
type MonitorIncidents struct {
	Id           string `json:"id"           orm:"id"            ` //
	Fingerprint  string `json:"fingerprint"  orm:"fingerprint"   ` //
	AgentId      string `json:"agentId"      orm:"agent_id"      ` //
	ContainerId  string `json:"containerId"  orm:"container_id"  ` //
	Kind         string `json:"kind"         orm:"kind"          ` //
	Status       string `json:"status"       orm:"status"        ` //
	FirstSeen    int64  `json:"firstSeen"    orm:"first_seen"    ` //
	LastSeen     int64  `json:"lastSeen"     orm:"last_seen"     ` //
	LastNotified int64  `json:"lastNotified" orm:"last_notified" ` //
	ResolvedAt   int64  `json:"resolvedAt"   orm:"resolved_at"   ` //
	DetailJson   string `json:"detailJson"   orm:"detail_json"   ` //
}
