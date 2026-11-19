package mcp

import "net/http"

const catalogExternalStack = catalogAgent + "/external-stacks/{name}"

func catalogExternalStacks() []Tool {
	external := func(fields ...catalogField) []catalogField {
		return append([]catalogField{catalogAgentID(), catalogStackName("name")}, fields...)
	}
	importFields := func(fields ...catalogField) []catalogField {
		return append([]catalogField{catalogAgentID(), catalogStackName("name"), catalogComposeContent(), catalogEnvContent("可选 .env 正文；预检与实际导入须提交相同内容。")}, fields...)
	}
	return []Tool{
		catalogTool("get_external_stack", "查看发现的外部 Compose 项目及关联容器。", http.MethodGet, catalogExternalStack, true, false, nil, nil, external()...),
		catalogTool("get_external_stack_config", "查看外部编排的配置校验值和草稿版本，不返回配置、草稿正文或诊断原文。", http.MethodGet, catalogExternalStack+"/config", true, false, nil, nil, external()...),
		catalogTool("save_external_stack_draft", "将外部 Compose 配置草稿保存到 Hub，不改宿主文件、不重启或重建容器；使用草稿版本防止覆盖其他编辑。", http.MethodPut, catalogExternalStack+"/draft", false, false, nil, nil,
			external(catalogComposeContent(), catalogConfigHash("source_hash", "可选的源配置哈希，取自读取外部配置的结果。", false), catalogInteger("expected_version", "首次创建用 0，编辑时用草稿当前版本。", true, 0, 0))...),
		catalogTool("retry_external_stack_adopt", "清除该项目的自动发现结论，后台将在下一轮重新读取配置并尝试自动接管；不会立即部署服务。", http.MethodPost, catalogExternalStack+"/retry-adopt", false, false, nil, nil, external()...),
		catalogTool("preview_stack_import", "预检 Compose 配置导入，不接管或启动项目；只有 can_import 为真且没有阻塞项时才继续，并保留返回的 config_hash。", http.MethodPost, catalogStackList+"/import-preview", true, false, nil, nil, importFields()...),
		catalogTool("import_stack", "登记通过预检的配置并尝试接管，不启动、停止或重建容器；Hub 可能已保存配置而 Agent 未接管，需检查 adopted 和 sync_ok。", http.MethodPost, catalogStackList+"/import", false, false, nil, catalogConfirm(),
			importFields(catalogConfigHash("expected_content_hash", "从 arkade_preview_stack_import 获取的 config_hash；修改 YAML 或 .env 后必须重新预检。", true))...),
		catalogTool("preview_external_stack_adopt", "预检将指定外部项目登记为 Arkade 编排；不修改原文件或运行中的容器。保留返回的 config_hash。", http.MethodPost, catalogExternalStack+"/apply-preview", true, false, nil, nil, external(catalogComposeContent())...),
		catalogTool("adopt_external_stack", "按预检结果将指定外部项目登记到 Arkade，不改原文件、不会启动或重建容器。检查 adopted 和 sync_ok，未接管时不要部署。", http.MethodPost, catalogExternalStack+"/apply", false, false, nil, catalogConfirm(),
			external(catalogComposeContent(), catalogConfigHash("expected_content_hash", "从 arkade_preview_external_stack_adopt 获取的 config_hash；修改 YAML 后必须重新预检。", true))...),
		catalogTool("copy_external_stack_config", "仅复制 YAML 到目标服务器对应的 Hub 记录，不覆盖已有项目，不复制环境文件、镜像或卷数据，不启动目标。源项目不变。若 YAML 内含凭据，其正文也会复制。", http.MethodPost, catalogExternalStack+"/copy", false, false, nil, catalogConfirm(),
			external(catalogString("target_agent_id", "目标服务器 ID。", true, 1, 0), catalogStackName("target_name"), catalogComposeContent())...),
	}
}
