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
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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

	// Volumes 是**已成功备份**的命名卷（物理卷名）。
	Volumes []string
	// UntrackedVolumes 是**没能备份**的卷说明。
	//
	// ============================ 为什么单独列出来 ============================
	// 有两类卷会落到这里：
	//   - external: true 的外部卷：它不属于本项目，删项目时也不会删它，
	//     但项目的数据就在里面；
	//   - 显式 name: 声明的卷：物理名不在本项目命名空间里。
	//     （label 过滤能发现它，但早期版本的前缀匹配会静默漏掉。）
	//
	// 无论哪一类，用户点"备份（含数据卷）"时期的预期是"数据都在里面了"。
	// 只要有一个卷没进去，就必须**明确告诉他**，否则这份备份会在
	// 真正需要恢复的那天让人失望——而这正是备份最不能出错的地方。
	UntrackedVolumes []string
	// ExplicitNameVolumes 是**靠 label 过滤才发现**的显式命名卷。
	//
	// ============================ 为什么要单独统计 ============================
	// 旧实现按 "<project>_" 前缀找卷，因此对
	//
	//	volumes:
	//	  cache:
	//	    name: shared-cache
	//
	// 这类卷是**静默漏掉**的。现在改用 label 过滤后能捞到了，
	// 于是升级后的第一次备份会突然多出几个卷。
	//
	// 如果不告诉用户"这次比以前的版本多备份了 N 个"，他会以为卷集合
	// 莫名变多、甚至怀疑备份逻辑出错了。而实际上这是修复带来的正确变化。
	ExplicitNameVolumes []string
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
	var backedUp, explicitVols []string
	if includeVolumes {
		ob.stage("枚举项目数据卷", nil)
		vols, ext, verr := m.backupVolumes(ctx, n, dest)
		backedUp = vols
		explicitVols = ext
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
		// 明确本次包含的显式命名卷，不混入历史版本说明。
		if len(explicitVols) > 0 {
			manifest = append(manifest,
				"本次包含 "+strconv.Itoa(len(explicitVols))+" 个显式命名卷: "+
					strings.Join(explicitVols, ", "))
			ob.stage(fmt.Sprintf("本次包含 %d 个显式命名卷", len(explicitVols)), nil)
		}
	} else {
		manifest = append(manifest, "未包含数据卷（调用方未要求）")
		ob.stage("未包含数据卷（调用方未要求）", nil)
	}

	// ============================ 未追踪卷：必须让用户看见 ============================
	// 只有"用户要求了数据卷"时才有意义：没要求卷时所有卷都是未备份的，
	// 报一堆"未追踪"反而是噪音（前面已经明确说"未包含数据卷"了）。
	var untracked []string
	if includeVolumes {
		present := make(map[string]struct{}, len(backedUp))
		for _, v := range backedUp {
			present[v] = struct{}{}
		}
		untracked = m.discoverUntrackedVolumes(projectDir, present)
		if len(untracked) > 0 {
			// 逐条写进 manifest，便于运维直接看文件就知道缺什么。
			manifest = append(manifest, "以下卷未包含在备份里:")
			for _, u := range untracked {
				manifest = append(manifest, "  - "+u)
			}
			// 同时用 stage 事件告知界面：这是用户需要立刻知道的坏消息。
			ob.stage(fmt.Sprintf("注意：有 %d 个数据卷未包含在本次备份里", len(untracked)), nil)
		}
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
		Volumes: backedUp, UntrackedVolumes: untracked, ExplicitNameVolumes: explicitVols,
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
//
// 返回值 explicit 是"因改用 label 过滤才被发现"的卷——它们的物理名不符合
// <project>_ 前缀约定（用户在配置里显式写了 name:）。旧实现会静默漏掉它们，
// 因此需要向用户说明"这次比以前多备份了这些"。
func (m *Manager) backupVolumes(ctx context.Context, project, destDir string) (backed, explicit []string, err error) {
	// 先列出所有卷（label 优先，前缀回退，细节见 listProjectVolumes）。
	vols, err := m.listProjectVolumesDetailed(ctx, project)
	if err != nil {
		return nil, nil, err
	}
	if len(vols) == 0 {
		return nil, nil, nil
	}

	prefix := project + "_"
	done := make([]string, 0, len(vols))
	var ext []string
	var failed []string
	ob := observerFrom(ctx)
	for _, v := range vols {
		tarName := "volume-" + sanitizeFileName(v.Name) + ".tar"
		args := []string{
			"run", "--rm",
			"-v", v.Name + ":/from:ro",
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
			failed = append(failed, fmt.Sprintf("%s(%v: %s)", v.Name, runErr, truncate(string(out))))
			ob.stage("卷备份失败: "+v.Name, nil)
			continue
		}
		ob.stage("已备份卷: "+v.Name, nil)
		done = append(done, v.Name)
		// 物理名不遵守 <project>_ 约定的，就是旧实现会漏掉的显式命名卷。
		if !strings.HasPrefix(v.Name, prefix) {
			ext = append(ext, v.Name)
		}
	}
	if len(failed) > 0 {
		return done, ext, fmt.Errorf("部分卷未能备份: %s", strings.Join(failed, "; "))
	}
	sort.Strings(done)
	sort.Strings(ext)
	return done, ext, nil
}

// labelComposeProjectVolume 是 compose 打在卷上的"逻辑卷名"标签。
//
// compose 在创建命名卷时会写三个标签：
//
//	com.docker.compose.project        项目名
//	com.docker.compose.volume         卷在 compose.yml 里的逻辑名（如 data）
//	com.docker.compose.config-hash    配置哈希
//
// 这里用 project 过滤，用 volume 记录"这个物理卷对应配置里的哪个名字"，
// 从而能向用户报告可读的映射关系，而不是只有一串 <project>_<key>。
const labelComposeProjectVolume = "com.docker.compose.volume"

// projectVolume 描述一个属于本项目的数据卷。
type projectVolume struct {
	// Name 是 Docker 里真实的卷名（挂载时用的就是它）。
	Name string
	// Logical 是 compose.yml 里的逻辑名；拿不到时为空。
	Logical string
	// Source 说明这个卷是怎么被发现的，仅用于日志与排查。
	Source string
}

// listProjectVolumes 列出属于某编排项目的卷。
//
// ============================ 为什么改成 label 过滤 ============================
// 原实现是 `docker volume ls --format {{.Name}}` 把**机器上所有卷**拉回来，
// 再按 "<project>_" 前缀过滤。两个问题：
//
//  1. 慢且占内存。卷多的机器上（CI 机、多租户机）这一步会拉几万个名字，
//     而真正属于本项目的可能只有 2 个。
//
//  2. 更严重：**会静默漏掉显式命名的卷**。用户在 compose.yml 里写
//
//     volumes:
//     data:
//     name: my-postgres-data
//
//     这个卷的物理名是 my-postgres-data，不带 "<project>_" 前缀，
//     前缀匹配找不到它——于是备份**悄悄少了一个卷**，
//     而用户以为备份里有一切。这不是"少一个提示"，是数据丢失风险。
//
// compose 会给它创建的每个卷打 com.docker.compose.project 标签，
// 用这个过滤既更快又更准（已实测 docker 28.2.2 行为）。
//
// ============================ 为什么要保留前缀回退 ============================
// label 过滤依赖"卷是 compose 创建的且带标签"。下列情况标签不存在：
//   - 卷是用户手工 docker volume create 的；
//   - 项目曾用很老的 compose 版本创建；
//   - 卷名恰好符合前缀约定但由别的工具创建。
//
// 这时退回前缀匹配能兜住，总比一个卷都发现不了强。
//
// 两条路径在**同一个函数**里实现，不另写一份过滤逻辑——
// 两份实现迟早会在"什么是属于我项目的卷"上给出不同答案。
func (m *Manager) listProjectVolumes(ctx context.Context, project string) ([]string, error) {
	vols, err := m.listProjectVolumesDetailed(ctx, project)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(vols))
	for _, v := range vols {
		names = append(names, v.Name)
	}
	sort.Strings(names)
	return names, nil
}

// listProjectVolumesDetailed 是 listProjectVolumes 的带元数据版本。
func (m *Manager) listProjectVolumesDetailed(ctx context.Context, project string) ([]projectVolume, error) {
	// 先确认 docker compose 可用，复用 runner 的 DOCKER_HOST 传递。
	if err := m.checkComposeAvailable(ctx); err != nil {
		return nil, err
	}

	// ============================ 主路径：label 过滤 ============================
	byLabel, labelErr := m.listVolumesByLabel(ctx, project)
	if labelErr == nil && len(byLabel) > 0 {
		return byLabel, nil
	}

	// ============================ 回退：前缀匹配 ============================
	// 只有在 label 路径**出错**或**一个都没找到**时才走这里。
	//
	// "一个都没找到"也要回退：一个刚创建、还没 up 过的项目，
	// 卷可能确实是手工建的（没有标签），此时前缀匹配是唯一线索。
	byPrefix, prefixErr := m.listVolumesByPrefix(ctx, project)

	if labelErr != nil && prefixErr != nil {
		// 两条都失败：如实报告两条错误，不要只报一条让人以为另一条没问题。
		return nil, fmt.Errorf("列出数据卷失败（label 过滤：%v；前缀匹配：%v）", labelErr, prefixErr)
	}
	return byPrefix, nil
}

// parseVolumeLinesByLabel 解析 `volume ls --format "{{.Name}}\t{{.Label}}"` 的输出。
//
// ============================ 为什么把这步抽成纯函数 ============================
// 卷发现的正确性有两个独立的关注点：
//   - 怎么问 docker（label 还是前缀、失败怎么回退）—— 需要真 docker；
//   - 拿到的文本怎么解析 —— **不需要 docker**。
//
// 把解析抽出来，就能在任何平台（包括没有 docker 的 Windows 开发机）
// 用固定文本样例测掉制表符缺失、逻辑名带空格、空行、重复行这些真实边界。
// 否则这些分支只能靠 Linux 上的 shell 假 docker 覆盖，而绝大多数开发
// 都在 Windows 上跑测试——等于没有覆盖。
func parseVolumeLinesByLabel(out string) []projectVolume {
	var vols []projectVolume
	for _, line := range strings.Split(out, "\n") {
		// 只去行尾 \r（Windows 换行）与整体空白，**不要 TrimSpace 整行后再切**，
		// 否则逻辑名为空时的尾部制表符会被吃掉，无法区分
		// "没有逻辑名" 与 "逻辑名是空串"。
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		name, logical := line, ""
		if i := strings.IndexByte(line, '\t'); i >= 0 {
			name = strings.TrimSpace(line[:i])
			logical = strings.TrimSpace(line[i+1:])
		}
		if name == "" {
			continue
		}
		vols = append(vols, projectVolume{Name: name, Logical: logical, Source: "label"})
	}
	vols = dedupeVolumes(vols)
	sort.Slice(vols, func(i, j int) bool { return vols[i].Name < vols[j].Name })
	return vols
}

// parseVolumeLinesByPrefix 用前缀约定从 `volume ls --format {{.Name}}` 的输出里筛选。
func parseVolumeLinesByPrefix(out, project string) []projectVolume {
	prefix := project + "_"
	var vols []projectVolume
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		vols = append(vols, projectVolume{
			Name:    name,
			Logical: strings.TrimPrefix(name, prefix),
			Source:  "prefix",
		})
	}
	vols = dedupeVolumes(vols)
	sort.Slice(vols, func(i, j int) bool { return vols[i].Name < vols[j].Name })
	return vols
}

// listVolumesByLabel 用 compose 的项目标签精确列出卷。
func (m *Manager) listVolumesByLabel(ctx context.Context, project string) ([]projectVolume, error) {
	// 用两个占位符同时取名字与逻辑卷名，一次调用拿全。
	// --format 里用制表符分隔，比解析 json 省事且不受 docker 版本影响。
	cmd := exec.CommandContext(ctx, "docker", "volume", "ls",
		"--filter", labelComposeProject+"="+project,
		"--format", "{{.Name}}\t{{.Label \""+labelComposeProjectVolume+"\"}}")
	cmd.Env = m.runner.environ()
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseVolumeLinesByLabel(string(out)), nil
}

// listVolumesByPrefix 按 compose 的默认命名规则（<project>_<key>）匹配。
//
// 这是回退路径：它会拉全量卷名，因此只在 label 过滤无效时使用。
func (m *Manager) listVolumesByPrefix(ctx context.Context, project string) ([]projectVolume, error) {
	cmd := exec.CommandContext(ctx, "docker", "volume", "ls", "--format", "{{.Name}}")
	cmd.Env = m.runner.environ()
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseVolumeLinesByPrefix(string(out), project), nil
}

// discoverUntrackedVolumes 从 compose 配置里找出**不会被本项目命名空间覆盖**的卷。
//
// ============================ 这个问题为什么必须单独解 ============================
// 备份卷时我们枚举的是"本项目的卷"。但 compose 配置里有两类卷天生不在
// 本项目命名空间里：
//
//	volumes:
//	  pgdata:
//	    external: true          # 由外部管理，删项目不删它
//	  cache:
//	    name: shared-cache      # 物理名由用户指定
//
// 对 external 卷，Docker 不会给它打本项目标签（它可能被多个项目共用），
// 所以 label 过滤找不到它。对显式 name 卷，label 过滤能发现它
// （compose 仍会打标签）——因此这两类要分开判断：
//   - external：必然拿不到，直接列入未追踪；
//   - 显式 name：如果它出现在已发现的卷里，说明备份到了，不算未追踪。
//
// 解析用 yaml.v3 的节点树，只取顶层 volumes 下的 external 与 name 两个键，
// 不尝试完整理解 compose 语义（那是 migrate 包的事，这里只要够准）。
func (m *Manager) discoverUntrackedVolumes(projectDir string, present map[string]struct{}) []string {
	cfgPath := filepath.Join(projectDir, composeFileName)
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil
	}

	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil || len(root.Content) == 0 {
		return nil
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil
	}

	var decls *yaml.Node
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value == "volumes" {
			decls = doc.Content[i+1]
			break
		}
	}
	if decls == nil || decls.Kind != yaml.MappingNode {
		return nil
	}

	var untracked []string
	for i := 0; i+1 < len(decls.Content); i += 2 {
		key := decls.Content[i].Value
		val := decls.Content[i+1]
		if val.Kind != yaml.MappingNode {
			continue
		}
		external := false
		explicitName := ""
		for j := 0; j+1 < len(val.Content); j += 2 {
			switch val.Content[j].Value {
			case "external":
				// 兼容两种写法：external: true 与 external: {name: xxx}
				if val.Content[j+1].Kind == yaml.ScalarNode {
					external = strings.EqualFold(val.Content[j+1].Value, "true")
				} else if val.Content[j+1].Kind == yaml.MappingNode {
					external = true
				}
			case "name":
				if val.Content[j+1].Kind == yaml.ScalarNode {
					explicitName = strings.TrimSpace(val.Content[j+1].Value)
				}
			}
		}
		if external {
			// ============================ external 才是真正的"不归我" ============================
			// 它由 compose 之外管理（可能被多个项目共用），Docker 不会给它打
			// 本项目的标签，因此 label 过滤天然找不到它。
			// 备份它既做不到（拿不到），也不该做（别人的东西）。
			untracked = append(untracked, fmt.Sprintf("%s（external: true，由外部管理，未备份）", key))
			continue
		}
		if explicitName == "" {
			continue
		}
		// ============================ 显式 name 的卷：能捞到就算已备份 ============================
		// 与 external 不同，显式 name 的卷**仍然是本项目的卷**：
		// compose 给它打了 project 标签，数据也归本项目所有，
		// 只是物理名由用户指定而已。既然 label 过滤能发现它，
		// 就应当备份它——这也正是修掉"静默漏备份"的意义所在。
		//
		// 只有在它**确实没被枚举到**时才算未追踪（例如卷还没创建、
		// 或是极老的 compose 版本没打标签）。
		if _, ok := present[explicitName]; !ok {
			untracked = append(untracked,
				fmt.Sprintf("%s（显式命名 %s，未在本次备份中）", key, explicitName))
		}
	}
	sort.Strings(untracked)
	return untracked
}
func dedupeVolumes(vols []projectVolume) []projectVolume {
	if len(vols) <= 1 {
		return vols
	}
	seen := make(map[string]struct{}, len(vols))
	out := vols[:0]
	for _, v := range vols {
		if _, ok := seen[v.Name]; ok {
			continue
		}
		seen[v.Name] = struct{}{}
		out = append(out, v)
	}
	return out
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
