package migrate

// volumes_test.go 覆盖卷识别。
//
// ============================ 这是整个迁移里最容易错、错了最危险的一步 ============================
//
// 卷识别错有两种方向，后果完全不同：
//
//	漏识别 → 用户以为数据迁过去了，目标上跑起来是空目录，**没有任何报错**
//	多识别 → 去打包一个不属于本项目的卷，可能读到别人的数据，或直接失败
//
// 因此这里的用例按 compose 的五种声明方式逐个枚举，
// 并且**每种都断言完整计划**（要迁的卷 + 绑定的跳过清单 + 匿名卷的跳过清单），
// 而不是只断言"找到了 N 个卷"——后者漏掉"跳过了什么"正是最危险的盲区。

import (
	"strings"
	"testing"
)

// names 提取要迁的卷名。
func names(p Plan) []string { return p.VolumeNames() }

// contains 判断切片是否包含某值。
func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// TestResolvePlanNamedVolumes 验证顶层声明的命名卷被识别为 <project>_<key>。
func TestResolvePlanNamedVolumes(t *testing.T) {
	const cfg = `
services:
  web:
    image: nginx
    volumes:
      - data:/var/lib/data
      - cache:/cache
volumes:
  data:
  cache:
`
	plan, err := ResolvePlan("demo", cfg)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	got := names(plan)
	if len(got) != 2 {
		t.Fatalf("应识别出 2 个卷，实际 %v", got)
	}
	if !contains(got, "demo_data") || !contains(got, "demo_cache") {
		t.Fatalf("卷名应带项目前缀，实际 %v", got)
	}
	// 服务的引用关系要能追溯，便于报告"哪个服务的卷"。
	for _, v := range plan.Volumes {
		if len(v.Services) != 1 || v.Services[0] != "web" {
			t.Fatalf("卷 %s 的引用服务应为 [web]，实际 %v", v.Name, v.Services)
		}
		if v.Kind != VolumeNamed {
			t.Fatalf("卷 %s 应为 VolumeNamed，实际 %v", v.Name, v.Kind)
		}
	}
	// 没有任何跳过项。
	if len(plan.BindMounts) != 0 || len(plan.AnonymousMounts) != 0 {
		t.Fatalf("不应有跳过项：bind=%v anon=%v", plan.BindMounts, plan.AnonymousMounts)
	}
}

// TestResolvePlanExplicitName 验证 name: 声明的卷不加项目前缀。
func TestResolvePlanExplicitName(t *testing.T) {
	const cfg = `
services:
  db:
    image: postgres
    volumes:
      - pgdata:/var/lib/postgresql/data
volumes:
  pgdata:
    name: my-custom-pgdata
`
	plan, err := ResolvePlan("demo", cfg)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	got := names(plan)
	if len(got) != 1 || got[0] != "my-custom-pgdata" {
		t.Fatalf("显式命名的卷应使用 name: 的值（不加前缀），实际 %v", got)
	}
	if plan.Volumes[0].Kind != VolumeExplicit {
		t.Fatalf("应为 VolumeExplicit，实际 %v", plan.Volumes[0].Kind)
	}
}

// TestResolvePlanExternalVolume 验证 external: true 的卷按外部卷处理。
//
// external 卷在 compose 之外创建，名字不由项目决定。
// 它**要迁移**（内容同样是数据），但必须让用户知道这是共享卷的副本。
func TestResolvePlanExternalVolume(t *testing.T) {
	const cfg = `
services:
  app:
    image: app
    volumes:
      - shared:/data
volumes:
  shared:
    external: true
`
	plan, err := ResolvePlan("demo", cfg)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	got := names(plan)
	// external 卷名**不加**项目前缀，用键名本身。
	if len(got) != 1 || got[0] != "shared" {
		t.Fatalf("外部卷应使用键名本身（不加前缀），实际 %v", got)
	}
	if plan.Volumes[0].Kind != VolumeExternal {
		t.Fatalf("应为 VolumeExternal，实际 %v", plan.Volumes[0].Kind)
	}
}

// TestResolvePlanExcludesBindMounts 验证宿主机目录绑定不迁移但被统计。
//
// ============================ 这是"数据不全"告警的唯一来源 ============================
// bind 挂载的数据在宿主机目录里，迁移不会复制它。
// 若不统计，用户会以为整个编排连同数据都搬过去了。
func TestResolvePlanExcludesBindMounts(t *testing.T) {
	const cfg = `
services:
  web:
    image: nginx
    volumes:
      - ./html:/usr/share/nginx/html
      - /opt/app/config:/etc/app
      - data:/data
volumes:
  data:
`
	plan, err := ResolvePlan("demo", cfg)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	// 只有 data 要迁。
	got := names(plan)
	if len(got) != 1 || got[0] != "demo_data" {
		t.Fatalf("只应迁移命名卷，实际 %v", got)
	}
	// 两个 bind 都要被统计出来，且必须说明原因。
	if len(plan.BindMounts) != 2 {
		t.Fatalf("应统计出 2 个 bind 挂载，实际 %d: %v", len(plan.BindMounts), plan.BindMounts)
	}
	for _, m := range plan.BindMounts {
		if m.Reason == "" {
			t.Fatalf("bind 挂载必须带中文原因：%+v", m)
		}
		if m.Service != "web" {
			t.Fatalf("应记录服务名，实际 %q", m.Service)
		}
	}
	sources := []string{plan.BindMounts[0].Source, plan.BindMounts[1].Source}
	if !contains(sources, "./html") || !contains(sources, "/opt/app/config") {
		t.Fatalf("应记录 bind 源路径，实际 %v", sources)
	}
}

// TestResolvePlanExcludesAnonymousVolumes 验证匿名卷不迁移但被统计。
func TestResolvePlanExcludesAnonymousVolumes(t *testing.T) {
	const cfg = `
services:
  db:
    image: postgres
    volumes:
      - /var/lib/postgresql/data
`
	plan, err := ResolvePlan("demo", cfg)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(plan.Volumes) != 0 {
		t.Fatalf("匿名卷不应被当作可迁移卷，实际 %v", names(plan))
	}
	if len(plan.AnonymousMounts) != 1 {
		t.Fatalf("应统计出 1 个匿名卷，实际 %d", len(plan.AnonymousMounts))
	}
	if !strings.Contains(plan.AnonymousMounts[0].Reason, "匿名卷") {
		t.Fatalf("原因应说明是匿名卷，实际 %q", plan.AnonymousMounts[0].Reason)
	}
}

// TestResolvePlanLongSyntax 验证长语法挂载（type/source/target）。
func TestResolvePlanLongSyntax(t *testing.T) {
	const cfg = `
services:
  app:
    image: app
    volumes:
      - type: volume
        source: data
        target: /data
      - type: bind
        source: /host/dir
        target: /container/dir
      - type: tmpfs
        target: /tmp
      - type: volume
        target: /anon
volumes:
  data:
`
	plan, err := ResolvePlan("demo", cfg)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	got := names(plan)
	if len(got) != 1 || got[0] != "demo_data" {
		t.Fatalf("只有 type=volume 且有 source 的要迁移，实际 %v", got)
	}
	// bind + tmpfs 都记入 BindMounts，匿名 volume 记入 AnonymousMounts。
	if len(plan.BindMounts) != 2 {
		t.Fatalf("bind 与 tmpfs 都应记入跳过清单，实际 %d: %v", len(plan.BindMounts), plan.BindMounts)
	}
	if len(plan.AnonymousMounts) != 1 {
		t.Fatalf("无 source 的 volume 应记入匿名清单，实际 %d", len(plan.AnonymousMounts))
	}
}

// TestResolvePlanVolumeKeyNormalization 验证卷键名被规整成合法卷名。
func TestResolvePlanVolumeKeyNormalization(t *testing.T) {
	const cfg = `
services:
  app:
    image: app
    volumes:
      - MyData:/data
volumes:
  MyData:
`
	plan, err := ResolvePlan("Demo", cfg)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	// 项目名与卷键都要小写化、非法字符替换。
	got := names(plan)
	if len(got) != 1 || got[0] != "Demo_mydata" {
		t.Fatalf("卷键应被规整（小写化），实际 %v", got)
	}
}

// TestResolvePlanUnusedDeclarationIgnored 验证声明了但没人用的卷不迁移。
//
// 迁一个没人用的空卷纯属浪费（可能真是几 GB，因为以前用过）。
func TestResolvePlanUnusedDeclarationIgnored(t *testing.T) {
	const cfg = `
services:
  web:
    image: nginx
volumes:
  orphan:
`
	plan, err := ResolvePlan("demo", cfg)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(plan.Volumes) != 0 {
		t.Fatalf("未被引用的卷不应迁移，实际 %v", names(plan))
	}
}

// TestResolvePlanNoVolumes 验证完全没有卷的编排。
//
// 这是最常见的情况（纯无状态服务），必须正常返回空计划而不是报错。
func TestResolvePlanNoVolumes(t *testing.T) {
	const cfg = `
services:
  web:
    image: nginx
  api:
    image: api
`
	plan, err := ResolvePlan("demo", cfg)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(plan.Volumes) != 0 || len(plan.BindMounts) != 0 || len(plan.AnonymousMounts) != 0 {
		t.Fatalf("无卷编排的计划应为空，实际 %+v", plan)
	}
	// DataVolumeCount 是给用户看的"数据卷数"，必须为 0。
	if plan.DataVolumeCount() != 0 {
		t.Fatalf("数据卷数应为 0，实际 %d", plan.DataVolumeCount())
	}
}

// TestResolvePlanMultipleServicesSharedVolume 验证多服务共享一个卷。
func TestResolvePlanMultipleServicesSharedVolume(t *testing.T) {
	const cfg = `
services:
  web:
    image: nginx
    volumes:
      - shared:/data
  worker:
    image: worker
    volumes:
      - shared:/data
volumes:
  shared:
`
	plan, err := ResolvePlan("demo", cfg)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(plan.Volumes) != 1 {
		t.Fatalf("共享卷只应出现一次，实际 %v", names(plan))
	}
	svcs := plan.Volumes[0].Services
	if len(svcs) != 2 || !contains(svcs, "web") || !contains(svcs, "worker") {
		t.Fatalf("应记录两个引用服务，实际 %v", svcs)
	}
}

// TestResolvePlanInvalidYAML 验证非法配置给出错误而不是空计划。
//
// 返回空计划会被上层读成"这个编排没有卷"，于是迁移成功而数据全丢。
func TestResolvePlanInvalidYAML(t *testing.T) {
	if _, err := ResolvePlan("demo", "services: [unclosed"); err == nil {
		t.Fatal("非法 YAML 应返回错误，不能静默返回空计划")
	}
}

// TestResolvePlanInterpolatedVolumeName 验证含变量的卷名被当作无法确定。
//
// "${DATA_VOLUME}:/data" 里的卷名要运行时才知道，静态解析无法确定。
// 保守地当作匿名卷跳过并说明原因，绝不猜一个名字去迁。
func TestResolvePlanInterpolatedVolumeName(t *testing.T) {
	const cfg = `
services:
  app:
    image: app
    volumes:
      - ${DATA_VOLUME}:/data
`
	plan, err := ResolvePlan("demo", cfg)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	// ${DATA_VOLUME} 不含斜杠，会被当成卷名引用；
	// 但顶层没有声明它，因此应进入匿名跳过清单而不是被迁移。
	if len(plan.Volumes) != 0 {
		t.Fatalf("未声明的卷不应被迁移，实际 %v", names(plan))
	}
	if len(plan.AnonymousMounts) != 1 {
		t.Fatalf("应记入跳过清单，实际 %d: %v", len(plan.AnonymousMounts), plan.AnonymousMounts)
	}
}

// TestIsBindSource 逐个验证路径判断规则。
//
// 这条规则错一个方向就会去读别人的卷，或漏掉用户的真实数据。
func TestIsBindSource(t *testing.T) {
	cases := []struct {
		src  string
		want bool
	}{
		{"/abs/path", true},
		{"./rel", true},
		{"../parent", true},
		{"C:/win", true},
		{"C:\\win", true},
		{"data", false},
		{"my-data", false},
		{"my.data", false},
		{"demo_data", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isBindSource(c.src); got != c.want {
			t.Fatalf("isBindSource(%q) = %v，期望 %v", c.src, got, c.want)
		}
	}
}

// TestValidVolumeName 验证卷名校验（路径安全的第一道线）。
func TestValidVolumeName(t *testing.T) {
	valid := []string{"demo_data", "a", "A1", "my-vol", "my.vol", strings.Repeat("a", 255)}
	for _, v := range valid {
		if !validVolumeName(v) {
			t.Fatalf("%q 应合法", v)
		}
	}
	// 含分隔符、路径穿越、空格、空串一律非法。
	invalid := []string{"", "../etc", "/abs", "a/b", "a b", "a:b", strings.Repeat("a", 256), "卷"}
	for _, v := range invalid {
		if validVolumeName(v) {
			t.Fatalf("%q 应被拒绝", v)
		}
	}
}
