package envelope

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
// 一次性容器只跑一条**代码里写死的** compose 命令，参数全部来自容器标签
// 且经过白名单校验。绝不能让调用方传入任意命令——那等于把 Hub 变成一个
// 可在宿主机任意起容器的远控端，比自更新本身的权限面大一个数量级。

// SelfUpdatePayload 请求 Agent 更新自己。
type SelfUpdatePayload struct {
	// RequestID 是幂等键，同时用作临时容器名的一部分。
	//
	// 重试同一个 request_id 不会起第二个更新；Hub 重启后也能凭它
	// 找回上一次更新的临时容器与日志。
	RequestID string `json:"request_id"`
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
	// false 且 Blockers 为空只可能是"镜像未变化"：此时没有创建任何容器，
	// 也不会中断服务。
	Accepted bool `json:"accepted"`

	// Message 给用户看的中文说明（例如"镜像未变化，无需更新"）。
	Message string `json:"message,omitempty"`
}
