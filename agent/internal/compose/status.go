package compose

// status.go 探测编排项目的实时状态。
//
// 状态来源是 `docker compose ps --format json`，而不是自己算：
// compose 知道某个服务的容器是否真的在跑、是否健康、是否在重启。
// 用 ps 输出的权威结果归并，比在 Agent 里复刻一遍判定逻辑可靠得多。
//
// 状态归并规则（与协议注释一致）：
//   - running：所有服务都处于 running
//   - partial：部分服务在跑
//   - stopped：一个都没在跑（但项目存在）
//   - unknown：查不到（Agent 没装 compose、命令失败、项目从没启动过）

import (
	"strings"
)

// 状态常量，与协议 envelope.ComposeProjectSummary.Status 取值一致。
const (
	StatusRunning = "running"
	StatusPartial = "partial"
	StatusStopped = "stopped"
	StatusUnknown = "unknown"
)

// psEntry 是 `docker compose ps --format json` 输出的单个元素。
//
// 注意：compose 在不同版本里字段名有差异——有的输出 State（"running"），
// 有的只给 Status（"Up 3 minutes (healthy)"）。两个都接，根据可用字段判定。
type psEntry struct {
	Name    string `json:"Name"`
	Service string `json:"Service"`
	State   string `json:"State"`
	Status  string `json:"Status"`
	Health  string `json:"Health"`
}

// parsePSJson 解析 compose ps 的 JSON 输出。
//
// 兼容两种形态：
//   - JSON 数组：[{...},{...}]
//   - 每行一个 JSON 对象（compose v2 在部分版本里的 --format json 行为）
//
// 返回的条目按服务名去重合并——一个服务如果有多个副本（replicas>1），
// 任意一个在跑就算这个服务在跑。
func parsePSJson(raw string) []psEntry {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	var entries []psEntry
	if strings.HasPrefix(raw, "[") {
		entries = parseJSONArray(raw)
	} else {
		entries = parseJSONLines(raw)
	}
	return entries
}

// serviceStats 统计"哪些服务在跑"。
//
// services 是配置里声明的服务名（可能为空——那时只按 ps 结果统计）。
// 返回 (运行中的服务数, 总服务数)。
//
// 关键设计：**以配置里的服务列表为准**。原因：容器没启动时 compose ps 可能什么都不返回，
// 若只看 ps 结果，总服务数会变成 0，界面显示"0/0"，用户无法判断是没启动还是真没服务。
func serviceStats(services []string, entries []psEntry) (running, total int) {
	// 收集处于运行态的服务名。
	runningSet := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		name := e.Service
		if name == "" {
			name = e.Name
		}
		if name == "" {
			continue
		}
		if isRunningEntry(e) {
			runningSet[name] = struct{}{}
		}
	}

	if len(services) == 0 {
		// 配置解析失败时退回按 ps 结果统计：至少能反映实际情况。
		return len(runningSet), len(entries)
	}

	total = len(services)
	for _, s := range services {
		if _, ok := runningSet[s]; ok {
			running++
		}
	}
	return running, total
}

// isRunningEntry 判断一个 ps 条目是否代表"服务在运行"。
//
// 判定顺序：先看 State（结构化字段，最可靠），再回退到 Status 文本前缀。
// 只认 "running"：paused / restarting / created 都不算在跑——
// 它们对用户来说都不是"服务正常"，混进来会给出虚假的健康感。
func isRunningEntry(e psEntry) bool {
	if s := strings.ToLower(strings.TrimSpace(e.State)); s != "" {
		return s == "running"
	}
	// Status 形如 "Up 3 minutes (healthy)" / "Exited (0) 2 minutes ago"。
	s := strings.ToLower(strings.TrimSpace(e.Status))
	return strings.HasPrefix(s, "up")
}

// aggregateStatus 把运行数与总数归并成整体状态。
//
// 边界：total==0 说明既没解析出服务、ps 也没返回任何东西——项目可能从没启动，
// 报 unknown 比报 stopped 更诚实（stopped 暗示"曾经跑过、现在停了"）。
func aggregateStatus(running, total int) string {
	switch {
	case total == 0:
		return StatusUnknown
	case running == 0:
		return StatusStopped
	case running >= total:
		return StatusRunning
	default:
		return StatusPartial
	}
}
