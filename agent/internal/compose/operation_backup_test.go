package compose

// operation_backup_test.go：backup 作为异步任务时的可观测性与"不许误报成功"。
//
// ============================ 为什么单独测 backup ============================
//
// backup 没有"一条贯穿全程的命令"：它是 复制文件 → 枚举卷 → docker run tar
// → 写 manifest 的组合。因此它的事件流必须靠 stage 表达，
// 而"卷备份失败"这一最危险的误报点，也只能在这一层验证。

import (
	"runtime"
	"strings"
	"testing"
)

// TestOperationBackupEmitsStages 验证 backup 全程都有可观测的阶段。
//
// 用户点的"备份"可能在几分钟里毫无反馈；没有 stage 事件时，
// 界面只能显示一个转圈，用户无法判断到底卡在哪一步。
func TestOperationBackupEmitsStages(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	script := writeScript(t, "echo ok\n")
	_, p := newPullTestManager(t, script)
	if _, _, err := p.mgr.ws.CommitProject("demo",
		"services:\n  web:\n    image: nginx\n", "A=1\n"); err != nil {
		t.Fatal(err)
	}

	snap, err := p.StartOperation(OperationSpec{Action: ActionBackup}, "demo", "backup-stage-req")
	if err != nil {
		t.Fatal(err)
	}
	final := waitTerminal(t, p, snap.TaskID)

	if final.Status != PullSucceeded {
		t.Fatalf("备份应成功，实际 %q（%s）", final.Status, final.Error)
	}
	stages := streamEvents(final, "stage")
	if len(stages) < 3 {
		t.Fatalf("备份应上报多个阶段，实际 %d: %v", len(stages), stages)
	}
	joined := strings.Join(stages, "\n")
	for _, want := range []string{"创建备份目录", "复制编排文件", "写入备份说明"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("阶段里应包含 %q，实际: %s", want, joined)
		}
	}
}

// TestOperationBackupPathIsRelative 验证备份路径只回相对路径。
//
// ============================ 这是安全断言 ============================
// 绝对路径会暴露 Agent 的磁盘布局（用户目录、挂载点命名等），
// 而用户真正需要的信息只是"备份放在哪儿"。
func TestOperationBackupPathIsRelative(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	script := writeScript(t, "echo ok\n")
	_, p := newPullTestManager(t, script)
	if _, _, err := p.mgr.ws.CommitProject("demo",
		"services:\n  web:\n    image: nginx\n", ""); err != nil {
		t.Fatal(err)
	}

	snap, err := p.StartOperation(OperationSpec{Action: ActionBackup}, "demo", "backup-rel-req")
	if err != nil {
		t.Fatal(err)
	}
	final := waitTerminal(t, p, snap.TaskID)

	if final.BackupPath == "" {
		t.Fatal("备份成功后必须回备份路径（否则界面不知道备份在哪）")
	}
	if strings.HasPrefix(final.BackupPath, "/") || strings.Contains(final.BackupPath, ":\\") {
		t.Fatalf("备份路径必须是相对路径，实际 %q", final.BackupPath)
	}
	if !strings.HasPrefix(final.BackupPath, "backups/") {
		t.Fatalf("备份路径应以 backups/ 开头，实际 %q", final.BackupPath)
	}
}

// TestOperationBackupWithoutVolumesStaysSuccess 验证不备份卷时正常成功。
func TestOperationBackupWithoutVolumesStaysSuccess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	script := writeScript(t, "echo ok\n")
	_, p := newPullTestManager(t, script)
	if _, _, err := p.mgr.ws.CommitProject("demo",
		"services:\n  web:\n    image: nginx\n", ""); err != nil {
		t.Fatal(err)
	}

	snap, err := p.StartOperation(
		OperationSpec{Action: ActionBackup, IncludeVolumes: false}, "demo", "backup-novol-req")
	if err != nil {
		t.Fatal(err)
	}
	final := waitTerminal(t, p, snap.TaskID)
	if final.Status != PullSucceeded {
		t.Fatalf("不含卷的备份应成功，实际 %q（%s）", final.Status, final.Error)
	}
	joined := joinEvents(final.Output)
	if !strings.Contains(joined, "未包含数据卷") {
		t.Fatalf("应说明未包含数据卷，实际输出: %s", joined)
	}
}

// TestOperationBackupMissingProjectFails 验证不存在的项目直接报错。
func TestOperationBackupMissingProjectFails(t *testing.T) {
	script := writeScript(t, "echo ok\n")
	_, p := newPullTestManager(t, script)
	if _, err := p.StartOperation(
		OperationSpec{Action: ActionBackup}, "no-such-project", "backup-missing-req"); err == nil {
		t.Fatal("不存在的项目应被拒绝")
	}
}
