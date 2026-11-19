package mcp

import "net/http"

const (
	catalogContainerList = catalogAgent + "/containers"
	catalogContainer     = catalogContainerList + "/{containerID}"
	catalogNetworkList   = catalogAgent + "/networks"
	catalogVolumeList    = catalogAgent + "/volumes"
	catalogVolumeFiles   = catalogAgent + "/volume-files"
)

func catalogResources() []Tool {
	container := func(fields ...catalogField) []catalogField {
		return append([]catalogField{catalogAgentID(), catalogString("containerID", "容器 ID，从 arkade_list_containers 取得。", true, 1, 0)}, fields...)
	}
	tools := []Tool{
		catalogTool("list_containers", "列出指定服务器上的容器、镜像引用及状态。", http.MethodGet, catalogContainerList, true, false, nil, nil, catalogAgentID()),
		catalogTool("inspect_container", "查看指定容器的状态等摘要，不返回环境变量、命令、宿主机路径或网络地址。", http.MethodGet, catalogContainer, true, false, nil, nil, container()...),
		catalogTool("list_container_metrics", "查看服务器最近一次容器 CPU、内存及网络采样；留意 stale 和指标可用性字段。", http.MethodGet, catalogAgent+"/container-metrics", true, false, nil, nil, catalogAgentID()),
		catalogTool("start_container", "启动指定现有容器；它会按已有配置挂载卷、启用端口，可能与正在运行的服务冲突。", http.MethodPost, catalogContainer+"/start", false, false, nil, nil, container()...),
		catalogTool("stop_container", "停止指定容器，容器内的服务立即不可用，进行中的连接或作业可能中断。", http.MethodPost, catalogContainer+"/stop", false, true, nil, nil, container()...),
		catalogTool("restart_container", "重启指定容器，短暂中断服务和连接，未持久化的进程状态会丢失。", http.MethodPost, catalogContainer+"/restart", false, true, nil, nil, container()...),
		catalogTool("delete_container", "删除指定容器及其可写层；force 可能强制停止运行中的容器，remove_volumes 可一并删除关联的匿名卷。", http.MethodDelete, catalogContainer, false, true, []string{"force", "remove_volumes"}, nil,
			container(catalogBool("force", "是否强制停止并删除运行中的容器；默认关闭。"), catalogBool("remove_volumes", "是否一并删除关联的匿名卷；默认保留。"))...),
		catalogTool("list_networks", "列出指定服务器的容器网络及网络 ID。", http.MethodGet, catalogNetworkList, true, false, nil, nil, catalogAgentID()),
		catalogTool("delete_network", "删除指定容器网络；依赖该网络的后续部署可能失败，运行时可能拒绝仍被容器使用的网络。", http.MethodDelete, catalogNetworkList+"/{networkID}", false, true, nil, nil,
			catalogAgentID(), catalogString("networkID", "网络 ID，从 arkade_list_networks 取得。", true, 1, 0)),
		catalogTool("list_volumes", "列出指定服务器的容器存储卷。", http.MethodGet, catalogVolumeList, true, false, nil, nil, catalogAgentID()),
		catalogTool("delete_volume", "永久删除指定存储卷及其内容；运行时可能拒绝仍被容器使用的卷。", http.MethodDelete, catalogVolumeList+"/{name}", false, true, nil, nil,
			catalogAgentID(), catalogString("name", "存储卷名称，从 arkade_list_volumes 取得。", true, 1, 0)),
	}
	return append(tools, catalogVolumeFileTools()...)
}

func catalogVolumeFileTools() []Tool {
	volumeOperation := func(name, description, operation string, readOnly, destructive bool, fields ...catalogField) Tool {
		path := catalogString("path", "卷内以 / 分隔的相对路径；不允许绝对路径、反斜杠、空路径段、. 或 .. 段。", true, 1, 4096)
		if operation == "list" {
			path = catalogString("path", "卷内相对目录；省略、空字符串或 . 表示卷根。", false, 0, 4096)
		}
		return catalogTool(name, description, http.MethodPost, catalogVolumeFiles, readOnly, destructive, nil, map[string]any{"operation": operation},
			append([]catalogField{catalogAgentID(), catalogPattern("volume", "已有存储卷名称，从 arkade_list_volumes 取得。", "^[A-Za-z0-9][A-Za-z0-9_.-]*$", true, 1, 255), path}, fields...)...)
	}
	content := func() catalogField {
		return catalogString("data_base64", "标准 Base64 编码的文件正文，解码后最多 4 MiB；空字符串表示空文件。", true, 0, ((4<<20)+2)/3*4)
	}
	create := volumeOperation("create_volume_file", "在已有卷内新建文件，不覆盖任何现有文件；新文件可能立即被挂载此卷的服务读取。", "write", false, false, content())
	create.FixedBody["create_only"] = true
	return []Tool{
		volumeOperation("list_volume_files", "列出已有存储卷内的目录项，只在指定卷内操作。", "list", true, false),
		volumeOperation("read_volume_file", "查询卷内文件的大小与 SHA-256 校验值，不返回文件正文。覆盖前可查询当前校验值。", "read", true, false),
		create,
		volumeOperation("replace_volume_file", "以新内容替换卷内文件；必须提供读取时的 SHA-256 防止并发覆盖。替换会改变挂载此卷的服务所见数据。", "write", false, true, content(),
			catalogPattern("expected_sha256", "从 arkade_read_volume_file 取得的原文件 SHA-256。", "^[A-Fa-f0-9]{64}$", true, 64, 64)),
		volumeOperation("create_volume_directory", "在已有卷内创建目录；不递归创建缺失的父目录。", "mkdir", false, false),
		volumeOperation("rename_volume_file", "在同一卷内移动或重命名文件或目录，不覆盖目标；依赖原有路径的服务可能无法访问。", "rename", false, true,
			catalogString("destination", "目标卷内相对路径，不能等于 path，目标必须不存在。", true, 1, 4096)),
		volumeOperation("delete_volume_file", "永久删除卷内的文件或空目录；不会递归删除非空目录。依赖该路径的服务可能报错。", "delete", false, true),
	}
}
