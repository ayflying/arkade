package compose

// backup.go 实现编排备份。
//
// 备份产物留在 Agent 本机固定目录，**不回传二进制内容**：
// 编排文件只有几 KB，但卷数据可能是几百 MB，
// 通过 WebSocket 传输会拖垮整条连接的写队列（这也正是协议注释里写明的原因）。
//
// 目录结构：
//
//	<data_dir>/backups/<name>/<时间戳>/
//	    compose.yml        编排文件副本
//	    manifest.txt       本次备份的说明（时间、来源、包含哪些卷）
//	    volume-<卷名>.tar  每个卷一个 tar（仅在 IncludeVolumes 时）
//	    .env / 其他文件     编排目录里的非 compose.yml 文件（尽量带上）

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// backupTimestampLayout 是备份时间戳格式。
//
// 用 "20060102-150405" 而不是 Unix 时间戳：备份目录是要被人直接 ls 的，
// 可读的时间戳让运维一眼看出哪份是几点打的。加了短横线是为了排序仍然正确。
const backupTimestampLayout = "20060102-150405"

// backupManifestFile 是备份说明文件名。
const backupManifestFile = "manifest.txt"

// BackupResult 是备份结果。
type BackupResult struct {
	Name      string
	Path      string
	SizeBytes int64
	Files     []string
	CreatedAt int64

	// VolumeFailed 表示"卷备份失败，但编排文件已备份成功"。
	//
	// ============================ 为什么要这个字段 ============================
	// 原实现只把失败写进 manifest 就返回成功。异步任务据此把 status 置为
	// succeeded，UI 于是显示"备份完成"——而用户真正在意的数据卷其实没备份。
	// 这不是"少一个提示"，是**误报成功**。因此必须让调用方能区分。
	VolumeFailed bool
	// VolumeError 是卷备份失败的中文说明（不含路径细节中的敏感信息）。
	VolumeError string
}

// Backup 备份编排文件与（可选的）数据卷。
//
// 卷备份的做法：为每个卷起一个临时容器把卷内容 tar 到备份目录。
// 为什么用容器而不是直接读 /var/lib/docker/volumes：
//   - 直接读宿主机路径需要知道 Docker 的 root-dir（可能被改过）；
//   - 卷里的文件属主/权限/硬链接在 tar 里才保得住，容器内 tar 是官方推荐做法；
//   - 更重要的是它不依赖 Agent 自己能访问宿主机文件系统——
//     Agent 本身跑在容器里，只有 docker.sock，没有宿主机文件系统。
//
// 卷名从 compose 配置解析不出来（volumes 段的语义太复杂：可能是宿主机路径、
// 可能是外部卷、可能是匿名卷），因此这里按 compose 的命名约定推导：
// 未声明 name 时卷名是 <project>_<volumeKey>。推导不出的卷会被记入 manifest 说明，
// 而不是静默漏掉。
func (m *Manager) Backup(ctx context.Context, name string, includeVolumes bool) (BackupResult, error) {
	n, err := cleanName(name)
	if err != nil {
		return BackupResult{}, err
	}
	if err := m.requireExists(n); err != nil {
		return BackupResult{}, err
	}

	projectDir, err := m.ws.ProjectDir(n)
	if err != nil {
		return BackupResult{}, err
	}

	now := time.Now().UTC()
	dest := filepath.Join(m.ws.backupsDir(), n, now.Format(backupTimestampLayout))

	// ============================ 阶段上报 ============================
	// 备份没有"一条贯穿全程的命令"，因此每个阶段都如实上报。
	// 只有真正的 docker 调用才报 command；文件复制只报 stage，
	// 编造一条假命令会让用户复制去执行。
	ob := observerFrom(ctx)
	ob.stage("创建备份目录", nil)

	if err := os.MkdirAll(dest, 0o755); err != nil {
		return BackupResult{}, fmt.Errorf("创建备份目录失败: %w", err)
	}

	manifest := make([]string, 0, 8)
	manifest = append(manifest,
		fmt.Sprintf("编排项目: %s", n),
		fmt.Sprintf("备份时间: %s", now.Format(time.RFC3339)),
		fmt.Sprintf("来源目录: %s", projectDir),
	)

	// 复制编排目录下的文件。
	// compose.yml 是必需的，其余（.env、override 文件、build 上下文里的小文件）
	// 能带就带——漏了 .env 的备份是没法直接恢复的。
	ob.stage("复制编排文件到备份目录", nil)
	copied, copyErr := copyProjectFiles(projectDir, dest)
	if copyErr != nil {
		// 复制失败要清理掉半截备份，否则下次按时间戳找不到完整备份会误导人。
		_ = os.RemoveAll(dest)
		return BackupResult{}, copyErr
	}
	ob.stage(fmt.Sprintf("已复制 %d 个编排文件", len(copied)), nil)
	manifest = append(manifest, "已复制文件: "+strings.Join(copied, ", "))

	// volumeFailed 标记卷备份是否部分/全部失败。
	//
	// ============================ 为什么不能只写警告 ============================
	// 原实现把失败写进 manifest 后**返回成功**。对同步接口尚可，
	// 但异步任务的 status 会被 UI 当成"备份已完成"——
	// 用户以为拿到了完整备份（含数据卷），实际卷根本没备份上。
	// 因此这里额外把失败信号带出去，由任务层置 failed。
	volumeFailed := false
	if includeVolumes {
		ob.stage("枚举项目数据卷", nil)
		vols, verr := m.backupVolumes(ctx, n, dest)
		if verr != nil {
			// 卷备份失败不回滚：编排文件已经安全备份了。
			volumeFailed = true
			manifest = append(manifest, "卷备份失败: "+verr.Error())
			ob.stage("卷备份失败（编排文件已备份）", nil)
		} else if len(vols) == 0 {
			manifest = append(manifest, "未发现可备份的命名卷（可能是匿名卷或全部为宿主机路径挂载）")
			ob.stage("未发现可备份的命名卷", nil)
		} else {
			manifest = append(manifest, "已备份卷: "+strings.Join(vols, ", "))
			ob.stage(fmt.Sprintf("已备份 %d 个数据卷", len(vols)), nil)
		}
	} else {
		manifest = append(manifest, "未包含数据卷（调用方未要求）")
		ob.stage("未包含数据卷（调用方未要求）", nil)
	}

	ob.stage("写入备份说明", nil)
	manifestPath := filepath.Join(dest, backupManifestFile)
	if err := os.WriteFile(manifestPath, []byte(strings.Join(manifest, "\n")+"\n"), 0o644); err != nil {
		_ = os.RemoveAll(dest)
		return BackupResult{}, fmt.Errorf("写入备份说明失败: %w", err)
	}

	files, size, err := collectFiles(dest)
	if err != nil {
		return BackupResult{}, err
	}

	res := BackupResult{
		Name: n, Path: dest, SizeBytes: size, Files: files, CreatedAt: now.Unix(),
	}
	// 卷失败时带出错误，让任务层能把状态置为 failed 而不是 succeeded。
	if volumeFailed {
		res.VolumeFailed = true
		res.VolumeError = "部分数据卷未能备份，编排文件已备份成功"
	}
	return res, nil
}

// copyProjectFiles 把项目目录下的文件复制到备份目录（不含子目录）。
//
// 为什么跳过子目录：目录里可能有 build 上下文或挂载的数据目录，
// 递归复制会把几 GB 的数据卷进备份，而用户要的是"配置备份"。
// 卷内容另有 IncludeVolumes 通道，职责分开更清楚。
func copyProjectFiles(srcDir, destDir string) ([]string, error) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return nil, fmt.Errorf("读取编排目录失败: %w", err)
	}
	copied := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// 跳过写入过程中的临时文件（见 workspace.Write 的原子写）。
		if strings.HasSuffix(name, ".tmp") {
			continue
		}
		src := filepath.Join(srcDir, name)
		dst := filepath.Join(destDir, name)
		if err := copyFile(src, dst); err != nil {
			return nil, err
		}
		copied = append(copied, name)
	}
	sort.Strings(copied)
	return copied, nil
}

// copyFile 复制单个文件。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("打开 %s 失败: %w", filepath.Base(src), err)
	}
	defer func() { _ = in.Close() }()

	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("读取 %s 信息失败: %w", filepath.Base(src), err)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("创建 %s 失败: %w", filepath.Base(dst), err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("复制 %s 失败: %w", filepath.Base(src), err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("关闭 %s 失败: %w", filepath.Base(dst), err)
	}
	return nil
}

// backupVolumes 用临时容器的 tar 备份该项目的命名卷。
//
// 实现方式：docker run --rm -v <卷>:/from:ro -v <备份目录>:/backup alpine tar ...
// 为什么用 --rm 的临时容器而不是 docker cp：docker cp 不能直接复制"卷本身"
// （卷不是容器文件系统的一部分），必须挂载进某个容器才能访问。
func (m *Manager) backupVolumes(ctx context.Context, project, destDir string) ([]string, error) {
	// 先列出所有卷，按 compose 命名约定筛选出属于本项目的。
	vols, err := m.listProjectVolumes(ctx, project)
	if err != nil {
		return nil, err
	}
	if len(vols) == 0 {
		return nil, nil
	}

	done := make([]string, 0, len(vols))
	var failed []string
	ob := observerFrom(ctx)
	for _, v := range vols {
		tarName := "volume-" + sanitizeFileName(v) + ".tar"
		args := []string{
			"run", "--rm",
			"-v", v + ":/from:ro",
			"-v", destDir + ":/backup",
			// 用 alpine 而不是 busybox：alpine 自带 tar 且体积小；
			// 离线环境下可能拉不到，此时靠下面的失败聚合提示用户。
			"alpine:3.20",
			"tar", "-cf", "/backup/" + tarName, "-C", "/from", ".",
		}
		// 这是**真实命令**，如实上报 argv（只含卷名与路径，不含密钥）。
		ob.command(append([]string{"docker"}, args...), nil)

		cmd := exec.CommandContext(ctx, "docker", args...)
		env := m.runner.environ()
		cmd.Env = env
		out, runErr := cmd.CombinedOutput()

		// CombinedOutput 拿不到逐行实时输出，因此在命令结束后
		// 把真实输出按行补发——内容与顺序都是真的，只是延迟到该步完成。
		for _, line := range splitLines(string(out)) {
			ob.stdout(line)
		}

		if runErr != nil {
			failed = append(failed, fmt.Sprintf("%s(%v: %s)", v, runErr, truncate(string(out))))
			ob.stage("卷备份失败: "+v, nil)
			continue
		}
		ob.stage("已备份卷: "+v, nil)
		done = append(done, v)
	}
	if len(failed) > 0 {
		return done, fmt.Errorf("部分卷未能备份: %s", strings.Join(failed, "; "))
	}
	sort.Strings(done)
	return done, nil
}

// listProjectVolumes 列出属于某编排项目的卷。
//
// 筛选依据：卷名以 "<project>_" 开头。这是 compose 的默认命名规则
// （除非用户在 volumes 里显式写了 name:）。显式命名的卷推导不出来，
// 会漏掉——这是本实现的已知限制，比乱备份别人的卷要好。
func (m *Manager) listProjectVolumes(ctx context.Context, project string) ([]string, error) {
	// 先确认 docker compose 可用，复用 runner 的 DOCKER_HOST 传递。
	if err := m.checkComposeAvailable(ctx); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "docker", "volume", "ls", "--format", "{{.Name}}")
	cmd.Env = m.runner.environ()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("列出数据卷失败: %w", err)
	}
	prefix := project + "_"
	var vols []string
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if strings.HasPrefix(name, prefix) {
			vols = append(vols, name)
		}
	}
	sort.Strings(vols)
	return vols, nil
}

// checkComposeAvailable 确认 docker compose 可用。
//
// 为什么要单独探测：Agent 镜像里装的是 docker-cli + docker-cli-compose，
// 插件发现路径不对时 docker compose 会报 "not a docker command"。
// 这种情况与其让用户看到一堆看不懂的错误，不如直接给出可操作的提示。
func (m *Manager) checkComposeAvailable(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, TimeoutShort)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, "docker", "compose", "version")
	cmd.Env = m.runner.environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker compose 不可用（%v）：%s", err, truncate(string(out)))
	}
	return nil
}

// sanitizeFileName 把卷名转成安全的文件名片段。
//
// 卷名来自 Docker（可能含点、斜杠以外的大部分字符），但备份文件名要落盘，
// 保守起见只保留字母数字与 -_.
func sanitizeFileName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// collectFiles 统计备份目录下的文件清单与总大小（不递归子目录）。
func collectFiles(dir string) ([]string, int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, fmt.Errorf("统计备份内容失败: %w", err)
	}
	files := make([]string, 0, len(entries))
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		files = append(files, e.Name())
		total += info.Size()
	}
	sort.Strings(files)
	return files, total, nil
}
