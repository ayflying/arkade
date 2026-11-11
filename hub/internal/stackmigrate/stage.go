// Package stackmigrate 实现编排（配置 + 镜像 + 数据卷）的跨服务器迁移编排。
//
// ============================ 为什么编排放在 Hub 而不是 Agent ============================
//
// 迁移要跨两台服务器：在 A 导出、传给 B、在 B 导入、在 B 启动。
// 只有 Hub 同时持有两边的连接与权限判断；Agent 甚至不知道有另一台机器的存在，
// 这是安全边界——Agent 手里有 docker.sock（等价宿主机 root），
// 若再过一手另一台 Agent 的接入凭据，一台机器被攻破就等于两台。
//
// ============================ 为什么不用一个"长 RPC"完成 ============================
//
// 迁移要几分钟到几十分钟。同步 HTTP 会被超时切断，而切断之后：
//   - 用户看到"失败"，实际镜像/卷已经在传；
//   - 用户不知道停在了哪一步，也不知道目标上留下了什么。
//
// 所以采用"start 立即返回 task → 增量轮询事件"的作业模型。
//
// ============================ 为什么不另造一套进度协议 ============================
//
// 前端已经有一套编排操作任务的日志弹窗（ComposeOperationResult + Seq/Stream），
// 迁移的用户认知与它完全一致："一次编排操作，会打出多条进度行"。
// 另造协议意味着前端要写第二个弹窗、两套轮询、两种错误展示。
//
// 因此本包的事件结构**照抄** compose 编排操作任务（同名字段同语义），
// 前端组件可以原样复用。
//
// 但它**不是** Agent 侧的任务表：`GET .../operation-tasks/{taskID}` 会先查 Agent，
// 查不到时回落到本包的任务表（见 httpx 的分流）。这一点是刻意的：
// 迁移的全局进度只可能存在于 Hub，Agent 端看到的是片段。
package stackmigrate

// 阶段编码。
//
// ============================ 为什么阶段要编码而不是只给文案 ============================
// 失败时必须能回答"停在第几步、目标上留下了什么"。
// 若只有中文文案，判断"是否已导入镜像""是否需要清理"就成了字符串匹配，
// 文案一改逻辑就崩。因此文案给人看，编码给代码用。
const (
	StageValidate   = "validate"
	StageReadConfig = "read_config"
	StagePlan       = "plan"
	StageImages     = "images"
	StageVolumes    = "volumes"
	StageImportConf = "import_config"
	StageStart      = "start"
	StageFinish     = "finish"
)

// StageOrder 是阶段的执行顺序（也是失败时"最远到哪一步"的判据）。
var StageOrder = []string{
	StageValidate, StageReadConfig, StagePlan,
	StageImages, StageVolumes, StageImportConf, StageStart, StageFinish,
}

// StageTitle 返回阶段的中文名（用于事件文案）。
func StageTitle(stage string) string {
	switch stage {
	case StageValidate:
		return "校验前置条件"
	case StageReadConfig:
		return "读取源端编排配置"
	case StagePlan:
		return "解析迁移清单"
	case StageImages:
		return "迁移容器镜像"
	case StageVolumes:
		return "迁移数据卷"
	case StageImportConf:
		return "写入目标端编排配置"
	case StageStart:
		return "启动编排"
	case StageFinish:
		return "完成"
	}
	return "未知阶段"
}

// StageIndex 返回阶段的序号（1 起）。未知阶段返回 0。
func StageIndex(stage string) int {
	for i, s := range StageOrder {
		if s == stage {
			return i + 1
		}
	}
	return 0
}

// TotalStages 是阶段总数（用于"第 N/M 步"的文案）。
//
// 不含 finish：finish 只是收尾标记，把它算进分母会让用户以为
// "走到最后一步还没完"还要再等一轮。
const TotalStages = 7
