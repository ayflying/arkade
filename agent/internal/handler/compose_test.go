package handler

// compose_test.go 覆盖编排类处理器。
//
// 测试策略：这些处理器最终会执行 docker compose 命令，但**本机没有 docker**，
// 因此命令必然失败。这不是缺陷而是正好能验证最重要的一条规则：
//   **命令失败必须向 Hub 报错，绝不能返回"成功"**。
// 用户最怕的是界面显示"已启动"而服务实际没起来。
//
// 落盘、解析、状态归并等纯逻辑由 internal/compose 的测试覆盖，
// 本文件只验证 handler 这一层的接线是否正确（校验、错误翻译、协议结构）。

import (
	"path/filepath"
	"strings"
	"testing"

	"arkade/agent/internal/compose"
	"arkade/protocol/envelope"
)

// newComposeRegistry 构造带编排能力的注册表，数据目录用临时目录。
func newComposeRegistry(t *testing.T) (*Registry, *compose.Manager) {
	t.Helper()
	mgr := compose.NewManager(t.TempDir(), "")
	reg := NewRegistry()
	RegisterDefaults(reg, &fakeRuntime{}, mgr, nil)
	return reg, mgr
}

// TestComposeMethodsRegistered 验证 11 个编排方法全部注册。
func TestComposeMethodsRegistered(t *testing.T) {
	reg, _ := newComposeRegistry(t)
	methods := []string{
		envelope.MethodComposeList, envelope.MethodComposeUp, envelope.MethodComposeDown,
		envelope.MethodComposeStop, envelope.MethodComposeRestart, envelope.MethodComposePull,
		envelope.MethodComposeRebuild, envelope.MethodComposeRemove, envelope.MethodComposeBackup,
		envelope.MethodComposeLogs, envelope.MethodComposePS,
	}
	for _, m := range methods {
		if !reg.Has(m) {
			t.Errorf("方法 %s 未注册", m)
		}
	}
}

// TestComposeNilManagerSkipsRegistration 验证没有数据目录时不注册编排方法。
//
// 注册了也只会一调用就失败，Hub 侧收到 unsupported 的提示
// 比收到"写文件失败"清楚得多。
func TestComposeNilManagerSkipsRegistration(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg, &fakeRuntime{}, nil, nil)
	if reg.Has(envelope.MethodComposeUp) {
		t.Error("没有 Manager 时不应注册编排方法")
	}
	// 但资源方法仍应注册——它们不依赖数据目录。
	if !reg.Has(envelope.MethodImageList) {
		t.Error("资源方法仍应注册")
	}
}

// TestComposeUpValidation 验证参数校验。
func TestComposeUpValidation(t *testing.T) {
	reg, _ := newComposeRegistry(t)
	cases := []struct {
		name    string
		payload any
		want    string
	}{
		{"nil", nil, "缺少载荷"},
		{"空内容", envelope.ComposeSpecPayload{Name: "web", Content: ""}, "内容不能为空"},
		{"空白内容", envelope.ComposeSpecPayload{Name: "web", Content: "  \n\t "}, "内容不能为空"},
		{"非法项目名", envelope.ComposeSpecPayload{Name: "../evil", Content: "services: {}"}, "项目名"},
		{"大写项目名", envelope.ComposeSpecPayload{Name: "Web", Content: "services: {}"}, "项目名"},
		{"空项目名", envelope.ComposeSpecPayload{Name: "", Content: "services: {}"}, "项目名"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := request(t, reg, envelope.MethodComposeUp, c.payload)
			if resp.Error == nil {
				t.Fatal("应返回错误")
			}
			if resp.Error.Code != envelope.CodeBadRequest {
				t.Errorf("错误码应为 bad_request，实际 %q", resp.Error.Code)
			}
			if !strings.Contains(resp.Error.Message, c.want) {
				t.Errorf("文案应含 %q，实际 %q", c.want, resp.Error.Message)
			}
		})
	}
}

// TestComposeUpValidatesBeforeWriting 验证"配置不合法时**不写盘**"。
//
// 这条规则替代了早先的"命令失败也要保住内容"：
// 保住内容的动机是对的，但做法错了。把 compose 解析不了的 compose.yml
// 写进项目目录，会让这个项目之后**所有**命令都失败——
// 包括 down/stop，用户连"把它停掉"都做不到，只能登机器手工删文件。
//
// 更严重的是它会覆盖**正在运行的项目**的配置：
// 用户看到"下发失败"，但下一次 stop/restart 用的已经是被判为无效的那份内容。
//
// 因此现在的顺序是：先在临时目录校验 → 通过才写 → 再 up。
// 本机没有 docker，校验必然失败，所以断言"目录里什么都没有"。
func TestComposeUpValidatesBeforeWriting(t *testing.T) {
	reg, mgr := newComposeRegistry(t)
	const content = "services:\n  web:\n    image: nginx\n"

	resp := request(t, reg, envelope.MethodComposeUp, envelope.ComposeSpecPayload{
		Name: "web", Content: content,
	})
	// 校验失败必须报错（不能静默成功）。
	if resp.Error == nil {
		t.Fatal("校验失败时必须返回错误，不能报告成功")
	}
	// 且**绝不能**留下文件：既没有 compose.yml，也没有 .env。
	if mgr.Workspace().Exists("web") {
		t.Error("校验未通过时不应写入 compose.yml（否则后续 stop/restart 都会失败）")
	}
	if mgr.Workspace().EnvExists("web") {
		t.Error("校验未通过时不应写入 .env")
	}
}

// TestComposeUpHashMismatch 验证哈希不符时拒绝且不写盘。
//
// 声明哈希与内容对不上说明传输或 Hub 侧计算出了问题。
// 早先的实现选择"先落盘再报错便于排查"，那是危险的：
// 它会覆盖正在运行项目的配置，于是下一次 stop/restart 用的就是这份可疑内容。
// 排查所需的两个哈希值在错误信息里已经够了，不需要看磁盘上的文件。
func TestComposeUpHashMismatch(t *testing.T) {
	reg, mgr := newComposeRegistry(t)
	const content = "services: {}\n"

	resp := request(t, reg, envelope.MethodComposeUp, envelope.ComposeSpecPayload{
		Name: "web", Content: content, ContentHash: "deadbeefdeadbeef",
	})
	if resp.Error == nil {
		t.Fatal("哈希不符时应返回错误")
	}
	if !strings.Contains(resp.Error.Message, "哈希") {
		t.Errorf("文案应说明哈希不一致，实际 %q", resp.Error.Message)
	}
	// 不得写盘。
	if mgr.Workspace().Exists("web") {
		t.Error("哈希不符时不得写入 compose.yml")
	}
	if mgr.Workspace().EnvExists("web") {
		t.Error("哈希不符时不得写入 .env")
	}
}

// TestComposeOpRequiresExisting 验证对不存在的编排执行操作给出明确提示。
func TestComposeOpRequiresExisting(t *testing.T) {
	reg, _ := newComposeRegistry(t)
	methods := []struct {
		method  string
		payload any
	}{
		{envelope.MethodComposeDown, envelope.ComposeOpPayload{Name: "ghost"}},
		{envelope.MethodComposeStop, envelope.ComposeOpPayload{Name: "ghost"}},
		{envelope.MethodComposeRestart, envelope.ComposeOpPayload{Name: "ghost"}},
		{envelope.MethodComposePull, envelope.ComposeOpPayload{Name: "ghost"}},
		{envelope.MethodComposeRebuild, envelope.ComposeOpPayload{Name: "ghost"}},
		{envelope.MethodComposeLogs, envelope.ComposeLogsPayload{Name: "ghost"}},
		{envelope.MethodComposePS, envelope.ComposeOpPayload{Name: "ghost"}},
	}
	for _, c := range methods {
		t.Run(c.method, func(t *testing.T) {
			resp := request(t, reg, c.method, c.payload)
			if resp.Error == nil {
				t.Fatal("对不存在的编排操作应报错")
			}
			if !strings.Contains(resp.Error.Message, "不存在") {
				t.Errorf("文案应说明编排不存在，实际 %q", resp.Error.Message)
			}
		})
	}
}

// TestComposeOpNilPayload 验证缺载荷被拒。
func TestComposeOpNilPayload(t *testing.T) {
	reg, _ := newComposeRegistry(t)
	for _, m := range []string{
		envelope.MethodComposeDown, envelope.MethodComposeStop,
		envelope.MethodComposeRestart, envelope.MethodComposePull,
		envelope.MethodComposeRebuild, envelope.MethodComposeRemove,
		envelope.MethodComposeLogs, envelope.MethodComposePS,
		envelope.MethodComposeBackup,
	} {
		t.Run(m, func(t *testing.T) {
			resp := request(t, reg, m, nil)
			if resp.Error == nil {
				t.Fatal("缺载荷应报错")
			}
			if resp.Error.Code != envelope.CodeBadRequest {
				t.Errorf("错误码应为 bad_request，实际 %q", resp.Error.Code)
			}
		})
	}
}

// TestComposeListEmpty 验证空工作区的列表返回空数组而不是 null。
func TestComposeListEmpty(t *testing.T) {
	reg, _ := newComposeRegistry(t)
	resp := request(t, reg, envelope.MethodComposeList, nil)
	if resp.Error != nil {
		t.Fatalf("空列表不应报错: %+v", resp.Error)
	}
	var res envelope.ComposeListResult
	if err := envelope.DecodePayload(resp, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Projects) != 0 {
		t.Errorf("应为空，实际 %d", len(res.Projects))
	}
}

// TestComposeListReportsLocalHash 验证列表带上本机算出的哈希。
//
// 这个哈希是 Hub 判定 drift 的唯一依据，缺了它界面上就永远无法
// 告警"本机文件被手工改过"。因此必须来自实际文件内容。
func TestComposeListReportsLocalHash(t *testing.T) {
	reg, mgr := newComposeRegistry(t)
	const content = "services:\n  db:\n    image: postgres:16\n"
	if _, err := mgr.Workspace().Write("db", content); err != nil {
		t.Fatal(err)
	}

	resp := request(t, reg, envelope.MethodComposeList, nil)
	if resp.Error != nil {
		t.Fatalf("不应报错: %+v", resp.Error)
	}
	var res envelope.ComposeListResult
	if err := envelope.DecodePayload(resp, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Projects) != 1 {
		t.Fatalf("应有 1 个项目，实际 %d", len(res.Projects))
	}
	p := res.Projects[0]
	if p.Name != "db" {
		t.Errorf("项目名应为 db，实际 %q", p.Name)
	}
	if p.ConfigHash != compose.Hash(content) {
		t.Errorf("哈希应与文件内容一致，实际 %q", p.ConfigHash)
	}
	// 服务名应被解析出来。
	if len(p.Services) != 1 || p.Services[0] != "db" {
		t.Errorf("服务列表应为 [db]，实际 %v", p.Services)
	}
	// 本机没 docker，ps 失败 → 状态应为 unknown 而不是 stopped。
	if p.Status != compose.StatusUnknown {
		t.Errorf("ps 失败时状态应为 unknown，实际 %q", p.Status)
	}
}

// TestComposeLogsTailClamp 验证 tail 被限制在合法范围。
//
// 只验证不 panic 且能走到执行阶段（本机无 docker 会失败）——
// 越界的 tail 会被 Manager 内部夹紧，不会传给命令行造成异常。
func TestComposeLogsTailClamp(t *testing.T) {
	reg, mgr := newComposeRegistry(t)
	if _, err := mgr.Workspace().Write("web", "services: {}\n"); err != nil {
		t.Fatal(err)
	}
	// 超大 tail：应被夹到上限后执行（然后因没有 docker 而失败）。
	resp := request(t, reg, envelope.MethodComposeLogs,
		envelope.ComposeLogsPayload{Name: "web", TailLines: 999999})
	if resp.Error == nil {
		t.Error("本机无 docker，应返回错误")
	}
	// 负数 tail 同样不应导致异常。
	resp = request(t, reg, envelope.MethodComposeLogs,
		envelope.ComposeLogsPayload{Name: "web", TailLines: -5})
	if resp.Error == nil {
		t.Error("本机无 docker，应返回错误")
	}
}

// TestComposeLogsRejectsBadService 验证非法服务名被拒（安全边界）。
func TestComposeLogsRejectsBadService(t *testing.T) {
	reg, mgr := newComposeRegistry(t)
	if _, err := mgr.Workspace().Write("web", "services: {}\n"); err != nil {
		t.Fatal(err)
	}
	resp := request(t, reg, envelope.MethodComposeLogs,
		envelope.ComposeLogsPayload{Name: "web", Service: "../evil"})
	if resp.Error == nil {
		t.Fatal("非法服务名应被拒绝")
	}
	if resp.Error.Code != envelope.CodeBadRequest {
		t.Errorf("错误码应为 bad_request，实际 %q", resp.Error.Code)
	}
}

// TestComposeRemoveDeletesLocalFiles 验证删除会清掉本机目录。
//
// 即使用户的容器本来就没跑（down 失败），删除意图也必须完成——
// 否则界面上删了、机器上还留着，下次列表又冒出来。
func TestComposeRemoveDeletesLocalFiles(t *testing.T) {
	reg, mgr := newComposeRegistry(t)
	if _, err := mgr.Workspace().Write("web", "services: {}\n"); err != nil {
		t.Fatal(err)
	}
	if !mgr.Workspace().Exists("web") {
		t.Fatal("准备数据失败")
	}

	request(t, reg, envelope.MethodComposeRemove, envelope.ComposeOpPayload{Name: "web"})

	if mgr.Workspace().Exists("web") {
		t.Error("删除后本机目录应已清除")
	}
}

// TestComposeFailureIsRuntimeFailed 验证命令失败映射成 runtime_failed。
//
// 这个错误码决定了运维怎么看待失败：runtime_failed 表示
// "被管理的机器上的操作没跑通"（用户配置或环境问题），
// internal 则表示"Agent 程序有 bug"。混用会导致误报警或漏报警。
func TestComposeFailureIsRuntimeFailed(t *testing.T) {
	reg, mgr := newComposeRegistry(t)
	if _, err := mgr.Workspace().Write("web", "services: {}\n"); err != nil {
		t.Fatal(err)
	}
	resp := request(t, reg, envelope.MethodComposeStop, envelope.ComposeOpPayload{Name: "web"})
	if resp.Error == nil {
		t.Fatal("本机无 docker，应返回错误")
	}
	if resp.Error.Code != envelope.CodeRuntimeFailed {
		t.Errorf("错误码应为 runtime_failed，实际 %q（消息 %q）", resp.Error.Code, resp.Error.Message)
	}
}

// TestComposeBackupWritesFiles 验证备份即使 docker 不可用也能完成编排文件部分。
//
// 这是刻意设计：备份的价值在于保住用户的配置，配置本身不依赖 docker。
// 卷备份失败只会写进 manifest 警告，不应让整个备份失败。
func TestComposeBackupWritesFiles(t *testing.T) {
	reg, mgr := newComposeRegistry(t)
	const content = "services:\n  web:\n    image: nginx\n"
	if _, err := mgr.Workspace().Write("web", content); err != nil {
		t.Fatal(err)
	}

	resp := request(t, reg, envelope.MethodComposeBackup,
		envelope.ComposeBackupPayload{Name: "web", IncludeVolumes: false})
	if resp.Error != nil {
		t.Fatalf("备份不应失败: %+v", resp.Error)
	}
	var res envelope.ComposeBackupResult
	if err := envelope.DecodePayload(resp, &res); err != nil {
		t.Fatal(err)
	}
	if res.Name != "web" {
		t.Errorf("项目名应为 web，实际 %q", res.Name)
	}
	if res.SizeBytes <= 0 {
		t.Errorf("备份大小应大于 0，实际 %d", res.SizeBytes)
	}
	if len(res.Files) == 0 {
		t.Error("应至少备份了 compose.yml 与 manifest")
	}
	// 路径应在数据目录下的 backups 里。
	if !strings.Contains(filepath.ToSlash(res.Path), "/backups/web/") {
		t.Errorf("备份路径应在 backups/web 下，实际 %q", res.Path)
	}
}
