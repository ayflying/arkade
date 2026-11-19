package runtime

// helper_reaper.go：清理**自己遗留**的 helper 容器。
//
// ============================ 为什么必须有它 ============================
//
// helper 容器的正常生命周期是"创建 → 用 → 按自己的 ID 删除"，
// 删除写在 runHelper / RunVolumeFileHelper 的 defer 里。
// 但 defer 只覆盖"进程还活着、API 还连得上"这一种情况：
//
//   - Agent 进程在处理中途被重启/被杀（升级、OOM、宿主机维护），
//     defer 根本没机会执行；
//   - 引擎短暂不可用或卡死，删除请求在 20 秒内失败，错误被 _ = 丢弃；
//   - create 成功但注入失败，而删除又恰好赶上引擎故障。
//
// 上面任何一种情况留下的容器都**不会再有人来收**：
// AutoRemove=false（为了失败时能读日志），也没有任何按标签的批量回收。
// 结果是它们永久留在容器列表里，越积越多（现场曾看到 52 个）。
//
// 因此这里补一条**保守的自我清理**：只认本程序创建的标签与名字前缀，
// 只删"已经不可能还在被使用"的容器，并且任何存疑一律跳过。

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	// helperStaleAfter 是"认定残留"的最小年龄。
	//
	// 为什么必须远大于 helperTimeout（60s）：一次 helper 从创建到删除
	// 全程受 helperTimeout 约束，正常情况下最迟 60 秒后就不存在了。
	// 取 10 分钟留足余量——引擎卡顿、宿主 IO 抖动时总时长会被拉长，
	// 若阈值太接近 60 秒，就可能把**正在运行**的 helper 当残留删掉，
	// 那会把一次正常请求变成随机失败。
	helperStaleAfter = 10 * time.Minute

	// helperReapInterval 是后台巡检周期。
	//
	// 15 分钟远大于 helperStaleAfter 的一半：残留本身就是低频事件，
	// 巡检只是兜底，频率再高也只是徒增引擎调用。
	helperReapInterval = 15 * time.Minute

	// helperReapFirstDelay 是启动后第一次巡检的延迟。
	//
	// 不设为 0：Agent 启动时往往同时在跑心跳、编排状态探测等，
	// 立刻再压一轮容器列表只会让启动更慢。残留多等一分钟没有代价。
	helperReapFirstDelay = time.Minute

	// helperNamePrefixes 是**只有本程序会生成**的容器名前缀。
	//
	// 这是第二道判据（第一道是 io.arkade.role 标签）。两个条件必须同时满足：
	// 标签只说明"某个 Arkade 组件创建的"，名字前缀才说明"helper"。
	// 之所以要双判据：tag 允许被 -l 手工带进任意容器，
	// 单靠标签删除等于给了外部一个"让 Arkade 删我容器"的开关。
	volumeHelperNamePrefix  = "arkade-vfh-"
	composeHelperNamePrefix = "arkade-helper-"

	// helperRoleValue 与 helperLabel 配对，取值见两处创建点。
	helperRoleVolume  = "volume-file-helper"
	helperRoleCompose = "helper"
)

// IsHelperContainer 判断一个容器是否是本程序的一次性 helper（标签 + 名字前缀双判据）。
//
// 导出给两处使用，判据只在这里维护：
//   - 上面的回收逻辑（它能删谁）；
//   - handler 的 container.list（要不要把它显示给用户）。
//
// 为什么需要"不显示"：这些容器生命周期只有几秒到几十秒，
// 唯一价值是失败时留日志。把它们混进容器列表会让"这台机器上有
// 52 个容器"这种读起来像故障的数字变成常态，用户也无法对它们做任何有效操作
// （不能进、不能重启，删了还只是把正在进行的一次配置读取打坏）。
// 不显示不等于不管理——回收见下面，删除入口仍然保留在容器详情上。
func IsHelperContainer(c Container) bool {
	if !strings.HasPrefix(c.Name, volumeHelperNamePrefix) &&
		!strings.HasPrefix(c.Name, composeHelperNamePrefix) {
		return false
	}
	switch c.Labels[helperLabel] {
	case helperRoleVolume, helperRoleCompose:
		return true
	default:
		return false
	}
}

// helperIsOwned 是 IsHelperContainer 的包内别名，保留它以让回收代码读起来更贴身。
func helperIsOwned(c Container) bool { return IsHelperContainer(c) }

// helperIsReapable 判断一个自己的 helper 容器是否已经"不可能还在用"。
//
// 三条判据按代价从低到高排列，全部满足才回收：
//
//  1. **必须是已停止的**（created / exited / dead）。
//     running / restarting 一律跳过：它可能是一次**正在进行**的请求，
//     删掉就等于把别人的操作打断。宁可漏收，不可误删。
//  2. **必须超过 helperStaleAfter**。即使已停止，刚停下的容器也可能是
//     "helper 进程跑完了但 Agent 还没来得及删"的正常窗口。
//  3. **创建时间必须已知**。CreatedUnix==0 表示运行时没给这个字段
//     （老版本或兼容实现），此时无从判断年龄，跳过。
func helperIsReapable(c Container, now time.Time, staleAfter time.Duration) bool {
	if !helperIsOwned(c) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(c.State)) {
	case "created", "exited", "dead":
	default:
		return false
	}
	if c.CreatedUnix <= 0 {
		return false
	}
	created := time.Unix(c.CreatedUnix, 0)
	// 容忍轻微的时钟漂移：宿主时间若比 Agent 慢，年龄会是负数。
	if created.After(now) {
		return false
	}
	return now.Sub(created) >= staleAfter
}

// ReapStaleHelpers 回收本程序遗留的 helper 容器，返回实际删除的数量。
//
// 它是**幂等且可重复调用**的，不存在"删过了还会再删"的问题：
// 每轮都重新列一次容器，已删除的自然不在列表里。
//
// 这里不用并发槽位（helperSem）：回收是删除操作，不创建容器、不占内存，
// 与文件操作抢配额只会让正常请求更容易撞上 ErrHelperBusy。
func (e *engine) ReapStaleHelpers(ctx context.Context) (int, error) {
	// all=true：残留的绝大多数是已停止的容器，只看运行中的等于没扫。
	list, err := e.List(ctx, true)
	if err != nil {
		return 0, fmt.Errorf("列出容器失败: %w", err)
	}
	now := time.Now()
	removed := 0
	var firstErr error
	for _, c := range list {
		if !helperIsReapable(c, now, helperStaleAfter) {
			continue
		}
		// 单次删除用独立超时：一个容器删不动不应该拖住整轮回收。
		// force=false：上面已经保证它不在运行，不需要 force 的语义。
		// removeVolumes=false：helper 只挂具名卷（不是匿名卷），
		// 这里的 v 不可能删掉业务数据，但仍然显式关掉，避免语义漂移。
		rmCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := e.RemoveContainer(rmCtx, c.ID, false, false)
		cancel()
		if err != nil {
			// 只记第一条错误用于上报，其余容器继续处理：
			// 一轮遇到一个删不掉的（例如被外部占用）不该让其它残留继续留着。
			if firstErr == nil {
				firstErr = fmt.Errorf("删除残留 helper %s 失败: %w", c.Name, err)
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}

// RunHelperReaper 后台周期回收，直到 ctx 取消。
//
// 与 RunSweeper 同样的形状（ticker + ctx 退出），便于阅读与测试。
// 任何一轮失败都只记录不中断：回收失败不是致命错误，
// 下次巡检会重试（容器仍会在列表里）。
func (e *engine) RunHelperReaper(ctx context.Context, report func(removed int, err error)) {
	if report == nil {
		report = func(int, error) {}
	}
	timer := time.NewTimer(helperReapFirstDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	// 启动后先跑一轮：升级重启正是残留最集中的时刻。
	report(e.ReapStaleHelpers(ctx))

	ticker := time.NewTicker(helperReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			report(e.ReapStaleHelpers(ctx))
		}
	}
}
