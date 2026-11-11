package stackmigrate

// manager_test.go 覆盖 7 阶段编排的全部关键路径。
//
// ============================ 用内存替身，不碰真实服务器 ============================
// fakeBackend 只记录"谁被调用了、按什么顺序"，并可按需注入失败。
// 这样测的是**编排逻辑本身**（阶段顺序、失败报告、include_volumes 的分支），
// 而这些恰好是最容易出错、也最难在真实环境里复现的部分。
//
// 关键断言方式：对"不该发生的事"断言**调用次数为 0**，
// 而不是只看最终状态。比如"未勾选数据卷时不得触发卷导出"，
// 若只断言结果文案，一个偷偷调了导出的实现也能通过。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBackend 是内存替身。
type fakeBackend struct {
	mu sync.Mutex

	// 配置。
	online    map[string]bool
	manage    map[string]bool // agentID → 是否有权限
	exists    map[string]bool // "agent/stack" → 是否已存在
	busy      map[string]bool // "agent/stack" → 是否有编排操作在跑
	config    string
	env       string
	hash      string
	images    []string
	volumeDef VolumePlan

	// 注入的错误。
	readErr    error
	imagesErr  error
	volumesErr error
	importErr  error
	startErr   error
	copyErr    map[string]error // 按引用/卷名注入
	manageErr  error
	existsErr  error
	busyErr    error

	// 调用记录。
	calls []string
	// volumesOfCalls 单独计数：**未勾选卷时它必须为 0 次导出**，
	// 但允许 1 次只读解析（用于告知"跳过了几个卷"）。
	copyVolumeCalls int
	copyImageCalls  int
	importCalls     int
	startCalls      int
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		online:    map[string]bool{"a": true, "b": true},
		manage:    map[string]bool{"a/demo": true, "b/demo": true},
		exists:    map[string]bool{},
		busy:      map[string]bool{},
		config:    "services:\n  web:\n    image: nginx\nvolumes:\n  data:\n",
		env:       "",
		hash:      "0123456789abcdef",
		images:    []string{"nginx:latest"},
		volumeDef: VolumePlan{Volumes: []string{"demo_data"}, BindDirs: 0},
		copyErr:   map[string]error{},
	}
}

func (f *fakeBackend) record(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeBackend) CanManage(ctx context.Context, agent, stack string) (bool, error) {
	f.record("canmanage:" + agent)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.manageErr != nil {
		return false, f.manageErr
	}
	return f.manage[agent+"/"+stack], nil
}

func (f *fakeBackend) AgentOnline(agent string) bool {
	f.record("online:" + agent)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.online[agent]
}

func (f *fakeBackend) StackExists(ctx context.Context, agent, stack string) (bool, error) {
	f.record("exists:" + agent)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return f.exists[agent+"/"+stack], nil
}

func (f *fakeBackend) StackBusy(ctx context.Context, agent, stack string) (bool, error) {
	f.record("busy:" + agent)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.busyErr != nil {
		return false, f.busyErr
	}
	return f.busy[agent+"/"+stack], nil
}

func (f *fakeBackend) ReadCompose(ctx context.Context, agent, stack string) (string, string, string, error) {
	f.record("read:" + agent)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return "", "", "", f.readErr
	}
	return f.config, f.env, f.hash, nil
}

func (f *fakeBackend) ImagesOf(ctx context.Context, agent, stack string) ([]string, error) {
	f.record("images:" + agent)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.imagesErr != nil {
		return nil, f.imagesErr
	}
	return f.images, nil
}

func (f *fakeBackend) CopyImage(ctx context.Context, source, target, ref string, progress func(done, total int64)) error {
	f.record("copyimage:" + ref)
	f.mu.Lock()
	f.copyImageCalls++
	err := f.copyErr[ref]
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if progress != nil {
		progress(50, 100)
		progress(100, 100)
	}
	return nil
}

func (f *fakeBackend) VolumesOf(ctx context.Context, agent, stack string) (VolumePlan, error) {
	f.record("volumes:" + agent)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.volumesErr != nil {
		return VolumePlan{}, f.volumesErr
	}
	return f.volumeDef, nil
}

func (f *fakeBackend) CopyVolume(ctx context.Context, source, target, vol string, progress func(done, total int64)) error {
	f.record("copyvolume:" + vol)
	f.mu.Lock()
	f.copyVolumeCalls++
	err := f.copyErr[vol]
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if progress != nil {
		progress(100, 100)
	}
	return nil
}

func (f *fakeBackend) ImportCompose(ctx context.Context, agent, stack, config, env, hash string) error {
	f.record("import:" + agent)
	f.mu.Lock()
	f.importCalls++
	err := f.importErr
	f.mu.Unlock()
	return err
}

func (f *fakeBackend) StartStack(ctx context.Context, agent, stack string) error {
	f.record("start:" + agent)
	f.mu.Lock()
	f.startCalls++
	err := f.startErr
	f.mu.Unlock()
	return err
}

// callCount 按前缀统计调用次数。
func (f *fakeBackend) callCount(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// waitTask 等任务进入终态（或超时）。
func waitTask(t *testing.T, m *Manager, id string) Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snap, err := m.Get(id, 0)
		if err != nil {
			t.Fatalf("读取任务失败: %v", err)
		}
		if terminal(snap.Status) {
			return snap
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("任务未在 5 秒内结束")
	return Snapshot{}
}

// text 把全部事件拼成一段文本（便于断言内容）。
func text(s Snapshot) string {
	var b strings.Builder
	for _, ev := range s.Events {
		b.WriteString(ev.Text)
		b.WriteString("\n")
	}
	return b.String()
}

// submit 提交一次迁移并等它结束。
func submit(t *testing.T, fb *fakeBackend, opts Options) Snapshot {
	t.Helper()
	m := New(fb)
	snap, err := m.Submit(context.Background(), opts)
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	return waitTask(t, m, snap.TaskID)
}

// baseOpts 返回一份默认参数（含卷、不启动）。
func baseOpts() Options {
	return Options{
		StackName: "demo", Source: "a", Target: "b",
		RequestID: "req-1",
	}
}

// TestMigrateHappyPath 验证完整成功的 7 阶段编排。
func TestMigrateHappyPath(t *testing.T) {
	fb := newFakeBackend()
	opts := baseOpts()
	opts.IncludeVolumes = true
	opts.StartAfter = true

	snap := submit(t, fb, opts)
	if snap.Status != StatusSucceeded {
		t.Fatalf("应成功，实际 %s：%s", snap.Status, snap.Error)
	}
	// 阶段全部走到。
	for _, stage := range []string{StageValidate, StageReadConfig, StagePlan,
		StageImages, StageVolumes, StageImportConf, StageStart} {
		if !strings.Contains(text(snap), StageTitle(stage)) {
			t.Fatalf("事件中应包含阶段「%s」，实际:\n%s", StageTitle(stage), text(snap))
		}
	}
	// 关键动作都执行了。
	if fb.copyImageCalls != 1 || fb.copyVolumeCalls != 1 {
		t.Fatalf("应各搬 1 个镜像与 1 个卷，实际镜像 %d 卷 %d", fb.copyImageCalls, fb.copyVolumeCalls)
	}
	if fb.importCalls != 1 || fb.startCalls != 1 {
		t.Fatalf("应写入并启动，实际 import=%d start=%d", fb.importCalls, fb.startCalls)
	}
	// 任务字段。
	if snap.Name != "demo" {
		t.Fatalf("task.name 必须是源编排名，实际 %q", snap.Name)
	}
	if snap.ImageCount != 1 || snap.VolumeCount != 1 {
		t.Fatalf("统计应为 1 镜像 1 卷，实际 %d/%d", snap.ImageCount, snap.VolumeCount)
	}
	if snap.SkippedVolumes != 0 {
		t.Fatalf("勾选了卷时不应有跳过数，实际 %d", snap.SkippedVolumes)
	}
}

// TestMigrateWithoutVolumesSkipsAllVolumeExport 验证未勾选卷时**零次卷导出**。
//
// ============================ 这是本任务最重要的一条断言 ============================
// 产品决策：默认不迁数据卷。若实现里"顺手"走了导出，
// 用户会为一个他没要的功能付出几十 GB 的传输与磁盘代价，
// 而界面上看起来只是"迁移完成了"。
//
// 因此断言的是 CopyVolume 调用次数为 **0**。
func TestMigrateWithoutVolumesSkipsAllVolumeExport(t *testing.T) {
	fb := newFakeBackend()
	fb.volumeDef = VolumePlan{Volumes: []string{"demo_data", "demo_cache"}, BindDirs: 2}

	opts := baseOpts()
	opts.IncludeVolumes = false // 用户的默认选择

	snap := submit(t, fb, opts)
	if snap.Status != StatusSucceeded {
		t.Fatalf("应成功，实际 %s：%s", snap.Status, snap.Error)
	}
	// ============================ 核心断言 ============================
	if fb.copyVolumeCalls != 0 {
		t.Fatalf("未勾选数据卷时绝不能触发任何卷导出，实际 %d 次", fb.copyVolumeCalls)
	}
	// 结果必须明确告知"目标上将是空数据"以及跳过数量。
	body := text(snap)
	if !strings.Contains(body, "本次未迁移数据卷") {
		t.Fatalf("必须写明本次未迁移数据卷，实际:\n%s", body)
	}
	if !strings.Contains(body, "目标上将是空数据") {
		t.Fatalf("必须写明目标上将是空数据，实际:\n%s", body)
	}
	if !strings.Contains(body, "该项目有 2 个数据卷被跳过") {
		t.Fatalf("必须写明跳过了 2 个数据卷，实际:\n%s", body)
	}
	// bind 目录必须被统计并说明。
	if !strings.Contains(body, "已跳过宿主机绑定目录 2 个") {
		t.Fatalf("必须统计宿主机绑定目录，实际:\n%s", body)
	}
	if snap.BindDirs != 2 {
		t.Fatalf("bind 目录统计应为 2，实际 %d", snap.BindDirs)
	}
	if snap.SkippedVolumes != 2 {
		t.Fatalf("跳过卷数应为 2，实际 %d", snap.SkippedVolumes)
	}
}

// TestMigrateRejectsTargetNameConflict 验证目标同名时拒绝且**绝不覆盖**。
func TestMigrateRejectsTargetNameConflict(t *testing.T) {
	fb := newFakeBackend()
	fb.exists["b/demo"] = true // 目标已有同名编排

	snap := submit(t, fb, baseOpts())
	if snap.Status != StatusFailed {
		t.Fatalf("应失败，实际 %s", snap.Status)
	}
	if !strings.Contains(snap.Error, "已存在同名编排") {
		t.Fatalf("错误应说明同名冲突，实际 %q", snap.Error)
	}
	// 提示用户改名。
	if !strings.Contains(snap.Error, "改名") {
		t.Fatalf("应提示改名，实际 %q", snap.Error)
	}
	// ============================ 绝不能有任何写入 ============================
	if fb.importCalls != 0 {
		t.Fatalf("同名冲突时绝不能写入配置，实际 %d 次", fb.importCalls)
	}
	if fb.copyImageCalls != 0 || fb.copyVolumeCalls != 0 {
		t.Fatalf("同名冲突时不应开始搬运任何东西，实际镜像 %d 卷 %d",
			fb.copyImageCalls, fb.copyVolumeCalls)
	}
	// 校验阶段就停下。
	if snap.Stage != StageValidate {
		t.Fatalf("应停在校验阶段，实际 %s", snap.Stage)
	}
}

// TestMigrateRejectsSourceOffline 验证源端离线。
func TestMigrateRejectsSourceOffline(t *testing.T) {
	fb := newFakeBackend()
	fb.online["a"] = false

	snap := submit(t, fb, baseOpts())
	if snap.Status != StatusFailed {
		t.Fatalf("应失败，实际 %s", snap.Status)
	}
	if !strings.Contains(snap.Error, "源服务器当前离线") {
		t.Fatalf("错误应说明源端离线，实际 %q", snap.Error)
	}
	if fb.importCalls != 0 {
		t.Fatal("源端离线时不应有任何写入")
	}
}

// TestMigrateRejectsTargetOffline 验证目标端离线。
func TestMigrateRejectsTargetOffline(t *testing.T) {
	fb := newFakeBackend()
	fb.online["b"] = false

	snap := submit(t, fb, baseOpts())
	if snap.Status != StatusFailed {
		t.Fatalf("应失败，实际 %s", snap.Status)
	}
	if !strings.Contains(snap.Error, "目标服务器当前离线") {
		t.Fatalf("错误应说明目标端离线，实际 %q", snap.Error)
	}
}

// TestMigrateRejectsInsufficientPermission 验证权限不足。
//
// ============================ 权限必须如实拒绝 ============================
// 前端约定：403 是权限问题。若这里被实现成"降级继续"，
// 用户会得到一个"迁移成功"但目标上什么都没写的结果。
func TestMigrateRejectsInsufficientPermission(t *testing.T) {
	t.Run("无源端权限", func(t *testing.T) {
		fb := newFakeBackend()
		fb.manage["a/demo"] = false
		snap := submit(t, fb, baseOpts())
		if snap.Status != StatusFailed {
			t.Fatalf("应失败，实际 %s", snap.Status)
		}
		if !strings.Contains(snap.Error, "源服务器上该编排的管理权限") {
			t.Fatalf("错误应说明源端权限不足，实际 %q", snap.Error)
		}
	})
	t.Run("无目标端权限", func(t *testing.T) {
		fb := newFakeBackend()
		fb.manage["b/demo"] = false
		snap := submit(t, fb, baseOpts())
		if snap.Status != StatusFailed {
			t.Fatalf("应失败，实际 %s", snap.Status)
		}
		if !strings.Contains(snap.Error, "目标服务器的管理权限") {
			t.Fatalf("错误应说明目标端权限不足，实际 %q", snap.Error)
		}
	})
}

// TestMigrateRejectsSourceBusy 验证源端编排正在操作时拒绝。
func TestMigrateRejectsSourceBusy(t *testing.T) {
	fb := newFakeBackend()
	fb.busy["a/demo"] = true

	snap := submit(t, fb, baseOpts())
	if snap.Status != StatusFailed {
		t.Fatalf("应失败，实际 %s", snap.Status)
	}
	if !strings.Contains(snap.Error, "正在执行其它操作") {
		t.Fatalf("错误应说明编排忙，实际 %q", snap.Error)
	}
}

// TestMigrateRejectsSameServer 验证源与目标相同被拒绝。
func TestMigrateRejectsSameServer(t *testing.T) {
	fb := newFakeBackend()
	opts := baseOpts()
	opts.Target = "a"

	snap := submit(t, fb, opts)
	if snap.Status != StatusFailed {
		t.Fatalf("应失败，实际 %s", snap.Status)
	}
	if !strings.Contains(snap.Error, "不能是同一台") {
		t.Fatalf("错误应说明不能同一台，实际 %q", snap.Error)
	}
}

// TestMigrateStageOrder 验证阶段按序推进且事件序号单调。
func TestMigrateStageOrder(t *testing.T) {
	fb := newFakeBackend()
	opts := baseOpts()
	opts.IncludeVolumes = true

	snap := submit(t, fb, opts)
	if snap.Status != StatusSucceeded {
		t.Fatalf("应成功，实际 %s：%s", snap.Status, snap.Error)
	}

	// 校验调用顺序：先在线检查，再权限，再同名，再忙。
	wantOrder := []string{"online:a", "online:b", "canmanage:a", "canmanage:b",
		"exists:b", "busy:a", "read:a", "images:a"}
	fb.mu.Lock()
	calls := append([]string(nil), fb.calls...)
	fb.mu.Unlock()
	pos := 0
	for _, c := range calls {
		if pos < len(wantOrder) && c == wantOrder[pos] {
			pos++
		}
	}
	if pos != len(wantOrder) {
		t.Fatalf("前置检查顺序不符，期望依次出现 %v，实际 %v", wantOrder, calls)
	}

	// 事件序号必须从 1 开始连续递增（跳号意味着事件被丢弃）。
	if len(snap.Events) == 0 {
		t.Fatal("必须有事件")
	}
	for i, ev := range snap.Events {
		if ev.Seq != int64(i+1) {
			t.Fatalf("事件序号应连续：第 %d 条 seq=%d", i+1, ev.Seq)
		}
		if ev.Stream == "" {
			t.Fatalf("第 %d 条事件缺少 stream", i+1)
		}
	}
}

// TestMigrateFailureReportsCompletedStage 验证失败报告说明停在哪、留下什么。
//
// ============================ 这是失败处理的核心要求 ============================
// 用户必须能据此判断"重试"还是"先去目标上清理"。
func TestMigrateFailureReportsCompletedStage(t *testing.T) {
	fb := newFakeBackend()
	fb.images = []string{"nginx:latest", "redis:latest"}
	// 第二个镜像失败 → 阶段 4 中途停下，但第一个已经导入。
	fb.copyErr["redis:latest"] = errors.New("目标磁盘空间不足")

	opts := baseOpts()
	opts.IncludeVolumes = true

	snap := submit(t, fb, opts)
	if snap.Status != StatusFailed {
		t.Fatalf("应失败，实际 %s", snap.Status)
	}
	// 说明停在哪个阶段。
	if !strings.Contains(snap.Error, "停在第 4 步") {
		t.Fatalf("错误应说明停在第 4 步，实际 %q", snap.Error)
	}
	if !strings.Contains(snap.Error, StageTitle(StageImages)) {
		t.Fatalf("错误应写出阶段中文名，实际 %q", snap.Error)
	}
	// 说明原因。
	if !strings.Contains(snap.Error, "目标磁盘空间不足") {
		t.Fatalf("错误应保留原始原因，实际 %q", snap.Error)
	}
	// ============================ 必须说明目标上的残留 ============================
	if !strings.Contains(snap.Error, "已留下") {
		t.Fatalf("错误应说明目标上留下了什么，实际 %q", snap.Error)
	}
	if !strings.Contains(snap.Error, "已导入 1 个镜像") {
		t.Fatalf("应如实报告已导入 1 个镜像，实际 %q", snap.Error)
	}
	if snap.Residual.ImagesImported != 1 {
		t.Fatalf("残留统计应为 1 个镜像，实际 %d", snap.Residual.ImagesImported)
	}
	// 后续阶段绝不能执行。
	if fb.importCalls != 0 {
		t.Fatalf("失败后绝不能再写配置，实际 %d 次", fb.importCalls)
	}
	if fb.startCalls != 0 {
		t.Fatalf("失败后绝不能启动，实际 %d 次", fb.startCalls)
	}
	if fb.copyVolumeCalls != 0 {
		t.Fatalf("镜像阶段失败后不应进入卷阶段，实际 %d 次", fb.copyVolumeCalls)
	}
	// 阶段字段停在镜像阶段。
	if snap.Stage != StageImages {
		t.Fatalf("阶段应为 images，实际 %s", snap.Stage)
	}
}

// TestMigrateFailureAtVolumeStage 验证卷阶段失败的残留报告。
func TestMigrateFailureAtVolumeStage(t *testing.T) {
	fb := newFakeBackend()
	fb.volumeDef = VolumePlan{Volumes: []string{"demo_data", "demo_cache"}}
	fb.copyErr["demo_cache"] = errors.New("解包超时")

	opts := baseOpts()
	opts.IncludeVolumes = true

	snap := submit(t, fb, opts)
	if snap.Status != StatusFailed {
		t.Fatalf("应失败，实际 %s", snap.Status)
	}
	if !strings.Contains(snap.Error, "停在第 5 步") {
		t.Fatalf("应停在卷阶段（第 5 步），实际 %q", snap.Error)
	}
	// 第一个卷已恢复，镜像全部已导入——都必须如实报告。
	if snap.Residual.VolumesRestored != 1 {
		t.Fatalf("残留统计应为 1 个卷，实际 %d", snap.Residual.VolumesRestored)
	}
	if !strings.Contains(snap.Error, "已恢复 1 个数据卷") {
		t.Fatalf("应报告已恢复 1 个卷，实际 %q", snap.Error)
	}
	// 配置绝不能写。
	if fb.importCalls != 0 {
		t.Fatalf("失败后不应写配置，实际 %d 次", fb.importCalls)
	}
}

// TestMigrateStartAfterFalse 验证 start_after=false 不启动且明确记录。
func TestMigrateStartAfterFalse(t *testing.T) {
	fb := newFakeBackend()
	opts := baseOpts()
	opts.StartAfter = false

	snap := submit(t, fb, opts)
	if snap.Status != StatusSucceeded {
		t.Fatalf("应成功，实际 %s：%s", snap.Status, snap.Error)
	}
	if fb.startCalls != 0 {
		t.Fatalf("start_after=false 时绝不能启动，实际 %d 次", fb.startCalls)
	}
	// 必须明确记录"已迁移，未启动"。
	if !strings.Contains(text(snap), "已迁移，未启动") {
		t.Fatalf("必须记录已迁移未启动，实际:\n%s", text(snap))
	}
	if snap.Residual.Started {
		t.Fatal("残留不应标记为已启动")
	}
}

// TestMigrateStartAfterTrue 验证 start_after=true 会启动。
func TestMigrateStartAfterTrue(t *testing.T) {
	fb := newFakeBackend()
	opts := baseOpts()
	opts.StartAfter = true

	snap := submit(t, fb, opts)
	if snap.Status != StatusSucceeded {
		t.Fatalf("应成功，实际 %s：%s", snap.Status, snap.Error)
	}
	if fb.startCalls != 1 {
		t.Fatalf("start_after=true 时应启动一次，实际 %d 次", fb.startCalls)
	}
	if !snap.Residual.Started {
		t.Fatal("残留应标记为已启动")
	}
}

// TestMigrateStartFailureIsReported 验证启动失败也被如实报告。
func TestMigrateStartFailureIsReported(t *testing.T) {
	fb := newFakeBackend()
	fb.startErr = errors.New("端口被占用")
	opts := baseOpts()
	opts.StartAfter = true

	snap := submit(t, fb, opts)
	if snap.Status != StatusFailed {
		t.Fatalf("应失败，实际 %s", snap.Status)
	}
	if !strings.Contains(snap.Error, "启动编排失败") {
		t.Fatalf("应说明启动失败，实际 %q", snap.Error)
	}
	// 关键：配置已写入，必须如实告知（用户可去目标上手工启动）。
	if !snap.Residual.ComposeWritten {
		t.Fatal("应记录配置已写入")
	}
	if !strings.Contains(snap.Error, "已写入编排配置") {
		t.Fatalf("应告知配置已写入，实际 %q", snap.Error)
	}
}

// TestMigrateIdempotentRequest 验证同一 request_id 复用任务。
func TestMigrateIdempotentRequest(t *testing.T) {
	fb := newFakeBackend()
	m := New(fb)
	opts := baseOpts()

	first, err := m.Submit(context.Background(), opts)
	if err != nil {
		t.Fatalf("首次提交失败: %v", err)
	}
	waitTask(t, m, first.TaskID)

	second, err := m.Submit(context.Background(), opts)
	if err != nil {
		t.Fatalf("重复提交应复用而不是报错: %v", err)
	}
	if second.TaskID != first.TaskID {
		t.Fatalf("同 request_id 应返回同一任务：%q vs %q", second.TaskID, first.TaskID)
	}
	// 只执行了一次。
	if fb.importCalls != 1 {
		t.Fatalf("幂等复用不应重复执行，实际写入 %d 次", fb.importCalls)
	}
}

// TestMigrateRequestMismatchConflicts 验证同 request_id 不同参数报冲突。
func TestMigrateRequestMismatchConflicts(t *testing.T) {
	fb := newFakeBackend()
	m := New(fb)
	opts := baseOpts()
	first, err := m.Submit(context.Background(), opts)
	if err != nil {
		t.Fatalf("首次提交失败: %v", err)
	}
	waitTask(t, m, first.TaskID)

	// 改参数但同 request_id：用户实际提交的是另一次迁移。
	opts.IncludeVolumes = true
	if _, err := m.Submit(context.Background(), opts); !errors.Is(err, ErrDuplicateRequest) {
		t.Fatalf("参数变化应返回 ErrDuplicateRequest，实际 %v", err)
	}
}

// TestMigratePairBusy 验证同一对服务器只允许一个迁移。
//
// ============================ 这里必须有确定性同步点 ============================
// 早先的实现靠"轮询直到 CopyImage 被调用"来等第一个任务进入阻塞，
// 但那是**先检查后等待**：在 CopyImage 记录调用与真正 blockingBackend
// 阻塞之间有一小段窗口，而且 fakeBackend 的 images 列表是共享可变状态。
// 全量跑（其它测试干扰调度）时它偶发失败——这类不稳定测试比没有测试更糟，
// 因为它会让人开始怀疑产品代码。
//
// 改为用 channel 做**双向握手**：CopyImage 进入后先发信号，
// 测试收到信号才去提交第二个任务。这样"第一个任务确实还在跑"是
// 由阻塞本身保证的，而不是由时间窗口猜的。
func TestMigratePairBusy(t *testing.T) {
	fb := newFakeBackend()
	fb.images = []string{"slow:latest"}

	entered := make(chan struct{}, 1)
	release := make(chan struct{})

	bb := &blockingBackend{fakeBackend: fb, entered: entered, release: release}
	m := New(bb)

	first, err := m.Submit(context.Background(), baseOpts())
	if err != nil {
		t.Fatalf("首次提交失败: %v", err)
	}

	// 等它确实进入 CopyImage（即已经处于 running 且占着组合位）。
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("第一个迁移未进入镜像阶段")
	}

	// 同一对服务器的第二个迁移必须被拒。
	opts2 := baseOpts()
	opts2.RequestID = "req-2"
	if _, err := m.Submit(context.Background(), opts2); !errors.Is(err, ErrPairBusy) {
		t.Fatalf("同服务器组合应返回 ErrPairBusy，实际 %v", err)
	}

	// 放行第一个，等它结束。
	close(release)
	waitTask(t, m, first.TaskID)

	// 前一个结束后可以再提交（占位必须被释放，否则功能会永久卡死）。
	// 第二次阻塞用一个新的闸门：release 已经关闭，复用它会让
	// blockingBackend 直接返回，"任务还在跑"这个前提就不成立了。
	release2 := make(chan struct{})
	bb.mu.Lock()
	bb.release = release2
	bb.mu.Unlock()

	opts3 := baseOpts()
	opts3.RequestID = "req-3"
	third, err := m.Submit(context.Background(), opts3)
	if err != nil {
		t.Fatalf("前一个结束后应可再次提交: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("第二个迁移未进入镜像阶段")
	}
	close(release2)
	waitTask(t, m, third.TaskID)
}

// blockingBackend 让 CopyImage 阻塞，用于验证并发约束。
type blockingBackend struct {
	*fakeBackend
	mu      sync.Mutex
	entered chan struct{}
	release chan struct{}
}

func (b *blockingBackend) CopyImage(ctx context.Context, source, target, ref string,
	progress func(done, total int64)) error {
	b.fakeBackend.record("copyimage:" + ref)
	b.fakeBackend.mu.Lock()
	b.fakeBackend.copyImageCalls++
	b.fakeBackend.mu.Unlock()

	b.mu.Lock()
	gate := b.release
	entered := b.entered
	b.mu.Unlock()

	// 通知测试"我已经进来了"，然后等放行。
	select {
	case entered <- struct{}{}:
	default:
	}
	<-gate
	return nil
}

// TestMigrateGetIncremental 验证增量读取只看 after 之后的事件。
func TestMigrateGetIncremental(t *testing.T) {
	fb := newFakeBackend()
	m := New(fb)
	snap, err := m.Submit(context.Background(), baseOpts())
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	final := waitTask(t, m, snap.TaskID)

	// 全量读。
	all, _ := m.Get(snap.TaskID, 0)
	if len(all.Events) != len(final.Events) {
		// final 是终态快照，二者应一致（事件不再增长）。
		t.Fatalf("终态后事件数应稳定：%d vs %d", len(all.Events), len(final.Events))
	}
	if all.NextCursor != int64(len(all.Events)) {
		t.Fatalf("NextCursor 应等于最后一条的 seq，实际 %d", all.NextCursor)
	}

	// 从中间读。
	mid := int64(len(all.Events) / 2)
	part, _ := m.Get(snap.TaskID, mid)
	if len(part.Events) != len(all.Events)-int(mid) {
		t.Fatalf("增量应返回 %d 条，实际 %d", len(all.Events)-int(mid), len(part.Events))
	}
	if len(part.Events) > 0 && part.Events[0].Seq != mid+1 {
		t.Fatalf("增量首条 seq 应为 %d，实际 %d", mid+1, part.Events[0].Seq)
	}
}

// TestMigrateUnknownTask 验证未知任务标识。
func TestMigrateUnknownTask(t *testing.T) {
	m := New(newFakeBackend())
	if _, err := m.Get("deadbeefdeadbeefdeadbeefdeadbeef", 0); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("未知任务应返回 ErrTaskNotFound，实际 %v", err)
	}
}

// TestMigrateReadConfigFailure 验证读配置失败。
func TestMigrateReadConfigFailure(t *testing.T) {
	fb := newFakeBackend()
	fb.readErr = errors.New("工作区读取失败")

	snap := submit(t, fb, baseOpts())
	if snap.Status != StatusFailed {
		t.Fatalf("应失败，实际 %s", snap.Status)
	}
	if !strings.Contains(snap.Error, "停在第 2 步") {
		t.Fatalf("应停在第 2 步，实际 %q", snap.Error)
	}
	if !strings.Contains(snap.Error, "未留下任何改动") {
		t.Fatalf("此时目标上无残留，应如实说明，实际 %q", snap.Error)
	}
}

// TestMigrateEmptyConfig 验证源端配置为空被拒绝。
//
// 空配置若被"成功"迁移过去，目标端会得到一个空编排，
// 而用户以为数据和服务都搬过去了。
func TestMigrateEmptyConfig(t *testing.T) {
	fb := newFakeBackend()
	fb.config = "   "

	snap := submit(t, fb, baseOpts())
	if snap.Status != StatusFailed {
		t.Fatalf("应失败，实际 %s", snap.Status)
	}
	if !strings.Contains(snap.Error, "配置为空") {
		t.Fatalf("应说明配置为空，实际 %q", snap.Error)
	}
}

// TestMigrateImageProgressNotFlooding 验证进度事件不会淹没日志。
//
// 逐字节进度会让事件缓冲瞬间被挤爆，把真正有用的行挤掉。
func TestMigrateImageProgressNotFlooding(t *testing.T) {
	fb := newFakeBackend()
	fb.images = []string{"big:latest"}

	m := New(&floodBackend{fakeBackend: fb})
	snap, err := m.Submit(context.Background(), baseOpts())
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	final := waitTask(t, m, snap.TaskID)
	if len(final.Events) > 40 {
		t.Fatalf("即使上报 1000 次进度，事件也不应超过 40 条，实际 %d", len(final.Events))
	}
	if final.Truncated {
		t.Fatal("不应触发缓冲区截断")
	}
}

// floodBackend 上报极多进度回调。
type floodBackend struct {
	*fakeBackend
}

func (f *floodBackend) CopyImage(ctx context.Context, source, target, ref string,
	progress func(done, total int64)) error {
	f.fakeBackend.record("copyimage:" + ref)
	if progress != nil {
		for i := int64(0); i <= 10000; i++ {
			progress(i, 10000)
		}
	}
	return nil
}

// TestMigrateVolumesWhenUserOptsIn 验证勾选时确实迁卷。
func TestMigrateVolumesWhenUserOptsIn(t *testing.T) {
	fb := newFakeBackend()
	fb.volumeDef = VolumePlan{Volumes: []string{"demo_data", "demo_cache"}}
	opts := baseOpts()
	opts.IncludeVolumes = true

	snap := submit(t, fb, opts)
	if snap.Status != StatusSucceeded {
		t.Fatalf("应成功，实际 %s：%s", snap.Status, snap.Error)
	}
	if fb.copyVolumeCalls != 2 {
		t.Fatalf("应迁 2 个卷，实际 %d", fb.copyVolumeCalls)
	}
	body := text(snap)
	if !strings.Contains(body, "需要迁移的数据卷 2 个") {
		t.Fatalf("应列出要迁的卷，实际:\n%s", body)
	}
	// 勾选时不应出现"未迁移数据卷"的文案。
	if strings.Contains(body, "本次未迁移数据卷") {
		t.Fatalf("勾选时不应说未迁移，实际:\n%s", body)
	}
}

// TestMigrateExternalVolumeWarned 验证外部卷给出提示。
func TestMigrateExternalVolumeWarned(t *testing.T) {
	fb := newFakeBackend()
	fb.volumeDef = VolumePlan{Volumes: []string{"shared"}, External: []string{"shared"}}
	opts := baseOpts()
	opts.IncludeVolumes = true

	snap := submit(t, fb, opts)
	if snap.Status != StatusSucceeded {
		t.Fatalf("应成功，实际 %s：%s", snap.Status, snap.Error)
	}
	if !strings.Contains(text(snap), "外部卷") {
		t.Fatalf("应提示外部卷是副本，实际:\n%s", text(snap))
	}
}

// TestMigrateNoImages 验证无镜像的编排正常迁移。
func TestMigrateNoImages(t *testing.T) {
	fb := newFakeBackend()
	fb.images = nil

	snap := submit(t, fb, baseOpts())
	if snap.Status != StatusSucceeded {
		t.Fatalf("应成功，实际 %s：%s", snap.Status, snap.Error)
	}
	if fb.importCalls != 1 {
		t.Fatalf("仍应写入配置，实际 %d 次", fb.importCalls)
	}
}

// TestMigrateVolumesParseFailureNotFatal 验证未勾选时卷解析失败不阻断。
//
// 用户没要求迁卷，卷信息只是"附带告知"。
// 若因此让整个迁移失败，反而违背了用户的意图。
func TestMigrateVolumesParseFailureNotFatal(t *testing.T) {
	fb := newFakeBackend()
	fb.volumesErr = errors.New("配置解析失败")

	snap := submit(t, fb, baseOpts()) // IncludeVolumes=false
	if snap.Status != StatusSucceeded {
		t.Fatalf("未勾选卷时解析失败不应阻断迁移，实际 %s：%s", snap.Status, snap.Error)
	}
	// 必须告知"拿不到卷数量"，而不是显示 0。
	if !strings.Contains(text(snap), "无法解析该编排的数据卷清单") {
		t.Fatalf("应告知无法解析卷清单，实际:\n%s", text(snap))
	}
}

// TestMigrateVolumesParseFailureFatalWhenOptedIn 验证勾选时解析失败必须阻断。
func TestMigrateVolumesParseFailureFatalWhenOptedIn(t *testing.T) {
	fb := newFakeBackend()
	fb.volumesErr = errors.New("配置解析失败")
	opts := baseOpts()
	opts.IncludeVolumes = true

	snap := submit(t, fb, opts)
	if snap.Status != StatusFailed {
		t.Fatalf("勾选卷时解析失败必须阻断，实际 %s", snap.Status)
	}
	if fb.copyImageCalls != 0 {
		t.Fatal("清单解析失败时不应开始搬镜像（否则会搬了一半才发现没卷）")
	}
}

// TestStageHelpers 验证阶段序号与标题。
func TestStageHelpers(t *testing.T) {
	if StageIndex(StageValidate) != 1 {
		t.Fatalf("validate 应为第 1 步，实际 %d", StageIndex(StageValidate))
	}
	if StageIndex(StageStart) != 7 {
		t.Fatalf("start 应为第 7 步，实际 %d", StageIndex(StageStart))
	}
	if StageIndex("unknown") != 0 {
		t.Fatalf("未知阶段应为 0，实际 %d", StageIndex("unknown"))
	}
	for _, s := range StageOrder {
		if StageTitle(s) == "未知阶段" {
			t.Fatalf("阶段 %s 缺少中文名", s)
		}
	}
}

// TestResidualDescribe 验证残留描述。
func TestResidualDescribe(t *testing.T) {
	if got := (Residual{}).Describe(); !strings.Contains(got, "未留下任何改动") {
		t.Fatalf("空残留应说明无改动，实际 %q", got)
	}
	r := Residual{ImagesImported: 2, VolumesRestored: 1, ComposeWritten: true, Started: true}
	got := r.Describe()
	for _, want := range []string{"已导入 2 个镜像", "已恢复 1 个数据卷", "已写入编排配置", "已在目标上启动"} {
		if !strings.Contains(got, want) {
			t.Fatalf("残留描述应包含 %q，实际 %q", want, got)
		}
	}
}
