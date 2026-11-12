package monitor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
)

func ruleKey(agent, container, kind string) string { return agent + "\x00" + container + "\x00" + kind }
func fingerprint(key string) string                { b := sha256.Sum256([]byte(key)); return hex.EncodeToString(b[:]) }
func (e *Engine) evaluateContainer(ctx context.Context, c Config, agent string, m envelope.ContainerMetric, ts int64) error {
	key := metricKey(agent, m.ID)
	prior, exists := e.containers[key]
	if !exists && len(e.containers) >= 10000 {
		return nil
	}
	state := strings.ToLower(m.State)
	running := state == "running" || state == "restarting"
	restarting := state == "restarting"
	if exists && m.RestartCount > prior.Restarts {
		prior.LastRestart = ts
		restarting = true
	}
	if restarting {
		prior.LastRestart = ts
	}
	// Keep the anomaly continuous during a restart storm, but allow a stable
	// container to recover after one complete duration without another restart.
	if prior.LastRestart > 0 && ts-prior.LastRestart < durationSeconds(c.DurationSeconds) {
		restarting = true
	}
	_, knownExit := e.states[ruleKey(agent, m.ID, "unexpected_exit")]
	unexpected := (prior.Running || knownExit) && (state == "exited" || state == "dead") && (m.ExitCode != 0 || state == "dead")
	if running {
		prior.Running = true
	}
	prior.Restarts = m.RestartCount
	prior.Seen = ts
	e.containers[key] = prior
	checks := []struct {
		kind           string
		bad, available bool
		detail         string
	}{
		{"unhealthy", strings.EqualFold(m.Health, "unhealthy"), m.Health != "" || !running, "容器健康状态：" + m.Health},
		{"restart", restarting, true, fmt.Sprintf("容器重启次数：%d", m.RestartCount)},
		{"unexpected_exit", unexpected, true, fmt.Sprintf("容器状态：%s；退出码：%d", state, m.ExitCode)},
		{"cpu", c.CPUPercent > 0 && m.CPUPercent >= c.CPUPercent, m.StatsAvailable && finite(m.CPUPercent) && c.CPUPercent > 0, fmt.Sprintf("CPU 使用率 %.2f%%（阈值 %.2f%%）", m.CPUPercent, c.CPUPercent)},
		{"memory", c.MemoryPercent > 0 && m.MemoryPercent >= c.MemoryPercent, m.StatsAvailable && finite(m.MemoryPercent) && m.MemoryLimitBytes > 0 && c.MemoryPercent > 0, fmt.Sprintf("内存使用率 %.2f%%（阈值 %.2f%%）", m.MemoryPercent, c.MemoryPercent)},
		{"container_collection", running && !m.StatsAvailable, true, "容器资源采集不可用"},
	}
	for _, check := range checks {
		if err := e.evaluate(ctx, c, agent, m.ID, check.kind, check.bad, check.available, check.detail, ts, false); err != nil {
			return err
		}
	}
	return nil
}

// evaluate only treats an explicit available good sample as recovery. Unknown
// samples pause pending timers and leave firing incidents intact.
func (e *Engine) evaluate(ctx context.Context, c Config, agent, container, kind string, bad, available bool, detail string, ts int64, immediate bool) error {
	key := ruleKey(agent, container, kind)
	s := e.states[key]
	if !available {
		if s != nil && s.Incident.Status == "pending" {
			s.Incident.FirstSeen = ts
			s.LastObserved = 0
			return e.save(ctx, s)
		}
		return nil
	}
	if bad {
		if s == nil {
			if len(e.states) >= 10000 {
				return nil
			}
			s = &incidentState{Key: key, Incident: store.MonitorIncident{ID: fingerprint(key), Fingerprint: fingerprint(key), AgentID: agent, ContainerID: container, Kind: kind, Status: "pending", FirstSeen: ts}}
			s.initMaps()
			e.states[key] = s
		}
		if s.Recovering {
			s.Recovering = false
			s.PendingEvent = ""
			s.PendingAt = 0
		}
		if s.LastObserved == 0 && s.Incident.Status == "pending" {
			s.Incident.FirstSeen = ts
		}
		s.LastObserved = ts
		s.Detail = detail
		s.Incident.LastSeen = ts
		if s.Incident.Status == "pending" && (immediate || ts-s.Incident.FirstSeen >= durationSeconds(c.DurationSeconds)) {
			s.Incident.Status = "firing"
		}
		if err := e.save(ctx, s); err != nil {
			return err
		}
		if s.Incident.Status == "firing" {
			e.deliveryEligible[key] = true
		}
		return nil
	}
	if s == nil {
		return nil
	}
	if s.Incident.Status == "pending" {
		return e.closeSilently(ctx, key, ts)
	}
	s.Detail = detail
	s.LastObserved = ts
	s.Incident.LastSeen = ts
	if !s.Recovering {
		s.Recovering = true
		s.PendingEvent = ""
		s.PendingAt = 0
		s.RetryAt = map[string]int64{}
		s.Failures = map[string]int{}
	}
	if err := e.save(ctx, s); err != nil {
		return err
	}
	e.deliveryEligible[key] = true
	return nil
}
func enabledChannels(c Config) []string {
	var out []string
	if c.DingTalkEnabled {
		out = append(out, "dingtalk")
	}
	if c.EmailEnabled {
		out = append(out, "email")
	}
	return out
}
func (e *Engine) dispatch(ctx, notifyCtx context.Context, c Config, s *incidentState, ts int64) error {
	channels := enabledChannels(c)
	if s.Recovering && !c.RecoveryNotify {
		return e.closeSilently(ctx, s.Key, ts)
	}
	if s.PendingEvent == "" {
		switch {
		case s.Recovering:
			s.PendingEvent = "resolved"
		case s.Incident.LastNotified == 0:
			s.PendingEvent = "firing"
		case ts-s.Incident.LastNotified >= seconds(c.RepeatSeconds, 1800):
			s.PendingEvent = "repeat"
		default:
			return nil
		}
		s.PendingAt = ts
	}
	// Persist delivery intent before calling external providers.
	if err := e.save(ctx, s); err != nil {
		return err
	}
	token := fmt.Sprintf("%s:%d", s.PendingEvent, s.PendingAt)
	pending := false
	for _, channel := range channels {
		if s.Recovering && !s.Sent[channel] {
			continue
		}
		if s.Delivered[channel] == token {
			continue
		}
		if ts < s.RetryAt[channel] {
			pending = true
			continue
		}
		if e.notify == nil {
			pending = true
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if notifyCtx.Err() != nil {
			pending = true
			continue
		}
		event := Event{Incident: s.Incident, Channel: channel, Event: s.PendingEvent}
		deliveryCtx, cancel := context.WithTimeout(notifyCtx, 5*time.Second)
		err := e.notify(deliveryCtx, event)
		cancel()
		delivery := store.MonitorDelivery{ID: identifier(), IncidentID: s.Incident.ID, Channel: channel, Event: s.PendingEvent, Status: "sent", CreatedAt: ts}
		if err != nil {
			delivery.Status = "failed"
			delivery.Error = "通知发送失败"
			s.Failures[channel]++
			shift := s.Failures[channel] - 1
			if shift > 6 {
				shift = 6
			}
			delay := int64(30) << shift
			if delay > 1800 {
				delay = 1800
			}
			s.RetryAt[channel] = ts + delay
			pending = true
		} else {
			s.Delivered[channel] = token
			delete(s.RetryAt, channel)
			delete(s.Failures, channel)
			if !s.Recovering {
				s.Sent[channel] = true
				s.Incident.LastNotified = ts
			}
		}
		// Persist the success marker first to prevent a delivery-log failure from
		// causing immediate duplicate sends. External sends are necessarily at-least-once.
		if saveErr := e.save(ctx, s); saveErr != nil {
			return saveErr
		}
		if e.db != nil {
			if saveErr := e.db.AddMonitorDelivery(ctx, delivery); saveErr != nil {
				return saveErr
			}
		}
	}
	if s.Recovering && !pending {
		return e.closeSilently(ctx, s.Key, ts)
	}
	if !s.Recovering && len(channels) == 0 {
		return nil
	}
	if !pending {
		s.PendingEvent = ""
		s.PendingAt = 0
		return e.save(ctx, s)
	}
	return nil
}
