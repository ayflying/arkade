// Package monitor 提供后台可配置的监控与通知，不执行容器自愈操作。
package monitor

import "github.com/gogf/gf/v2/errors/gerror"

type Config struct {
	Enabled            bool     `json:"enabled"`
	IntervalSeconds    int      `json:"interval_seconds"`
	DurationSeconds    int      `json:"duration_seconds"`
	RepeatSeconds      int      `json:"repeat_seconds"`
	OfflineSeconds     int      `json:"offline_seconds"`
	CPUPercent         float64  `json:"cpu_percent"`
	MemoryPercent      float64  `json:"memory_percent"`
	RecoveryNotify     bool     `json:"recovery_notify"`
	ExcludedAgents     []string `json:"excluded_agents"`
	ExcludedContainers []string `json:"excluded_containers"`
	DingTalkEnabled    bool     `json:"dingtalk_enabled"`
	EmailEnabled       bool     `json:"email_enabled"`
}

func DefaultConfig() Config {
	return Config{IntervalSeconds: 30, DurationSeconds: 120, RepeatSeconds: 1800, OfflineSeconds: 120, CPUPercent: 80, MemoryPercent: 80, RecoveryNotify: true, ExcludedAgents: []string{}, ExcludedContainers: []string{}}
}
func ValidateConfig(c Config) error {
	if c.IntervalSeconds < 10 || c.IntervalSeconds > 3600 {
		return gerror.New("采集间隔必须为10到3600秒")
	}
	if c.DurationSeconds < 0 || c.DurationSeconds > 86400 || c.OfflineSeconds < 30 || c.OfflineSeconds > 86400 {
		return gerror.New("异常持续时间需为0到86400秒，失联时间需为30到86400秒")
	}
	if c.RepeatSeconds < 60 || c.RepeatSeconds > 604800 {
		return gerror.New("重复通知间隔必须为60秒到7天")
	}
	if c.CPUPercent <= 0 || c.CPUPercent > 12800 || c.MemoryPercent <= 0 || c.MemoryPercent > 100 {
		return gerror.New("CPU阈值必须为0到12800之间，内存阈值必须为0到100之间")
	}
	if len(c.ExcludedAgents) > 2000 || len(c.ExcludedContainers) > 2000 {
		return gerror.New("排除列表不能超过2000项")
	}
	for _, list := range [][]string{c.ExcludedAgents, c.ExcludedContainers} {
		for _, v := range list {
			if len(v) == 0 || len(v) > 256 {
				return gerror.New("排除对象必须为1到256字节的身份ID或容器名")
			}
		}
	}
	return nil
}
