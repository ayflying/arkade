package compose

// backup_volume_test.go：备份的卷发现、未追踪卷、以及"备份不嵌套"的回归断言。
//
// ============================ 本文件覆盖什么 ============================
// task-16 的三个关注点，各有独立测试：
//
//  1. 卷发现改用 compose 的 project 标签过滤（更快、且不再静默漏掉显式命名的卷）；
//  2. 显式命名的卷**算已备份**，external 卷才算未追踪；
//  3. 备份不会被历史备份污染（**防回归**，不是修 bug——见下）。
//
// 解析层（parseVolumeLines*）是纯函数，在任何平台都能测；
// 需要真 docker 的路径靠 Linux 上的假 docker 脚本覆盖。

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ---- 一、label 输出的解析 ----

// TestParseVolumeLinesByLabel 验证 label 过滤输出的解析。
//
// 真实输出形如（制表符分隔）：
//
//	demo_data<TAB>data
//	my-explicit-vol<TAB>cache
//
// 第二个正是旧实现会漏掉的显式命名卷：它的物理名不带 demo_ 前缀。
func TestParseVolumeLinesByLabel(t *testing.T) {
	out := "demo_data\tdata\n" +
		"my-explicit-vol\tcache\n" +
		"\n" + // 末尾空行，真实输出常见
		"demo_uploads\tuploads\n"

	got := parseVolumeLinesByLabel(out)
	if len(got) != 3 {
		t.Fatalf("应解析出 3 个卷，实际 %d: %+v", len(got), got)
	}

	byName := make(map[string]projectVolume, len(got))
	for _, v := range got {
		byName[v.Name] = v
	}

	// 显式命名的卷必须出现，且能带出其逻辑名。
	v, ok := byName["my-explicit-vol"]
	if !ok {
		t.Fatal("显式命名的卷 my-explicit-vol 未被解析出来（旧实现会漏掉它）")
	}
	if v.Logical != "cache" {
		t.Fatalf("逻辑名应为 cache，实际 %q", v.Logical)
	}

	// 结果必须按卷名排序，便于稳定输出与测试。
	for i := 1; i < len(got); i++ {
		if got[i-1].Name > got[i].Name {
			t.Fatalf("结果未按卷名排序: %+v", got)
		}
	}
}

// TestParseVolumeLinesByLabelWithoutLogicalName 验证缺逻辑名时不产生怪值。
//
// 某些 docker 版本或非 compose 创建的卷没有 com.docker.compose.volume 标签，
// 输出的第二列是空的（甚至整行没有制表符）。
func TestParseVolumeLinesByLabelWithoutLogicalName(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{"尾部制表符但无值", "demo_data\t\n", "demo_data"},
		{"整行没有制表符", "demo_data\n", "demo_data"},
		{"CRLF 换行", "demo_data\tdata\r\n", "demo_data"},
	}
	for _, c := range cases {
		got := parseVolumeLinesByLabel(c.out)
		if len(got) != 1 {
			t.Fatalf("%s：应解析出 1 个卷，实际 %d", c.name, len(got))
		}
		if got[0].Name != c.want {
			t.Fatalf("%s：卷名应为 %q，实际 %q", c.name, c.want, got[0].Name)
		}
	}
}

// TestParseVolumeLinesByLabelDedup 验证重复行被去重。
func TestParseVolumeLinesByLabelDedup(t *testing.T) {
	out := "demo_data\tdata\ndemo_data\tdata\n"
	got := parseVolumeLinesByLabel(out)
	if len(got) != 1 {
		t.Fatalf("重复行应去重为 1 个，实际 %d: %+v", len(got), got)
	}
}

// ---- 二、回退路径的前缀匹配 ----

// TestParseVolumeLinesByPrefix 验证前缀回退只捞本项目命名空间下的卷。
func TestParseVolumeLinesByPrefix(t *testing.T) {
	out := "demo_data\n" +
		"demo_uploads\n" +
		"other_data\n" + // 别的项目，必须排除
		"my-explicit-vol\n" + // 显式命名，前缀匹配拿不到（这正是它的局限）
		"\n"

	got := parseVolumeLinesByPrefix(out, "demo")
	if len(got) != 2 {
		t.Fatalf("应只匹配到 2 个本项目卷，实际 %d: %+v", len(got), got)
	}
	if got[0].Name != "demo_data" || got[1].Name != "demo_uploads" {
		t.Fatalf("匹配结果不对: %+v", got)
	}
	// 前缀路径能反推逻辑名。
	if got[0].Logical != "data" {
		t.Fatalf("逻辑名应为 data，实际 %q", got[0].Logical)
	}
}

// TestPrefixPathMissesExplicitNameVolume 用测试把"前缀匹配会漏卷"这件事钉住。
//
// 这条测试不是在测一个 bug，而是在**记录两条路径的语义差异**：
// 正因为前缀路径会漏掉显式命名的卷，主路径才必须用 label 过滤。
// 如果哪天有人把主路径改回前缀匹配，这条测试会提醒他代价是什么。
func TestPrefixPathMissesExplicitNameVolume(t *testing.T) {
	out := "demo_data\nmy-explicit-vol\n"
	got := parseVolumeLinesByPrefix(out, "demo")

	for _, v := range got {
		if v.Name == "my-explicit-vol" {
			t.Fatal("前缀匹配不应找到显式命名的卷——若找到了，说明匹配逻辑被改成了子串/模糊匹配")
		}
	}
	if len(got) != 1 {
		t.Fatalf("只应找到 demo_data 一个，实际 %+v", got)
	}
}

// ---- 三、未追踪卷的识别 ----

// TestDiscoverUntrackedVolumes 验证 external 与显式命名的判定。
func TestDiscoverUntrackedVolumes(t *testing.T) {
	dir := t.TempDir()
	cfg := `services:
  web:
    image: nginx
    volumes:
      - data:/data
      - cache:/cache
      - pgdata:/pg
  db:
    image: postgres
volumes:
  data:
  cache:
    name: shared-cache
  pgdata:
    external: true
`
	if err := os.WriteFile(filepath.Join(dir, composeFileName), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(t.TempDir(), "")

	// 场景 A：显式命名的 shared-cache **已被发现**（label 过滤捞到了）。
	// 此时它算已备份，只有 external 的 pgdata 是未追踪。
	present := map[string]struct{}{
		"demo_data":    {},
		"shared-cache": {},
	}
	got := m.discoverUntrackedVolumes(dir, present)
	if len(got) != 1 {
		t.Fatalf("应只有 1 个未追踪卷（external），实际 %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "pgdata") || !strings.Contains(got[0], "external") {
		t.Fatalf("未追踪卷应是 external 的 pgdata，实际 %q", got[0])
	}

	// 场景 B：显式命名的卷**没被发现**（卷还没创建等），此时也要报告。
	got = m.discoverUntrackedVolumes(dir, map[string]struct{}{"demo_data": {}})
	if len(got) != 2 {
		t.Fatalf("应有 2 个未追踪卷，实际 %d: %v", len(got), got)
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "shared-cache") {
		t.Fatalf("未被发现的显式命名卷应出现在报告里: %v", got)
	}
	if !strings.Contains(joined, "pgdata") {
		t.Fatalf("external 卷应出现在报告里: %v", got)
	}
}

// TestDiscoverUntrackedVolumesNoDeclaration 验证配置里没有 volumes 段时不报错。
func TestDiscoverUntrackedVolumesNoDeclaration(t *testing.T) {
	dir := t.TempDir()
	cfg := "services:\n  web:\n    image: nginx\n"
	if err := os.WriteFile(filepath.Join(dir, composeFileName), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(t.TempDir(), "")
	if got := m.discoverUntrackedVolumes(dir, nil); len(got) != 0 {
		t.Fatalf("没有 volumes 段时应无未追踪卷，实际 %v", got)
	}
}

// TestDiscoverUntrackedVolumesMissingFile 验证配置缺失时安全返回。
func TestDiscoverUntrackedVolumesMissingFile(t *testing.T) {
	m := NewManager(t.TempDir(), "")
	if got := m.discoverUntrackedVolumes(t.TempDir(), nil); len(got) != 0 {
		t.Fatalf("配置文件不存在时应安全返回空，实际 %v", got)
	}
}

// TestDiscoverUntrackedVolumesExternalLongForm 验证 external 的映射写法。
//
// 两种等价写法都要认：
//
//	external: true
//	external:
//	  name: legacy-vol
func TestDiscoverUntrackedVolumesExternalLongForm(t *testing.T) {
	dir := t.TempDir()
	cfg := `services:
  web:
    image: nginx
volumes:
  legacy:
    external:
      name: legacy-vol
`
	if err := os.WriteFile(filepath.Join(dir, composeFileName), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(t.TempDir(), "")
	got := m.discoverUntrackedVolumes(dir, nil)
	if len(got) != 1 || !strings.Contains(got[0], "legacy") {
		t.Fatalf("应识别出 external 的 legacy 卷，实际 %v", got)
	}
}

// TestDiscoverUntrackedVolumesMalformedConfig 验证坏 YAML 不会导致 panic。
func TestDiscoverUntrackedVolumesMalformedConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, composeFileName), []byte("\t\tbroken: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(t.TempDir(), "")
	// 不 panic 即通过；返回什么不重要，调用方会把它当成"没有额外信息"。
	_ = m.discoverUntrackedVolumes(dir, nil)
}

// ---- 四、防回归：备份不得包含历史备份 ----

// TestBackupDoesNotNestHistoricalBackups 是**防回归**断言，不是 bug 修复。
//
// ============================ 这条测试钉住一个不变量 ============================
// 备份产物落在 <data_dir>/backups/<name>/<时间戳>/，与 <data_dir>/stacks/ 平级，
// 而 copyProjectFiles 只读 stacks/<name>/ 顶层——因此历史备份**物理上**
// 不会被卷进来（已实测：连做三次备份，体积与文件数完全一致）。
//
// 但这是个极易被"顺手"破坏的不变量：
//   - 有人把备份目录挪进项目目录（看起来更"聚合"）；
//   - 有人给 copyProjectFiles 加递归（"顺便把构建上下文也备上"）。
//
// 一旦破坏，备份体积会按次数翻倍，而且**要等用户磁盘爆了才发现**。
// 这条测试的成本几乎为零，价值在于让那种改动当场失败。
func TestBackupDoesNotNestHistoricalBackups(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, "")
	if _, _, err := m.ws.CommitProject("nest",
		"services:\n  web:\n    image: nginx:latest\n", "A=1\n"); err != nil {
		t.Fatal(err)
	}

	projectDir, err := m.ws.ProjectDir("nest")
	if err != nil {
		t.Fatal(err)
	}
	// 前提断言：备份根目录必须在项目目录之外。
	// 若将来有人挪了位置，这里先失败，比等体积翻倍更容易定位。
	rel, rerr := filepath.Rel(projectDir, m.ws.backupsDir())
	if rerr == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("备份根目录不应位于项目目录内（rel=%q）", rel)
	}

	// 连做三次不带卷的备份（文件复制路径，无需 docker）。
	var sizes []int64
	var counts []int
	for i := 0; i < 3; i++ {
		res, err := backupFilesOnly(m, "nest", i)
		if err != nil {
			t.Fatalf("第 %d 次备份失败: %v", i+1, err)
		}
		sizes = append(sizes, res.size)
		counts = append(counts, len(res.files))
		// 备份里绝不能出现 backups 相关的路径。
		for _, f := range res.files {
			if strings.Contains(f, "backups") {
				t.Fatalf("第 %d 次备份包含了历史备份路径 %q，产物: %v", i+1, f, res.files)
			}
		}
	}

	for i := 1; i < len(sizes); i++ {
		if sizes[i] != sizes[0] {
			t.Fatalf("备份体积随次数增长：第 1 次 %d 字节，第 %d 次 %d 字节（说明历史备份被卷进来了）",
				sizes[0], i+1, sizes[i])
		}
		if counts[i] != counts[0] {
			t.Fatalf("备份文件数随次数增长：第 1 次 %d 个，第 %d 次 %d 个",
				counts[0], i+1, counts[i])
		}
	}
}

// fileSnapshot 是一次"只复制文件"的备份结果，供防回归测试使用。
type fileSnapshot struct {
	files []string
	size  int64
}

// backupFilesOnly 只走备份的**文件复制**部分，不碰 docker。
//
// 为什么不直接调 Backup(includeVolumes=false)：那条路径会经过 observer、
// manifest 等逻辑，虽然 Windows 上没 docker 也能跑（不传卷就不调 docker），
// 但会引入更多无关失败点。这条测试只关心"复制了哪些文件"，
// 所以只调用复制那一步，让它失败时能一眼看出是复制逻辑的问题。
func backupFilesOnly(m *Manager, name string, seq int) (fileSnapshot, error) {
	projectDir, err := m.ws.ProjectDir(name)
	if err != nil {
		return fileSnapshot{}, err
	}
	// 每次用不同目录名，模拟"多次备份产生的多个时间戳目录"并存的情况——
	// 这正是原任务卡描述的现场：如果复制会碰到历史备份，这个布局能测出来。
	dest := filepath.Join(m.ws.backupsDir(), name, "snap-"+strconv.Itoa(seq))
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fileSnapshot{}, err
	}
	copied, err := copyProjectFiles(projectDir, dest)
	if err != nil {
		return fileSnapshot{}, err
	}
	files, size, err := collectFiles(dest)
	if err != nil {
		return fileSnapshot{}, err
	}
	if len(files) != len(copied) {
		files = copied
	}
	return fileSnapshot{files: files, size: size}, nil
}
