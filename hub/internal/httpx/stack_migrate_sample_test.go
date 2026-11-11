package httpx

// stack_migrate_sample_test.go：导出迁移接口的**真实 JSON 响应样例**。
//
// ============================ 这个文件是为了什么 ============================
// 前端要与后端逐字段对齐、写实测脚本时，需要"后端真的会吐什么"。
// 手写拼 JSON 样例是**不可接受**的：手写的样例永远是对的，
// 而真实结构体可能因为 omitempty、指针空值、字段名拼错而与它不一致，
// 那种不一致正是联调时最耗时的问题。
//
// 因此这里直接调用生产代码里的 migrateTaskView()，
// 用与真实响应完全相同的组装路径生成样例，只是把 agentID/编排名
// 换成真实样式的值（243-7d5cc78d / lanet）。
//
// 运行方式：
//
//	go test ./internal/httpx -run TestPrintMigrateSamples -v
//
// ============================ 输出不要提交进仓库 ============================
// 只留生成器，不留快照文件。理由：
//   - 快照会和代码一起漂移。它不会自己更新，而一旦过期，
//     看的人无从判断"是代码变了"还是"快照本来就旧"。
//   - 把 JSON 样例落进 httpx 包会让人误以为是测试夹具，
//     进而有人开始"修正"样例去迁就实现——那就本末倒置了。
//
// 对齐前端时按需运行、当场贴给对方即可，事实来源永远是生产代码。
//
// 它**不校验任何东西**（没有断言），只负责打印。之所以还写成测试而不是
// 一个临时 main：临时 main 需要把内部函数导出去，而导出内部函数本身
// 就是这个仓库不想要的设计。测试文件天然能做"内部调用 + 外部可见输出"。
//
// 若某天它因为编译错误而失效，说明迁移的视图结构变了——
// 那时样例本来就该重新生成，正好提醒我们。

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"arkade/hub/internal/stackmigrate"
)

// printJSON 把任意值格式化成缩进 JSON 并打印。
func printJSON(t *testing.T, label string, v any) {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("%s: 序列化失败: %v", label, err)
	}
	fmt.Printf("\n===== %s =====\n%s\n", label, raw)
}

// TestPrintMigrateSamples 打印三种真实响应样例。
//
// 用 -v 运行才能看到输出。
func TestPrintMigrateSamples(t *testing.T) {
	const (
		agentID = "243-7d5cc78d"
		stack   = "lanet"
		taskID  = "9f2c1a7b3e4d5f60718293a4b5c6d7e8"
	)

	t.Run("成功终态", func(t *testing.T) {
		start := time.Date(2026, 3, 14, 9, 12, 7, 0, time.UTC)
		snap := stackmigrate.Snapshot{
			TaskID: taskID, Name: stack,
			Source: agentID, Target: "12a-91f0be22",
			IncludeVolumes: true, StartAfter: true,
			Status: stackmigrate.StatusSucceeded,
			// 事件按真实执行顺序构造，文案与 manager.go 里产出的完全一致。
			Events: []stackmigrate.Event{
				{Seq: 1, Stream: "stage", Text: "第 1 步「校验前置条件」"},
				{Seq: 2, Stream: "stdout", Text: "校验通过：两端在线、权限具备、目标无同名编排"},
				{Seq: 3, Stream: "stage", Text: "第 2 步「读取源端编排配置」"},
				{Seq: 4, Stream: "stdout", Text: "已读取源端编排配置（哈希 8f3a1c9d2b7e4a05）"},
				{Seq: 5, Stream: "stage", Text: "第 3 步「解析迁移清单」"},
				{Seq: 6, Stream: "stdout", Text: "需要迁移的容器镜像 2 个"},
				{Seq: 7, Stream: "stdout", Text: "需要迁移的数据卷 1 个：lanet_pgdata"},
				{Seq: 8, Stream: "stdout", Text: "已跳过宿主机绑定目录 1 个（迁移不会复制宿主机上的目录内容）"},
				{Seq: 9, Stream: "stage", Text: "第 4 步「迁移容器镜像」"},
				{Seq: 10, Stream: "stdout", Text: "[1/2] 迁移镜像 ghcr.io/acme/lanet-api:v1.4.2"},
				{Seq: 11, Stream: "stdout", Text: "  镜像 ghcr.io/acme/lanet-api:v1.4.2 已传输 50%"},
				{Seq: 12, Stream: "stdout", Text: "[2/2] 迁移镜像 postgres:16.3-alpine"},
				{Seq: 13, Stream: "stdout", Text: "镜像迁移完成，共 2 个"},
				{Seq: 14, Stream: "stage", Text: "第 5 步「迁移数据卷」"},
				{Seq: 15, Stream: "stdout", Text: "[1/1] 迁移数据卷 lanet_pgdata"},
				{Seq: 16, Stream: "stdout", Text: "  数据卷 lanet_pgdata 已传输 100%"},
				{Seq: 17, Stream: "stdout", Text: "数据卷迁移完成，共 1 个"},
				{Seq: 18, Stream: "stage", Text: "第 6 步「写入目标端编排配置」"},
				{Seq: 19, Stream: "stdout", Text: "已在目标服务器写入编排配置"},
				{Seq: 20, Stream: "stage", Text: "第 7 步「启动编排」"},
				{Seq: 21, Stream: "stdout", Text: "已在目标服务器启动该编排"},
				{Seq: 22, Stream: "stage", Text: "迁移完成"},
			},
			NextCursor: 22,
			Stage:      stackmigrate.StageFinish,
			StageTitle: stackmigrate.StageTitle(stackmigrate.StageFinish),
			Residual: stackmigrate.Residual{
				ImagesImported: 2, VolumesRestored: 1,
				ComposeWritten: true, Started: true,
			},
			ImageCount: 2, VolumeCount: 1, BytesCopied: 8_246_411_264, BindDirs: 1,
			CreatedAt: start, StartedAt: start,
			FinishedAt: start.Add(4*time.Minute + 21*time.Second),
		}
		printJSON(t, "POST /migrate 202 成功受理（同一结构，终态）", map[string]any{
			"task": migrateTaskView(agentID, stack, snap),
		})
	})

	t.Run("运行中增量轮询", func(t *testing.T) {
		start := time.Date(2026, 3, 14, 9, 12, 7, 0, time.UTC)
		snap := stackmigrate.Snapshot{
			TaskID: taskID, Name: stack,
			Source: agentID, Target: "12a-91f0be22",
			IncludeVolumes: false, StartAfter: false,
			Status: stackmigrate.StatusRunning,
			// 增量：after=6 之后的 4 条。
			Events: []stackmigrate.Event{
				{Seq: 7, Stream: "stdout", Text: "本次未迁移数据卷，目标上将是空数据（该项目有 1 个数据卷被跳过）"},
				{Seq: 8, Stream: "stdout", Text: "已跳过宿主机绑定目录 1 个（迁移不会复制宿主机上的目录内容）"},
				{Seq: 9, Stream: "stage", Text: "第 4 步「迁移容器镜像」"},
				{Seq: 10, Stream: "stdout", Text: "[1/2] 迁移镜像 ghcr.io/acme/lanet-api:v1.4.2"},
			},
			NextCursor: 10,
			Stage:      stackmigrate.StageImages,
			StageTitle: stackmigrate.StageTitle(stackmigrate.StageImages),
			Residual:   stackmigrate.Residual{},
			ImageCount: 2, VolumeCount: 1, SkippedVolumes: 1, BindDirs: 1,
			CreatedAt: start, StartedAt: start,
		}
		printJSON(t, "GET /operation-tasks/{id}?after=6 运行中增量", map[string]any{
			"task": migrateTaskView(agentID, stack, snap),
		})
	})

	t.Run("失败终态", func(t *testing.T) {
		start := time.Date(2026, 3, 14, 9, 12, 7, 0, time.UTC)
		snap := stackmigrate.Snapshot{
			TaskID: taskID, Name: stack,
			Source: agentID, Target: "12a-91f0be22",
			IncludeVolumes: true, StartAfter: true,
			Status: stackmigrate.StatusFailed,
			Events: []stackmigrate.Event{
				{Seq: 14, Stream: "stage", Text: "第 5 步「迁移数据卷」"},
				{Seq: 15, Stream: "stdout", Text: "[1/2] 迁移数据卷 lanet_pgdata"},
				{Seq: 16, Stream: "stderr", Text: "迁移失败（停在第 5 步「迁移数据卷」）：迁移数据卷 lanet_uploads 失败：目标磁盘空间不足。目标服务器上已留下：已导入 2 个镜像、已恢复 1 个数据卷"},
			},
			NextCursor: 16,
			Error: "迁移失败（停在第 5 步「迁移数据卷」）：迁移数据卷 lanet_uploads 失败：" +
				"目标磁盘空间不足。目标服务器上已留下：已导入 2 个镜像、已恢复 1 个数据卷",
			Stage:      stackmigrate.StageVolumes,
			StageTitle: stackmigrate.StageTitle(stackmigrate.StageVolumes),
			Residual: stackmigrate.Residual{
				ImagesImported: 2, VolumesRestored: 1,
			},
			ImageCount: 2, VolumeCount: 2,
			CreatedAt: start, StartedAt: start,
			FinishedAt: start.Add(3*time.Minute + 2*time.Second),
		}
		printJSON(t, "GET /operation-tasks/{id} 失败终态", map[string]any{
			"task": migrateTaskView(agentID, stack, snap),
		})
	})

	t.Run("校验阶段即失败_无残留", func(t *testing.T) {
		start := time.Date(2026, 3, 14, 9, 12, 7, 0, time.UTC)
		snap := stackmigrate.Snapshot{
			TaskID: taskID, Name: stack,
			Source: agentID, Target: "12a-91f0be22",
			Status: stackmigrate.StatusFailed,
			Events: []stackmigrate.Event{
				{Seq: 1, Stream: "stage", Text: "第 1 步「校验前置条件」"},
				{Seq: 2, Stream: "stderr", Text: "迁移失败（停在第 1 步「校验前置条件」）：目标服务器上已存在同名编排「lanet」，请先在目标上改名或删除后再迁移。目标服务器上未留下任何改动"},
			},
			NextCursor: 2,
			Error: "迁移失败（停在第 1 步「校验前置条件」）：目标服务器上已存在同名编排「lanet」，" +
				"请先在目标上改名或删除后再迁移。目标服务器上未留下任何改动",
			Stage:      stackmigrate.StageValidate,
			StageTitle: stackmigrate.StageTitle(stackmigrate.StageValidate),
			Residual:   stackmigrate.Residual{},
			CreatedAt:  start, StartedAt: start,
			FinishedAt: start.Add(120 * time.Millisecond),
		}
		printJSON(t, "GET /operation-tasks/{id} 同名冲突（校验阶段失败）", map[string]any{
			"task": migrateTaskView(agentID, stack, snap),
		})
	})
}
