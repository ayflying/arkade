package store

import (
	"context"
	"math"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/errors/gerror"

	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/do"
	"arkade/hub/internal/model/entity"
)

func (d *DB) GetMonitorConfig(ctx context.Context) (MonitorConfig, error) {
	var e entity.MonitorConfig
	err := scanOne(ctx, d.gdb.Model(dao.MonitorConfig.Table()).Where(dao.MonitorConfig.Columns().Id, 1), &e)
	return MonitorConfig{ConfigJSON: e.ConfigJson, SecretsCiphertext: e.SecretsCiphertext, Version: e.Version}, err
}

// UpdateMonitorConfig atomically compares expected and advances the version.
// value.Version is informational; only expected is used for concurrency control.
func (d *DB) UpdateMonitorConfig(ctx context.Context, value MonitorConfig, expected int64) error {
	if len(value.ConfigJSON) > MaxMonitorConfigBytes || len(value.SecretsCiphertext) > MaxMonitorSecretsBytes {
		return gerror.New("monitor configuration payload exceeds size limit")
	}
	if expected < 1 || expected == math.MaxInt64 {
		return ErrConflict
	}
	c := dao.MonitorConfig.Columns()
	result, err := d.gdb.Model(dao.MonitorConfig.Table()).Ctx(ctx).Where(c.Id, 1).Where(c.Version, expected).
		Data(do.MonitorConfig{ConfigJson: value.ConfigJSON, SecretsCiphertext: value.SecretsCiphertext, Version: expected + 1}).Update()
	if err != nil {
		return normalizeExecError(err, "更新监控配置")
	}
	n, err := result.RowsAffected()
	if err != nil {
		return gerror.Wrap(err, "确认监控配置更新失败")
	}
	if n == 0 {
		return ErrConflict
	}
	return nil
}

func monitorIncidentFrom(e entity.MonitorIncidents) MonitorIncident {
	return MonitorIncident{ID: e.Id, Fingerprint: e.Fingerprint, AgentID: e.AgentId, ContainerID: e.ContainerId, Kind: e.Kind, Status: e.Status, FirstSeen: e.FirstSeen, LastSeen: e.LastSeen, LastNotified: e.LastNotified, ResolvedAt: e.ResolvedAt, DetailJSON: e.DetailJson}
}

// UpsertMonitorIncident retains one row and its original ID for a stable
// fingerprint. The single engine worker owns state transitions and timestamps,
// including resetting FirstSeen on a new episode of the same fingerprint.
func (d *DB) UpsertMonitorIncident(ctx context.Context, value MonitorIncident) error {
	if value.ID == "" || value.Fingerprint == "" {
		return gerror.New("monitor incident ID and fingerprint are required")
	}
	if len(value.DetailJSON) > MaxMonitorDetailBytes {
		return gerror.New("monitor incident detail exceeds size limit")
	}
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		c := dao.MonitorIncidents.Columns()
		data := do.MonitorIncidents{AgentId: value.AgentID, ContainerId: value.ContainerID, Kind: value.Kind, Status: value.Status, FirstSeen: value.FirstSeen, LastSeen: value.LastSeen, LastNotified: value.LastNotified, ResolvedAt: value.ResolvedAt, DetailJson: value.DetailJSON}
		n, err := tx.Model(dao.MonitorIncidents.Table()).Ctx(ctx).Where(c.Fingerprint, value.Fingerprint).Count()
		if err != nil {
			return gerror.Wrap(err, "查询监控事件失败")
		}
		if n == 0 {
			data.Id = value.ID
			data.Fingerprint = value.Fingerprint
			_, err = tx.Model(dao.MonitorIncidents.Table()).Ctx(ctx).Data(data).Insert()
		} else {
			_, err = tx.Model(dao.MonitorIncidents.Table()).Ctx(ctx).Where(c.Fingerprint, value.Fingerprint).Data(data).Update()
		}
		return normalizeExecError(err, "保存监控事件")
	})
}

func monitorHistoryLimit(limit int) int {
	if limit <= 0 || limit > 200 {
		return 200
	}
	return limit
}

func (d *DB) listMonitorIncidents(ctx context.Context, m *gdb.Model) ([]MonitorIncident, error) {
	var rows []entity.MonitorIncidents
	if err := m.Ctx(ctx).Scan(&rows); err != nil {
		return nil, gerror.Wrap(err, "读取监控事件失败")
	}
	out := make([]MonitorIncident, 0, len(rows))
	for _, e := range rows {
		out = append(out, monitorIncidentFrom(e))
	}
	return out, nil
}

// ListMonitorIncidents returns newest activity first, capped at 200 rows.
func (d *DB) ListMonitorIncidents(ctx context.Context, limit int) ([]MonitorIncident, error) {
	c := dao.MonitorIncidents.Columns()
	return d.listMonitorIncidents(ctx, d.gdb.Model(dao.MonitorIncidents.Table()).OrderDesc(c.LastSeen).OrderDesc(c.Id).Limit(monitorHistoryLimit(limit)))
}

// ListOpenMonitorIncidents restores all unfinished state, never truncating it to
// the public history limit. Status vocabulary is owned by the engine.
func (d *DB) ListOpenMonitorIncidents(ctx context.Context) ([]MonitorIncident, error) {
	c := dao.MonitorIncidents.Columns()
	return d.listMonitorIncidents(ctx, d.gdb.Model(dao.MonitorIncidents.Table()).WhereNotIn(c.Status, []string{"resolved", "closed"}).OrderDesc(c.LastSeen).OrderDesc(c.Id))
}

func (d *DB) AddMonitorDelivery(ctx context.Context, value MonitorDelivery) error {
	if value.ID == "" {
		return gerror.New("monitor delivery ID is required")
	}
	if len(value.Error) > MaxMonitorErrorBytes {
		return gerror.New("monitor delivery error exceeds size limit")
	}
	// CreatedAt is explicit engine event time, not ORM insertion time.
	_, err := d.gdb.Model(dao.MonitorDeliveries.Table()).Ctx(ctx).Data(do.MonitorDeliveries{Id: value.ID, IncidentId: value.IncidentID, Channel: value.Channel, Event: value.Event, Status: value.Status, Error: value.Error, CreatedAt: value.CreatedAt}).Insert()
	return normalizeExecError(err, "保存监控投递记录")
}

func (d *DB) ListMonitorDeliveries(ctx context.Context, limit int) ([]MonitorDelivery, error) {
	c := dao.MonitorDeliveries.Columns()
	var rows []entity.MonitorDeliveries
	err := d.gdb.Model(dao.MonitorDeliveries.Table()).Ctx(ctx).OrderDesc(c.CreatedAt).OrderDesc(c.Id).Limit(monitorHistoryLimit(limit)).Scan(&rows)
	if err != nil {
		return nil, gerror.Wrap(err, "读取监控投递记录失败")
	}
	out := make([]MonitorDelivery, 0, len(rows))
	for _, e := range rows {
		out = append(out, MonitorDelivery{ID: e.Id, IncidentID: e.IncidentId, Channel: e.Channel, Event: e.Event, Status: e.Status, Error: e.Error, CreatedAt: e.CreatedAt})
	}
	return out, nil
}
