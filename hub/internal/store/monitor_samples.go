package store

// monitor_samples.go 是容器资源历史的仓储层：1 分钟桶的写入、降采样查询与清理。
//
// ============================ 为什么按分钟建桶 ============================
//
// 采集间隔可配置（10–3600 秒）。若按采集次数原样落库，7 天行数会随间隔浮动，
// 10 秒间隔下每容器每周就是 6 万行。分钟桶把上界固定为每容器每天 1440 行，
// 同时保留"今天"视图需要的分钟级细节；查询再按时间跨度二次降采样。
//
// ============================ 只存白名单 ============================
//
// 行里只有资源指标和已经算好的速率，没有 inspect、环境变量、标签原文。
// project 用于说明样本属于哪个已接管的编排项目，空值样本不写入（见 monitor 包）。

import (
	"context"
	"database/sql"
	"math"
	"strconv"
	"strings"

	"github.com/gogf/gf/v2/errors/gerror"

	"arkade/hub/internal/dao"
)

const (
	// MonitorSampleBucketSeconds 是历史样本的桶宽，也是写入去重的时间粒度。
	MonitorSampleBucketSeconds = 60
	// MonitorSampleRetentionDays 是历史保留天数，也是查询允许的最大跨度。
	MonitorSampleRetentionDays = 7
	// MaxMonitorHistoryPoints 是单次查询返回的点数上限；超过时由调用方加大桶宽。
	MaxMonitorHistoryPoints = 360
	// monitorSampleWriteChunk 限制单条 INSERT 的行数，避免参数个数逼近驱动上限。
	monitorSampleWriteChunk = 200
	// monitorSampleColumnCount 与 monitorSampleColumns 的列数保持一致。
	monitorSampleColumnCount = 20
)

// MonitorSample 是一条待写入的分钟样本。BucketAt 必须是桶起点（Unix 秒）。
type MonitorSample struct {
	AgentID          string
	ContainerID      string
	Project          string
	ContainerName    string
	BucketAt         int64
	LastAt           int64
	State            string
	StatsAvailable   bool
	CPUPercent       float64
	MemoryBytes      int64
	MemoryLimitBytes int64
	MemoryPercent    float64
	NetworkAvailable bool
	NetworkRXRate    float64
	NetworkTXRate    float64
	BlockAvailable   bool
	BlockReadRate    float64
	BlockWriteRate   float64
	RatesAvailable   bool
	SnapshotComplete bool
}

// MonitorSamplePoint 是查询返回的一个降采样点。
// 指针字段为 nil 表示该桶内没有可用样本，前端必须断开曲线而不是画成 0。
type MonitorSamplePoint struct {
	At                  int64    `json:"at"`
	Samples             int64    `json:"samples"`
	StatsAvailable      bool     `json:"stats_available"`
	CPUPercent          *float64 `json:"cpu_percent,omitempty"`
	MemoryBytes         *int64   `json:"memory_bytes,omitempty"`
	MemoryLimitBytes    *int64   `json:"memory_limit_bytes,omitempty"`
	MemoryPercent       *float64 `json:"memory_percent,omitempty"`
	NetworkAvailable    bool     `json:"network_available"`
	RatesAvailable      bool     `json:"rates_available"`
	NetworkRXPerSecond  *float64 `json:"network_rx_per_second,omitempty"`
	NetworkTXPerSecond  *float64 `json:"network_tx_per_second,omitempty"`
	BlockReadPerSecond  *float64 `json:"block_read_per_second,omitempty"`
	BlockWritePerSecond *float64 `json:"block_write_per_second,omitempty"`
}

const monitorSampleColumns = `agent_id,container_id,project,container_name,bucket_at,last_at,state,` +
	`stats_available,cpu_percent,memory_bytes,memory_limit_bytes,memory_percent,` +
	`network_available,network_rx_per_second,network_tx_per_second,` +
	`block_available,block_read_per_second,block_write_per_second,rates_available,snapshot_complete`

// 同桶重复采集覆盖为最后一次：采集更密时保留最新值，采集更稀时留下稀疏的点。
const monitorSampleConflictSet = `project = excluded.project, container_name = excluded.container_name, ` +
	`last_at = excluded.last_at, state = excluded.state, stats_available = excluded.stats_available, ` +
	`cpu_percent = excluded.cpu_percent, memory_bytes = excluded.memory_bytes, ` +
	`memory_limit_bytes = excluded.memory_limit_bytes, memory_percent = excluded.memory_percent, ` +
	`network_available = excluded.network_available, network_rx_per_second = excluded.network_rx_per_second, ` +
	`network_tx_per_second = excluded.network_tx_per_second, block_available = excluded.block_available, ` +
	`block_read_per_second = excluded.block_read_per_second, block_write_per_second = excluded.block_write_per_second, ` +
	`rates_available = excluded.rates_available, snapshot_complete = excluded.snapshot_complete`

// UpsertMonitorSamples 批量写入分钟样本，同主键覆盖。空输入是合法调用。
//
// 写入前先按主键去重：PostgreSQL 不允许一条 INSERT ... ON CONFLICT 里有两行
// 命中同一个主键（会直接报 "cannot affect row a second time"），一旦发生就是
// 整批失败、这一分钟的数据全丢。去重保留后出现的那条，与"同桶取最后一次"一致。
func (d *DB) UpsertMonitorSamples(ctx context.Context, samples []MonitorSample) error {
	samples = dedupeMonitorSamples(samples)
	for start := 0; start < len(samples); start += monitorSampleWriteChunk {
		end := start + monitorSampleWriteChunk
		if end > len(samples) {
			end = len(samples)
		}
		if err := d.upsertMonitorSamplesChunk(ctx, samples[start:end]); err != nil {
			return err
		}
	}
	return nil
}

// dedupeMonitorSamples 按 (agent_id, container_id, bucket_at) 去重，后写覆盖先写。
func dedupeMonitorSamples(samples []MonitorSample) []MonitorSample {
	type key struct {
		agent, container string
		bucket           int64
	}
	seen := make(map[key]int, len(samples))
	out := make([]MonitorSample, 0, len(samples))
	for _, s := range samples {
		k := key{agent: s.AgentID, container: s.ContainerID, bucket: s.BucketAt}
		if i, ok := seen[k]; ok {
			out[i] = s
			continue
		}
		seen[k] = len(out)
		out = append(out, s)
	}
	return out
}

func (d *DB) upsertMonitorSamplesChunk(ctx context.Context, samples []MonitorSample) error {
	args := make([]any, 0, len(samples)*monitorSampleColumnCount)
	var values strings.Builder
	for i, s := range samples {
		if i > 0 {
			values.WriteByte(',')
		}
		values.WriteByte('(')
		for n := 0; n < monitorSampleColumnCount; n++ {
			if n > 0 {
				values.WriteByte(',')
			}
			values.WriteByte('$')
			values.WriteString(strconv.Itoa(i*monitorSampleColumnCount + n + 1))
		}
		values.WriteByte(')')
		args = append(args,
			s.AgentID, s.ContainerID, s.Project, s.ContainerName, s.BucketAt, s.LastAt, s.State,
			s.StatsAvailable, finiteOrZero(s.CPUPercent), s.MemoryBytes, s.MemoryLimitBytes, finiteOrZero(s.MemoryPercent),
			s.NetworkAvailable, finiteOrZero(s.NetworkRXRate), finiteOrZero(s.NetworkTXRate),
			s.BlockAvailable, finiteOrZero(s.BlockReadRate), finiteOrZero(s.BlockWriteRate),
			s.RatesAvailable, s.SnapshotComplete,
		)
	}
	query := `INSERT INTO monitor_samples (` + monitorSampleColumns + `) VALUES ` + values.String() +
		` ON CONFLICT (agent_id, container_id, bucket_at) DO UPDATE SET ` + monitorSampleConflictSet
	if _, err := d.gdb.DoExec(ctx, nil, query, args...); err != nil {
		return gerror.Wrap(err, "写入容器资源历史失败")
	}
	return nil
}

// finiteOrZero 防止 NaN/Inf 进入数据库：PostgreSQL 的 double precision 接受
// 'NaN'，但前端图表拿到后整条曲线会消失，表现为"有数据却画不出来"。
func finiteOrZero(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// QueryMonitorSamples 返回 [from, to] 内的降采样序列，按时间升序。
//
// 桶宽由调用方决定，服务端用 (bucket_at / bucket) * bucket 在 SQL 里聚合，
// 保证返回行数不超过跨度/桶宽；不要改成全量取回再在 Go 里聚合，7 天请求
// 会变成一万行传输。
func (d *DB) QueryMonitorSamples(ctx context.Context, agentID, containerID string, from, to, bucket int64) ([]MonitorSamplePoint, error) {
	if bucket < MonitorSampleBucketSeconds {
		bucket = MonitorSampleBucketSeconds
	}
	if to < from {
		return nil, nil
	}
	const query = `SELECT (bucket_at / $5) * $5 AS point_at, count(*) AS samples,
       bool_or(stats_available) AS stats_available,
       avg(cpu_percent) FILTER (WHERE stats_available) AS cpu_percent,
       avg(memory_bytes) FILTER (WHERE stats_available) AS memory_bytes,
       max(memory_limit_bytes) FILTER (WHERE stats_available) AS memory_limit_bytes,
       avg(memory_percent) FILTER (WHERE stats_available) AS memory_percent,
       bool_or(network_available) AS network_available,
       bool_or(rates_available) AS rates_available,
       avg(network_rx_per_second) FILTER (WHERE rates_available) AS network_rx_per_second,
       avg(network_tx_per_second) FILTER (WHERE rates_available) AS network_tx_per_second,
       avg(block_read_per_second) FILTER (WHERE rates_available) AS block_read_per_second,
       avg(block_write_per_second) FILTER (WHERE rates_available) AS block_write_per_second
FROM monitor_samples
WHERE agent_id = $1 AND container_id = $2 AND bucket_at >= $3 AND bucket_at <= $4
GROUP BY 1
ORDER BY 1`
	// 这里用底层 *sql.DB 而不是 Record：聚合列可能是 NULL（该桶没有可用样本），
	// 必须能区分"没有数据"和"值为 0"，Scan 到 sql.Null* 是最直接的方式。
	raw, err := d.gdb.Master()
	if err != nil {
		return nil, gerror.Wrap(err, "取得数据库连接失败")
	}
	rows, err := raw.QueryContext(ctx, query, agentID, containerID, from, to, bucket)
	if err != nil {
		return nil, gerror.Wrap(err, "读取容器资源历史失败")
	}
	defer rows.Close()
	out := make([]MonitorSamplePoint, 0, MaxMonitorHistoryPoints)
	for rows.Next() {
		var (
			point                                  MonitorSamplePoint
			cpu, memBytes, memLimit, memPercent    sql.NullFloat64
			rxRate, txRate, readRate, writeRate    sql.NullFloat64
		)
		if err = rows.Scan(&point.At, &point.Samples, &point.StatsAvailable,
			&cpu, &memBytes, &memLimit, &memPercent,
			&point.NetworkAvailable, &point.RatesAvailable,
			&rxRate, &txRate, &readRate, &writeRate); err != nil {
			return nil, gerror.Wrap(err, "解析容器资源历史失败")
		}
		point.CPUPercent = nullableFloat(cpu)
		point.MemoryPercent = nullableFloat(memPercent)
		point.MemoryBytes = nullableInt(memBytes)
		point.MemoryLimitBytes = nullableInt(memLimit)
		point.NetworkRXPerSecond = nullableFloat(rxRate)
		point.NetworkTXPerSecond = nullableFloat(txRate)
		point.BlockReadPerSecond = nullableFloat(readRate)
		point.BlockWritePerSecond = nullableFloat(writeRate)
		out = append(out, point)
	}
	if err = rows.Err(); err != nil {
		return nil, gerror.Wrap(err, "读取容器资源历史失败")
	}
	return out, nil
}

func nullableFloat(v sql.NullFloat64) *float64 {
	if !v.Valid || math.IsNaN(v.Float64) || math.IsInf(v.Float64, 0) {
		return nil
	}
	out := v.Float64
	return &out
}

func nullableInt(v sql.NullFloat64) *int64 {
	if !v.Valid || math.IsNaN(v.Float64) || math.IsInf(v.Float64, 0) {
		return nil
	}
	out := int64(math.Round(v.Float64))
	return &out
}

// PruneMonitorSamples 删除 before 之前的样本。before 非正时不执行任何删除。
func (d *DB) PruneMonitorSamples(ctx context.Context, before int64) error {
	if before <= 0 {
		return nil
	}
	c := dao.MonitorSamples.Columns()
	if _, err := d.gdb.Model(dao.MonitorSamples.Table()).Ctx(ctx).WhereLT(c.BucketAt, before).Delete(); err != nil {
		return gerror.Wrap(err, "清理容器资源历史失败")
	}
	return nil
}

// 删除对象时的清理见 monitor_purge.go：一个对象的残留数据不止样本表，
// 告警事件与投递记录同样需要一起清掉。
