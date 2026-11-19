package mcp

import "net/http"

const (
	catalogAgentImages = catalogAgent + "/images"
	catalogImageTasks  = "/api/image-tasks"
	catalogImageTask   = catalogImageTasks + "/{taskID}"
)

func catalogImages() []Tool {
	reference := func() catalogField {
		return catalogString("reference", "镜像引用，例如 registry.example.com/team/app:tag；最多 512 字节。", true, 1, 512)
	}
	taskID := func() catalogField {
		return catalogString("taskID", "镜像任务的 id，从提交结果或 arkade_list_image_tasks 取得。", true, 1, 0)
	}
	return []Tool{
		catalogTool("list_images", "列出指定服务器的本地镜像、标签及镜像 ID。", http.MethodGet, catalogAgentImages, true, false, nil, nil, catalogAgentID()),
		catalogTool("delete_image", "删除服务器上的本地镜像或标签；强制删除可能移除多个标签，后续重建或回滚可能需要重新拉取。", http.MethodDelete, catalogAgentImages+"/{imageID}", false, true, []string{"force"}, nil,
			catalogAgentID(), catalogString("imageID", "镜像 ID，从 arkade_list_images 取得。", true, 1, 0), catalogBool("force", "是否强制删除镜像；默认关闭。")),
		catalogTool("pull_image", "启动从镜像仓库拉取镜像的任务；会消耗网络和磁盘，可能更新本地同名标签，不会重启容器。用 arkade_get_image_task 查询进度。", http.MethodPost, catalogAgentImages+"/pull", false, false, nil, nil, catalogAgentID(), reference()),
		catalogTool("export_image", "启动镜像导出任务，在 Hub 生成可能含私有镜像内容的大型归档。用 arkade_get_image_task 查看 download_ready，再通过 Arkade Web/API 下载；MCP 不传输原始镜像归档，也不支持流式导入。", http.MethodPost, catalogAgentImages+"/export", false, false, nil, nil, catalogAgentID(), reference()),
		catalogTool("migrate_image", "启动跨服务器镜像迁移；源镜像保留，目标会导入镜像、可能改变同名标签，并消耗目标磁盘和两端网络。不会迁移容器或卷。", http.MethodPost, catalogAgentImages+"/migrate", false, true, nil, catalogConfirm(),
			catalogAgentID(), reference(), catalogString("target_agent_id", "不同于源服务器的目标在线服务器 ID。", true, 1, 0)),
		catalogTool("list_image_tasks", "列出可见的镜像任务，可按服务器筛选。", http.MethodGet, catalogImageTasks, true, false, []string{"agent_id"}, nil,
			catalogString("agent_id", "可选的服务器 ID。", false, 1, 0)),
		catalogTool("get_image_task", "查询镜像拉取、导出、迁移或经 Web/API 导入的任务进度。导出完成后用 Web/API 下载原始归档。", http.MethodGet, catalogImageTask, true, false, nil, nil, taskID()),
		catalogTool("cancel_image_task", "请求取消镜像任务；可能中断传输，已写入的镜像或标签变更不应视为已回滚。", http.MethodPost, catalogImageTask+"/cancel", false, true, nil, nil, taskID()),
	}
}
