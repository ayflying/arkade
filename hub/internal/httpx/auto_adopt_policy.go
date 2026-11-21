package httpx

// auto_adopt_policy.go：决定"这个外部项目这一轮到底要不要再扫"。
//
// ============================ 为什么需要它 ============================
//
// 自动发现读一次配置，在 Agent 侧就要创建一个一次性 helper 容器
// （注入约 11MiB 的 Agent 二进制 → 启动 → 读结果 → 删除）。
// 因此"多久读一次"直接等于"多久建一次容器"。
//
// 改造前的行为是：只要项目还没接管成功，**每 30 秒重读一次，永不停止**。
// 一台机器上有十几个读不到配置的外部项目时，就变成持续的创建/删除循环，
// 现场表现是容器列表里堆积几十个 arkade-helper-*，并伴随持续的磁盘写入。
//
// 这里的判据把失败分成两类，分别对应两种完全不同的处理：
//
//	确定性阻塞（blocked）：同样的输入必然同样失败——多配置文件、工作目录
//	  不在白名单、配置是符号链接、标签不一致…… 重读一次与重读一千次
//	  结果相同，唯一的代价是每次多建一个容器。→ **不再自动重扫**
//	  （只能靠用户手动重试，或外部项目被删除后重新出现）。
//
//	环境性失败（retry）：引擎连不上、helper 基础镜像缺失（dependency_missing）、
//	  并发繁忙、超时…… 这类会自愈，值得重试，但要用指数退避，且必须有次数上限。
//
// ============================ 两类结论的边界（界面依赖它） ============================
//
// 同一份结论既用于"要不要再扫"，也用于"界面上怎么解释"。因此必须保持：
//
//	skipped ⟺ 这个**项目自身**的确定性结论（用户可能想去改配置）；
//	failed  ⟺ **整台服务器**的环境问题（用户在这一行里做不了任何事）。
//
// 界面据此只在 skipped 时展示原因。把环境问题落成 skipped，会让列表每一行
// 都挂一条用户无法处理的告警——曾经就把"服务器没拉 alpine:3.20"显示成
// 每个未登记项目的故障，而用户根本没打算接管这些项目。
//
// 两者都不能"30 秒硬刷"。成功接管的项目不在这里判断——那由
// compose_stacks.synced_at 表达，扫到就跳过。

import (
	"strings"
	"time"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
)

const (
	// autoAdoptRetryBase 是第一次环境性失败后的等待时间。
	//
	// 取 1 分钟：比原来的 30 秒略长，但真正的粒度由后面的指数增长决定。
	autoAdoptRetryBase = time.Minute
	// autoAdoptRetryMax 是退避上限，避免无限增长后看起来"再也不扫了"。
	autoAdoptRetryMax = 30 * time.Minute
	// autoAdoptMaxAttempts 是环境性失败的最大自动重试次数。
	//
	// 超过之后把结论落成 skipped（不再自动重扫），让用户手动处理：
	// 若确实是长期故障（例如服务器上根本没有 helper 镜像），
	// 继续每 30 分钟建一次容器也只是白烧磁盘，用户仍能在界面上看到原因。
	autoAdoptMaxAttempts = 5
)

// autoAdoptStatusPending 是"从没扫过"的初始状态。
const autoAdoptStatusPending = "pending"

// autoAdoptRetryDelay 返回第 attempts 次失败后应等待的时长。
//
// 指数退避：1min → 2min → 4min → 8min → 16min → 30min（上限）。
// attempts<=0 时按第一次失败处理，防御性处理避免除零式的怪异结果。
func autoAdoptRetryDelay(attempts int64) time.Duration {
	if attempts <= 1 {
		return autoAdoptRetryBase
	}
	delay := autoAdoptRetryBase
	for i := int64(1); i < attempts; i++ {
		delay *= 2
		if delay > autoAdoptRetryMax {
			// 用 > 而不是 >=：恰好等于上限时保持该值本身，
			// 否则"封顶值"永远不会被实际使用。
			return autoAdoptRetryMax
		}
	}
	return delay
}

// autoAdoptShouldScan 判断某个项目这一轮是否应该再去读配置。
//
// 返回值语义：
//   - true：可以扫（从没扫过、或已到退避时间）；
//   - false：跳过（确定性阻塞、重试次数用尽、退避时间未到）。
//
// 已成功接管的判断不在这里：它由调用方用 synced_at 先判断掉，
// 因为那需要 compose_stacks 的行，而这里是纯函数，便于单测覆盖。
func autoAdoptShouldScan(prev store.AutoAdoptState, has bool, now time.Time) bool {
	if !has {
		// 从没扫过：必须扫，否则新项目永远不会被接管。
		return true
	}
	switch prev.Status {
	case store.AutoAdoptStatusSkipped:
		// 确定性阻塞：不再自动重扫。
		// 想再来一次只有两条路——用户手动重试，或项目消失后重新出现
		// （清理逻辑会删掉它的状态行）。
		return false
	case store.AutoAdoptStatusFailed:
		if prev.Attempts >= autoAdoptMaxAttempts {
			// 次数用尽：不再自动重扫。这里刻意不再检查时间，
			// 避免"看起来到了时间又扫一次"的隐式恢复。
			return false
		}
		// 到点才扫；NextCheckAt 为零值（异常数据）时按可以扫处理，
		// 宁可多扫一次也不要把一个项目永久卡死。
		if prev.NextCheckAt.IsZero() {
			return true
		}
		return !now.Before(prev.NextCheckAt)
	default:
		// 未知状态（例如历史遗留或将来新增）：不要卡死，允许扫。
		return true
	}
}

// autoAdoptRetryable 判断一次读取失败是否属于"环境性失败"。
//
// 只有会自动消失的原因才值得退避重试；其余（无权限、项目已不存在、
// 版本不支持）重试多少次都一样，应当按确定性阻塞处理，不再重扫。
func autoAdoptRetryable(cerr *agentlink.ConnError) bool {
	if cerr == nil {
		return false
	}
	switch cerr.Code {
	case envelope.CodeAgentBusy, envelope.CodeTimeout, envelope.CodeAgentOffline,
		envelope.CodeRuntimeFailed, envelope.CodeInternal,
		// dependency_missing：本机缺 helper 基础镜像。这是**用户拉一下就能好**的
		// 环境问题，不属于"重试不会变好"——以前它被归进 unsupported，
		// 结果用户按提示拉完镜像，后台却已经永久停扫，只能再去详情页手动重试。
		envelope.CodeDependencyMissing:
		// 忙、超时、Agent 掉线、运行时/内部故障、缺本地依赖：都可能自愈。
		return true
	default:
		// unsupported（引擎/版本做不到）、not_found（项目已消失）、
		// forbidden/unauthorized（没权限）、bad_request：重试不会变好。
		return false
	}
}

// autoAdoptFailureState 依据一次失败产出要落库的状态。
//
// isRetryable 为 false 时落成 skipped（不再重扫）；为 true 时落成 failed
// 并计算退避时间；达到次数上限后同样转为不再重扫，但保留 attempts 计数
// 以便界面显示"尝试过几次"。
func autoAdoptFailureState(prev store.AutoAdoptState, has bool, isRetryable bool, reason string, now time.Time) store.AutoAdoptState {
	if !isRetryable {
		return store.AutoAdoptState{
			Status:        store.AutoAdoptStatusSkipped,
			Reason:        reason,
			Attempts:      prev.Attempts + 1,
			LastScannedAt: now,
			// NextCheckAt 零值 = 不再自动重扫。
		}
	}
	attempts := prev.Attempts + 1
	out := store.AutoAdoptState{
		Status:        store.AutoAdoptStatusFailed,
		Reason:        reason,
		Attempts:      attempts,
		LastScannedAt: now,
	}
	if attempts < autoAdoptMaxAttempts {
		out.NextCheckAt = now.Add(autoAdoptRetryDelay(attempts))
	} else {
		// itoa 是 httpx 包内的既有工具（router.go），直接复用不另写一份。
		out.Reason = reason + "（已重试 " + itoa(attempts) + " 次，暂停自动重试；可在界面手动重试）"
	}
	return out
}

// readFailureReason 生成"读不到编排配置"的用户可读结论。
//
// 环境性失败与确定性失败必须给出**不同的下一步**：
//   - 环境性（可重试）：告诉用户会自动再试，不必马上动手；
//   - 确定性（已停扫）：告诉用户自动扫描已暂停，需要进详情页手动重新扫描。
//
// 两者都要带上 Agent 给出的具体原因。以前可重试分支只写一句
// "请检查Agent版本、本地助手镜像及项目文件权限"，把最有用的信息
// （缺的是哪个镜像、是超时还是并发繁忙）丢掉了，用户只能挨个猜。
func readFailureReason(cerr *agentlink.ConnError, retryable bool) string {
	reason := "无法读取项目配置"
	if cerr != nil {
		if detail := strings.TrimSpace(cerr.Message); detail != "" {
			reason += "（" + detail + "）"
		}
	}
	if retryable {
		return reason + "，将按退避自动重试"
	}
	return reason + "，重试不会自动恢复，已暂停自动扫描"
}

// 注意：不要再手写一个 int64 转字符串的辅助函数——httpx 包里已有 itoa，
// 这里直接用它，避免同名重复实现（历史上就因为这类复制出现过改一处漏一处）。
