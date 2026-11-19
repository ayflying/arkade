package mcp

import "net/http"

const (
	catalogStackList = catalogAgent + "/stacks"
	catalogStack     = catalogStackList + "/{name}"
	catalogStackTask = catalogStack + "/operation-tasks"
)

func catalogStacks() []Tool {
	stack := func(fields ...catalogField) []catalogField {
		return append([]catalogField{catalogAgentID(), catalogStackName("name")}, fields...)
	}
	return []Tool{
		catalogTool("list_stacks", "列出指定服务器的已纳管编排和发现的外部 Compose 项目。", http.MethodGet, catalogStackList, true, false, nil, nil, catalogAgentID()),
		catalogTool("get_stack", "查看已纳管编排的状态、版本、配置哈希及关联容器。", http.MethodGet, catalogStack, true, false, nil, nil, stack()...),
		catalogTool("get_stack_config", "读取当前编排 YAML 和环境文件供编辑；响应可能含凭据。", http.MethodGet, catalogStack+"/config", true, false, nil, nil, stack()...),
		catalogTool("get_stack_logs", "读取指定编排最近的日志，不持续跟踪；日志可能含凭据或个人数据。", http.MethodGet, catalogStack+"/logs", true, false, []string{"service", "tail"}, nil,
			stack(catalogString("service", "仅查看指定 Compose 服务；省略则查看整个项目。", false, 0, 0), catalogTail())...),
		catalogTool("list_stack_containers", "列出当前编排的服务容器状态。", http.MethodGet, catalogStack+"/ps", true, false, nil, nil, stack()...),
		catalogTool("create_stack", "创建编排并立即尝试部署；可能拉取镜像、创建容器、挂载卷和暴露端口。检查 sync_ok；Hub 保存成功不代表部署成功。", http.MethodPost, catalogStackList, false, false, nil, nil,
			catalogAgentID(), catalogStackName("name"), catalogComposeContent(), catalogEnvContent("可选的 .env 文件正文，最多 1 MiB。")),
		catalogTool("update_stack", "用读取到的 version 替换现有编排 YAML 并立即尝试部署；可能重建运行中的服务、改变网络和挂载。检查 sync_ok。", http.MethodPut, catalogStack, false, true, nil, nil,
			stack(catalogComposeContent(), catalogEnvContent("省略保留旧 .env；空字符串清空 .env。"), catalogInteger("version", "读取该项目时返回的当前版本；版本冲突应重新读取。", true, 1, 0))...),
		catalogTool("delete_stack", "下线并删除编排及其 Hub 记录；容器和项目网络会移除，启用 remove_volumes 可永久删除项目卷数据。", http.MethodDelete, catalogStack, false, true, []string{"remove_volumes"}, nil,
			stack(catalogBool("remove_volumes", "同时删除项目卷；默认保留。"))...),
		catalogTool("deploy_stack", "异步部署已保存编排；可能创建或重建容器、短暂中断服务。用 arkade_get_stack_operation 查询任务。", http.MethodPost, catalogStackTask, false, true, nil, map[string]any{"action": "up"}, stack(catalogRequestID())...),
		catalogTool("update_and_deploy_stack", "按版本保存新配置并异步部署；可能替换容器和网络、中断服务。部署失败时配置可能已经保存；用原 request_id 重试同一请求。", http.MethodPost, catalogStackTask, false, true, nil, map[string]any{"action": "up"},
			stack(catalogRequestID(), catalogComposeContent(), catalogEnvContent("省略保留旧 .env；空字符串清空 .env。"), catalogInteger("version", "读取该项目时返回的当前版本。", true, 1, 0), catalogConfigHash("expected_config_hash", "可选的当前配置哈希，用于额外冲突检测。", false))...),
		catalogTool("start_stack_operation", "提交指定编排的异步运维任务。stop/restart/down/rebuild 会中断服务，down 可删除卷，pull 会更新本地镜像，backup 会在服务器写入可能含敏感数据的归档。用 arkade_get_stack_operation 查询进度。", http.MethodPost, catalogStackTask, false, true, nil, nil,
			stack(catalogRequestID(), catalogField{"action", map[string]any{"type": "string", "enum": []string{"down", "stop", "restart", "pull", "rebuild", "backup"}, "description": "选择一项已有的 Compose 运维动作；部署和删除另有独立工具。"}, true},
				catalogField{"services", map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1}, "description": "仅 stop/restart 可指定服务；省略则操作整个项目。"}, false},
				catalogBool("remove_volumes", "仅 down 可选；启用可永久删除项目卷，默认保留。"), catalogBool("include_volumes", "仅 backup 可选；把卷数据也写入备份归档。"))...),
		catalogTool("get_stack_operation", "查询指定项目的运维或迁移任务；after 可用上次的 next_cursor 仅读取新增输出。任务日志可能含敏感信息。", http.MethodGet, catalogStackTask+"/{taskID}", true, false, []string{"after"}, nil,
			stack(catalogPattern("taskID", "任务返回的 task_id。", "^[A-Za-z0-9_-]+$", true, 8, 128), catalogInteger("after", "只读取该输出游标之后的事件；省略从头读取。", false, 0, 0))...),
		catalogTool("migrate_stack", "向不同的目标服务器迁移编排；源项目保留。目标会接收配置和镜像、可选卷数据并可启动服务；失败可能残留已导入的资源，检查任务的 migrate.residual。", http.MethodPost, catalogStack+"/migrate", false, true, nil, nil,
			stack(catalogString("target_agent_id", "不同于源服务器的目标服务器 ID。", true, 1, 0), catalogBool("include_volumes", "是否迁移受支持的项目卷数据；无法迁移的挂载需单独处理。"), catalogBool("start_after", "迁移后在目标启动服务；源仍保留，注意双重运行。"), catalogString("confirmation", "原 API 要求的编排名复述，必须和路径参数 name 完全相同。", true, 1, 64), catalogString("request_id", "可选的幂等请求标识，建议使用 UUID；仅重试完全相同的迁移时复用。", false, 1, 128))...),
	}
}

func catalogRequestID() catalogField {
	return catalogPattern("request_id", "幂等请求标识，建议新任务使用 UUID；仅重试完全相同的请求时复用。", "^[A-Za-z0-9_-]+$", true, 8, 128)
}
