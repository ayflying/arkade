package httpx

// handlers_compose_common.go 放编排接口的共用逻辑：项目名与内容校验、
// 命令输出截断、哈希计算、drift 判定。
//
// 单独成文件的原因：这些规则被创建/更新/详情/动作等多个 handler 共用，
// 复制到各处必然出现「创建时校验了、更新时忘了」的分裂。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"arkade/protocol/envelope"
)

const (
	// maxComposeContentBytes 是编排文件内容上限。
	// 给 1MB 是因为正常的 compose.yml 只有几 KB，1MB 已经足够宽松，
	// 同时能挡住"塞垃圾内容把数据库和消息撑爆"的滥用。
	maxComposeContentBytes = 1 << 20
	// composeHashHexLen 是内容哈希取 sha256 十六进制的前 16 位。
	// 与协议注释（payload_resource.go 的 ConfigHash）保持一致——
	// Hub 与 Agent 必须用完全相同的算法，否则 drift 判定会永远为真。
	composeHashHexLen = 16
	// maxComposeOutputRunes 是回传给界面的命令输出上限（字符数）。
	// compose 失败时可能打出几百行构建日志，全量塞进 JSON 既没意义也拖慢界面。
	maxComposeOutputRunes = 4000
)

// composeConfigHash 计算编排内容的哈希（sha256 前 16 位十六进制）。
//
// 为什么不用文件 mtime：mtime 在容器重建、镜像层拷贝后不可靠；
// 内容哈希是唯一能跨机器、跨重启稳定比对的依据。
func composeConfigHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:composeHashHexLen]
}

// validComposeName 校验编排项目名。
//
// 规则 [a-z0-9][a-z0-9_-]*，且必须严格全串匹配。
//
// 为什么这么严：项目名会直接拼进 Agent 本机的文件路径
// （<data_dir>/stacks/<name>/compose.yml）以及 docker compose -p 参数。
// 一旦允许 "."、".."、"/" 或空格，就能构造出 ../ 越界写到数据目录之外，
// 甚至在 docker 命令里注入额外参数。逐字符白名单比正则黑名单更难写错。
func validComposeName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
			// 首字符不能是分隔符：否则 ".", "-" 开头的目录名在某些
			// shell 与文件系统里会被当成选项或隐藏文件。
			if i == 0 {
				return false
			}
		default:
			// 大写字母、点、斜杠、空格等一律拒绝。
			return false
		}
	}
	return true
}

// validateComposeContent 校验编排内容。
//
// 只做「非空 + 不超过上限」两项，不校验 YAML 语法：
// Hub 侧不该内置一个 YAML 解析器（会引入依赖），真正的语法错误由 Agent 侧
// docker compose 命令报出来，那段错误信息对用户更有用。
func validateComposeContent(content string) error {
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("编排内容不能为空")
	}
	if len(content) > maxComposeContentBytes {
		return fmt.Errorf("编排内容过大（%d 字节，上限 %d 字节）", len(content), maxComposeContentBytes)
	}
	return nil
}

// truncateComposeOutput 截断命令输出，保留尾部。
//
// 保留尾部而不是头部：compose 的失败原因（端口冲突、镜像拉取失败、配置错误）
// 都出现在输出末尾，掐头部才能留下有用信息。
func truncateComposeOutput(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= maxComposeOutputRunes {
		return s
	}
	return "…（输出过长，已截断前部）\n" + string(r[len(r)-maxComposeOutputRunes:])
}

// composeOpResultToView 把 Agent 返回的操作结果转成接口响应。
//
// 额外带上 sync_ok 字段：Agent 侧执行成功为 true。前端据此决定是显示
// 绿色成功还是橙色警告（记录已保存但本机执行失败）。
func composeOpResultToView(agentID string, res envelope.ComposeOpResult) map[string]any {
	return map[string]any{
		"agent_id":    agentID,
		"name":        res.Name,
		"status":      res.Status,
		"config_hash": res.ConfigHash,
		"output":      truncateComposeOutput(res.Output),
	}
}

// composeProjectStatusOffline 是 Agent 离线时列表里显示的状态。
//
// 与容器接口的语义保持一致（那边离线就是不列或标 offline），
// 不新造 "unreachable" 之类的词，避免前端为同一个概念写两套判断。
const composeProjectStatusOffline = "offline"

// composeStackView 是编排列表/详情里的一条记录（Hub 记录 + Agent 状态合并后的视图）。
type composeStackView struct {
	AgentID string `json:"agent_id"`
	Name    string `json:"name"`
	// Status 来自 Agent 实时状态；Agent 离线时为 "offline"。
	Status string `json:"status"`
	// ConfigHash 是 Hub 侧权威副本的哈希。
	ConfigHash string `json:"config_hash"`
	// RemoteHash 是 Agent 本机文件的哈希。Agent 离线时为空。
	RemoteHash string `json:"remote_hash"`
	// Drift 为真表示两边哈希不一致（本地手改过、或上次下发没成功）。
	// 用户明确要求"两边都存并同步"，那么偏离就必须在界面上可见，
	// 而不是静默用某一端覆盖另一端。
	Drift    bool     `json:"drift"`
	Services []string `json:"services"`
	Running  int      `json:"running"`
	Total    int      `json:"total"`
	// Version 是乐观锁版本号，前端更新时必须原样带回。
	Version   int64 `json:"version"`
	UpdatedAt int64 `json:"updated_at"`
	// SyncedAt 记录最近一次成功下发给 Agent 的时间（0 表示从未成功下发）。
	SyncedAt int64 `json:"synced_at"`
}

// requireComposeName 校验并写出项目名错误。返回 false 表示已写响应。
func requireComposeName(w http.ResponseWriter, d Deps, name string) bool {
	if !validComposeName(name) {
		badRequest(w, d, "编排项目名不合法：只能包含小写字母、数字、下划线、连字符，且必须以字母或数字开头（实际 %q）", name)
		return false
	}
	return true
}
