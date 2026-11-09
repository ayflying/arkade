package compose

// external.go 从 Docker 引擎的容器标签反推"机器上已有、但不是 Arkade 创建的"编排项目。
//
// 背景（真实故障）：用户反馈「容器编排怎么是空的，这台设备应该有不少编排」。
// 根因是 List() 只枚举 Arkade 工作区目录，而机器上那 5 个项目
// （act-runner / adinsight-db-lab / arkade-demo / lanet / portainer-agent）
// 是运维直接用 docker compose 起的，工作区里没有目录，于是列表全空。
//
// 设计边界（极其重要，不允许越界）：
//
//  1. **只读容器标签**。发现依据是 Docker 给每个容器打的
//     com.docker.compose.project / .service 标签，这是 compose 自己写的元数据。
//  2. **绝不读取外部配置**。不去猜、不去读外部项目的 docker-compose.yml
//     或 .env——那些文件可能含密钥，且内容属于别人的管辖权。
//  3. **绝不执行任何外部 up/down/stop**。这些项目是生产负载，
//     Arkade 只"看见"它们，不碰它们。
//  4. **绝不落盘、绝不接管**。发现结果只用于展示"服务器已有编排（未纳管）"，
//     不写进 Hub 数据库、不生成对应的工作区目录、不复制配置。
//
// 换句话说：Arkade 对这些项目只有**只读可见性**，没有所有权。

import (
	"context"
	"sort"
	"strings"
)

// Compose 项目标签的固定键名（Docker 官方约定，不是我们的发明）。
const (
	labelComposeProject = "com.docker.compose.project"
	labelComposeService = "com.docker.compose.service"
	// labelComposeOneoff 标记 docker compose run 产生的一次性容器。
	// 它们不属于项目主体（跑完就该消失），统计服务数时必须排除，
	// 否则"1 个服务"的项目会因为跑过几次 run 而显示成 5 个服务。
	labelComposeOneoff = "com.docker.compose.oneoff"
)

// ExternalProject 是从引擎标签反推出的外部编排项目。
//
// 与 ProjectStatus（Arkade 管理的项目）刻意分开类型：
// 外部项目没有目录、没有配置哈希、不可编辑，
// 共用一个结构体会让"哪些字段对外部项目无意义"变得含糊，
// 前端也容易误给它们渲染编辑/删除按钮。
type ExternalProject struct {
	Name string
	// Services 是服务名集合（去重、排序）。
	Services []string
	// Running 是处于运行状态的容器数。
	Running int
	// Total 是容器总数（含已停止）。
	Total int
}

// Status 归并整体状态，取值与 Arkade 项目保持一致，
// 让前端可以用同一套渲染逻辑。
//
// 全停 → stopped 而不是 running：用户看一眼就知道这个项目现在没在跑。
func (p ExternalProject) Status() string {
	switch {
	case p.Total == 0:
		// 理论不可达（没有容器就不会有项目），但保持防御。
		return "unknown"
	case p.Running == 0:
		return "stopped"
	case p.Running == p.Total:
		return "running"
	default:
		return "partial"
	}
}

// 容器状态字符串（Docker /containers/json 的 State 字段）。
const stateRunning = "running"

// containerLabelSource 是发现外部项目所需的容器信息。
//
// 单独定义精简接口而不是依赖 runtime 包的具体类型：
// compose 包不应该为了读三个字段就绑上整个引擎接口，
// 这样测试里给一个函数就够了。
type containerLabelSource interface {
	ListContainerLabels(ctx context.Context) ([]ContainerLabels, error)
}

// ContainerLabels 是发现所需的单个容器标签信息。
type ContainerLabels struct {
	State  string
	Labels map[string]string
}

// DiscoverExternal 从引擎反推外部编排项目。
//
// 返回的项目**不含** Arkade 工作区里的项目——去重由调用方按名字做，
// 因为只有调用方同时知道两侧的名单。
//
// 错误处理：引擎不可用时返回错误而不是空列表。
// **空列表和"引擎查不到"是两回事**：前者表示"确实没有外部项目"，
// 后者表示"我不知道有什么"。把错误吞成空列表会让用户以为机器上
// 真的没有编排，而实际上是一次连接故障——这正是本 bug 的同类问题。
func DiscoverExternal(ctx context.Context, src containerLabelSource) ([]ExternalProject, error) {
	containers, err := src.ListContainerLabels(ctx)
	if err != nil {
		return nil, err
	}

	// 按项目名聚合。
	type agg struct {
		services map[string]struct{}
		running  int
		total    int
	}
	projects := map[string]*agg{}

	for _, c := range containers {
		name := strings.TrimSpace(c.Labels[labelComposeProject])
		if name == "" {
			// 不是 compose 容器（手工 docker run 起的），跳过。
			continue
		}
		// 排除 compose run 的一次性容器。
		if strings.EqualFold(strings.TrimSpace(c.Labels[labelComposeOneoff]), "true") {
			continue
		}
		a := projects[name]
		if a == nil {
			a = &agg{services: map[string]struct{}{}}
			projects[name] = a
		}
		a.total++
		if c.State == stateRunning {
			a.running++
		}
		// 服务名可能缺失（标签不全），缺失时不占一个服务名额。
		if svc := strings.TrimSpace(c.Labels[labelComposeService]); svc != "" {
			a.services[svc] = struct{}{}
		}
	}

	out := make([]ExternalProject, 0, len(projects))
	for name, a := range projects {
		services := make([]string, 0, len(a.services))
		for s := range a.services {
			services = append(services, s)
		}
		// 排序：map 遍历顺序随机，不排序会让前端列表每次刷新都在跳。
		sort.Strings(services)
		out = append(out, ExternalProject{
			Name:     name,
			Services: services,
			Running:  a.running,
			Total:    a.total,
		})
	}
	// 按名称排序，保证多个项目之间顺序稳定。
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
