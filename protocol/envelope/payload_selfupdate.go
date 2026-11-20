package envelope

import "strings"

// payload_selfupdate.go：self.update —— 更新 Agent 自己所在容器的镜像。
//
// ============================ 语义（务必先读） ============================
//
// 整个协议里只有这一个方法会**把执行者自己杀掉**。因此它刻意不返回执行结果：
//
//   - 真正的重建（compose up）交给一个一次性容器异步执行，本方法随即返回；
//   - 调用方靠"版本号是否变化 / 是否重新上线"判断成败，而不是等一个永远不会到的应答。
//
// 若在 Agent 进程内同步执行 compose up，进程会在命令中途被杀掉，
// 调用方既拿不到应答，也无法区分"成功了"与"半路失败"。
//
// ============================ 为什么不做通用"执行命令" ============================
//
// 一次性容器只跑代码里固定的 Compose 命令，目标身份来自容器标签并经过校验。
// 本机可提交同项目的纳管配置快照，但调用方不能指定执行命令或目标服务。

// SelfUpdatePayload 请求 Agent 更新自己。
type SelfUpdatePayload struct {
	// RequestID 是幂等键，同时用作临时容器名的一部分。
	//
	// 重试同一个 request_id 不会起第二个更新；Hub 重启后也能凭它
	// 找回上一次更新的临时容器与日志。
	RequestID string `json:"request_id,omitempty"`
	// Probe 只探测真实容器归属，不创建或清理助手。探测不发送 RequestID，
	// 让不识别 Probe 的老执行面也因缺少幂等键而拒绝执行。
	Probe bool `json:"probe,omitempty"`
	// Managed 仅由 Hub 在核验同名项目的已同步配置后填入，不是公共 API 输入。
	Managed *SelfUpdateManagedConfig `json:"managed,omitempty"`
}

// SelfUpdateManagedConfig 是同一个 Compose 项目的纳管配置快照，可能包含密钥。
// 不得写入请求日志、容器环境变量、命令行或标签。
type SelfUpdateManagedConfig struct {
	Project    string `json:"project"`
	Content    string `json:"content"`
	EnvContent string `json:"env_content,omitempty"`
}

const (
	SelfUpdateHelperPrefix         = "arkade-selfupdate-"
	SelfUpdateManagedPrepareFailed = "ARKADE_SELF_UPDATE_PREPARE_FAILED"
	SelfUpdateManagedComposeFailed = "ARKADE_SELF_UPDATE_COMPOSE_FAILED"
)

// SelfUpdateHelperName 从请求 ID 推导更新助手名称，可在 Hub 重启后按名称查询。
func SelfUpdateHelperName(requestID string) string {
	var b strings.Builder
	for _, r := range requestID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-_")
	if s == "" {
		s = "manual"
	}
	if len(s) > 40 {
		s = s[:40]
	}
	return SelfUpdateHelperPrefix + s
}

// SelfUpdateResult 是一次自更新的**受理结果**。
//
// 注意：`Blockers` 非空表示"这个部署方式不能自更新"，属于**结论**而不是故障
// （与 ComposeSourceResult.Blockers 同一套约定）。此时其余字段为空、
// 也没有创建任何临时容器。
type SelfUpdateResult struct {
	// Blockers 是不能自更新的固定中文原因（由 Agent 侧生成，不含宿主路径）。
	Blockers []string `json:"blockers,omitempty"`

	// Project / Service / WorkingDir / ConfigFile 来自容器自带的 compose 标签。
	Project    string `json:"project,omitempty"`
	Service    string `json:"service,omitempty"`
	WorkingDir string `json:"working_dir,omitempty"`
	ConfigFile string `json:"config_file,omitempty"`

	// CurrentImage 当前容器实际运行的镜像引用（来自 Config.Image）。
	CurrentImage string `json:"current_image,omitempty"`

	// HelperContainerID / HelperContainerName 是执行重启的一次性容器。
	HelperContainerID   string `json:"helper_container_id,omitempty"`
	HelperContainerName string `json:"helper_container_name,omitempty"`

	// Accepted 是否已提交重建。
	//
	// Probe 的成功结果只有项目身份，Accepted=false；非探测请求只有
	// Accepted=true 才表示助手已启动或复用，最终结果需查询助手状态。
	Accepted bool `json:"accepted"`

	// Message 给用户看的中文说明（例如"镜像未变化，无需更新"）。
	Message string `json:"message,omitempty"`
}
