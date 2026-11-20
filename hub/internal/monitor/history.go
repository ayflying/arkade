package monitor

// history.go 把每轮采集结果写进容器资源历史。
//
// 它和告警是两条独立的路径：告警关闭时仍然记录历史，采集失败时只留下空洞，
// 不补点、不插值。写入失败只记日志，不影响本轮告警判定——否则一次数据库抖动
// 会把"没采到"升级成"采集失败"告警。

import (
	"context"
	"time"

	"arkade/hub/internal/store"
)

// historyWriteTimeout 是单轮历史写入的预算。采集轮本身有 25 秒上限，
// 这里单独限时，避免慢数据库把整个轮询拖到超时。
const historyWriteTimeout = 5 * time.Second

// persistHistory 按分钟桶写入本轮采纳的样本。
//
// 只写归属明确的样本：project 为空说明采集方没有报告 compose 项目（旧 Agent，
// 或容器不属于任何编排），无法证明它属于已接管的项目，宁可不写也不冒充。
func (e *Engine) persistHistory(ctx context.Context, values []Metric, now time.Time) {
	if e.db == nil || len(values) == 0 {
		return
	}
	bucket := now.Unix() - now.Unix()%store.MonitorSampleBucketSeconds
	rows := make([]store.MonitorSample, 0, len(values))
	for _, v := range values {
		if v.Project == "" {
			continue
		}
		rows = append(rows, store.MonitorSample{
			AgentID:          v.AgentID,
			ContainerID:      v.ContainerID,
			Project:          v.Project,
			ContainerName:    v.Name,
			BucketAt:         bucket,
			LastAt:           v.ReceivedAt,
			State:            v.State,
			StatsAvailable:   v.StatsAvailable,
			CPUPercent:       v.CPUPercent,
			MemoryBytes:      v.MemoryBytes,
			MemoryLimitBytes: v.MemoryLimitBytes,
			MemoryPercent:    v.MemoryPercent,
			NetworkAvailable: v.NetworkAvailable != nil && *v.NetworkAvailable,
			NetworkRXRate:    v.NetworkRXPerSecond,
			NetworkTXRate:    v.NetworkTXPerSecond,
			BlockAvailable:   v.BlockAvailable != nil && *v.BlockAvailable,
			BlockReadRate:    v.BlockReadPerSecond,
			BlockWriteRate:   v.BlockWritePerSecond,
			RatesAvailable:   v.RatesAvailable,
			SnapshotComplete: v.SnapshotComplete,
		})
	}
	if len(rows) == 0 {
		return
	}
	run, cancel := context.WithTimeout(ctx, historyWriteTimeout)
	defer cancel()
	if err := e.db.UpsertMonitorSamples(run, rows); err != nil {
		e.log.Warn("容器资源历史写入失败", "containers", len(rows))
	}
}
