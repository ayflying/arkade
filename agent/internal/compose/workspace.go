package compose

// workspace.go 管理本机编排文件的落盘位置。
//
// 目录约定：
//
//	<data_dir>/stacks/<name>/compose.yml     编排文件
//	<data_dir>/backups/<name>/<时间戳>/      备份产物（见 backup.go）
//
// 为什么按项目名建子目录而不是平铺成 <name>.yml：
// compose 会以「项目目录」为基准解析相对路径（build: ./app、volumes: ./data），
// 平铺后相对路径会指向 data_dir 根，与用户预期不符；而且备份、附加文件
// （.env、override 文件）都需要一个属于该项目的目录。
//
// 安全边界：所有路径都由校验过的项目名拼接，且拼接后再做一次越界检查
// （defense in depth）——即使将来校验被改松，这里还能兜住。

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// composeFileName 是固定文件名。
//
// 用 compose.yml 而不是 docker-compose.yml：compose v2 官方推荐前者，
// 且两者同时存在时 compose 会优先 compose.yml，容易让人改错文件。
const composeFileName = "compose.yml"

// envFileName 是项目级环境变量文件名，必须与 compose 的约定一致（就叫 .env）。
//
// 不能改成自定义名字：compose 默认只读同目录的 .env 来做插值，
// 换名字就得每次多传参数，而一旦某条命令漏传，插值结果会与预期不同
// （变量为空 vs 有值），表现为"同样的配置有时起来不一样"。
const envFileName = ".env"

// envFileMode 是 .env 的权限：0600。
//
// 与 compose.yml 的 0644 不同，因为 .env 里几乎必然有密钥
// （数据库密码、API key、镜像仓库凭据）。0644 意味着同机任何用户都能读到，
// 而项目目录在共享主机上并非只有 Agent 一个用户。
// 0600 只影响"谁能读"，不影响 compose 自己的读取（它以属主身份运行）。
const envFileMode os.FileMode = 0o600

// Workspace 是编排文件的工作区。
type Workspace struct {
	// DataDir 是 Agent 数据目录（配置里的 data_dir）。
	DataDir string
}

// stacksDir 返回 stacks 根目录。
func (w *Workspace) stacksDir() string {
	return filepath.Join(w.DataDir, "stacks")
}

// backupsDir 返回 backups 根目录。
func (w *Workspace) backupsDir() string {
	return filepath.Join(w.DataDir, "backups")
}

// ProjectDir 返回某个项目的目录。项目名非法时报错，绝不返回可疑路径。
func (w *Workspace) ProjectDir(name string) (string, error) {
	n, err := cleanName(name)
	if err != nil {
		return "", err
	}
	return w.safeJoin(w.stacksDir(), n)
}

// ComposePath 返回某个项目的 compose.yml 绝对路径。
func (w *Workspace) ComposePath(name string) (string, error) {
	dir, err := w.ProjectDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, composeFileName), nil
}

// EnvPath 返回某个项目的 .env 绝对路径（不保证文件存在）。
func (w *Workspace) EnvPath(name string) (string, error) {
	dir, err := w.ProjectDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, envFileName), nil
}

// EnvExists 判断该项目的 .env 是否存在。
//
// 用 os.Stat 而不是保存某个标记：文件本身就是唯一真相，
// 额外的标记只会在"文件被手工删掉"时给出错误结论。
func (w *Workspace) EnvExists(name string) bool {
	p, err := w.EnvPath(name)
	if err != nil {
		return false
	}
	_, statErr := os.Stat(p)
	return statErr == nil
}

// safeJoin 把项目名拼进 base 并确认结果仍在 base 之内。
//
// 这是第二道防线：ValidateName 已经禁止了点和斜杠，理论上 filepath.Join
// 不可能越界；但校验逻辑将来可能被调整，这里用 filepath.Rel 复核一次，
// 越界即报错。安全相关的检查宁可重复，不可省略。
func (w *Workspace) safeJoin(base, name string) (string, error) {
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("解析数据目录失败: %w", err)
	}
	joined := filepath.Join(absBase, name)
	rel, err := filepath.Rel(absBase, joined)
	if err != nil {
		return "", fmt.Errorf("计算项目路径失败: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("非法的编排项目名: %q", name)
	}
	return joined, nil
}

// Write 把编排内容写入 <data_dir>/stacks/<name>/compose.yml。
//
// 写法上的两个细节：
//   - 先写临时文件再 rename：rename 在同一文件系统内是原子的，
//     避免写到一半（磁盘满、进程被杀）留下半截 compose.yml——
//     半截文件比没有文件更糟，因为它会让 compose 报出莫名其妙的语法错误。
//   - 权限 0644：编排文件不含密钥（密钥应放 .env 或 compose 的 secrets），
//     需要被容器内的 compose 读取。
func (w *Workspace) Write(name, content string) (string, error) {
	dir, err := w.ProjectDir(name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建编排目录失败: %w", err)
	}

	target := filepath.Join(dir, composeFileName)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("写入编排文件失败: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		// rename 失败时清理临时文件，避免留下垃圾。
		_ = os.Remove(tmp)
		return "", fmt.Errorf("保存编排文件失败: %w", err)
	}
	return target, nil
}

// commitTempName 生成一个**唯一**的临时文件路径。
//
// ============================ 为什么必须唯一 ============================
//
// 早先用固定名 `<target>.tmp`，并为了清理崩溃残留而先 `os.Remove` 它。
// 这在并发下会造成破坏：两个请求同时写同一项目时，
// B 的"清理残留"正好删掉 A 正在写的临时文件，
// 于是 A 的 rename 拿到空文件或直接 ENOENT——**而 A 已经报告了成功**。
//
// 加上进程内项目锁后，同一 Agent 内不再并发；但临时名仍要唯一，因为：
//   - 跨进程没有锁（本地 CLI 可能与常驻 Agent 同时写），
//     唯一名保证两者**绝不互相删除**对方的临时文件。
//     我们不承诺跨进程的结果正确，但绝不能主动制造破坏；
//   - 崩溃残留不会被下一次写入误删，排查时还能看到现场。
//
// 代价：崩溃可能留下少量孤儿临时文件。它们不会被任何逻辑读取
// （唯一的读取路径是 rename 到正式名，而那是本次调用自己写的名字），
// 只是占用磁盘。相比"删掉别人正在写的文件"，这个代价可以接受。
func commitTempName(target string) string {
	return fmt.Sprintf("%s.tmp.%d.%s", target, os.Getpid(), randomSuffix())
}

// randomSuffix 生成 8 字节随机十六进制串。
//
// 随机源不可用时**退化为时间戳**而不是失败：
// 拿不到随机数不该阻止用户保存编排，而时间戳+进程号已足以区分同机并发。
func randomSuffix() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// CommitProject 把 compose.yml 与 .env **一起**落盘，尽可能接近原子。
//
// ============================ 为什么需要"一起" ============================
//
// compose.yml 与 .env 共同决定哈希与运行结果。
// 若分两次独立写，第二次失败就会留下"新编排 + 旧 env"的组合，
// 而这个组合的哈希我们**从来没有校验过**——它既不是旧状态也不是新状态。
// 危害很具体：用户把镜像 tag 从 1.0 改到 2.0 并把新 tag 写在 .env 里，
// 若 .env 写失败而 compose.yml 写成功，服务会以 1.0 的变量配 2.0 的编排启动。
//
// ============================ 实现方式 ============================
//
// 两个 renames 无法真正原子（文件系统不提供跨文件事务），所以采用：
//  1. 先把两份内容都写进各自的 .tmp（任一失败 → 全清，绝不 rename）；
//  2. 记录旧内容（用于回滚）；
//  3. 依次 rename；
//  4. 若第 2 个 rename 失败，用记录下来的旧内容把第 1 个恢复回去。
//
// 这样保证的语义是"要么都新、要么都旧"，而不是"可能半新半旧"。
// 极端情况（回滚本身也失败，例如磁盘故障）会在错误里明示，
// 让用户知道需要手工核对，而不是静默留下不一致。
//
// 顺序上先 rename compose.yml 再 rename .env：
// 先写 .env 而 compose.yml 失败，才更危险（新密钥配旧编排）；
// 反过来 compose.yml 新而 .env 旧，至少下次下发会被哈希校验发现。
func (w *Workspace) CommitProject(name, content, envContent string) (cfgPath, envPath string, err error) {
	dir, err := w.ProjectDir(name)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("创建编排目录失败: %w", err)
	}

	cfgTarget := filepath.Join(dir, composeFileName)
	envTarget := filepath.Join(dir, envFileName)

	// 记录旧状态，供失败时回滚。"" 表示原本不存在。
	// 旧 .env 不需要读取：它的语义是"被本次内容覆盖或删除"，
	// 不存在需要恢复旧 .env 的失败路径（.env 是最后一步，
	// 失败时回滚的是 compose.yml，而不是把旧 .env 写回去）。
	oldCfg, oldCfgErr := os.ReadFile(cfgTarget)

	cfgTmp := commitTempName(cfgTarget)
	envTmp := commitTempName(envTarget)
	cleanup := func() {
		_ = os.Remove(cfgTmp)
		_ = os.Remove(envTmp)
	}

	// 第 1 步：两份内容都先落到临时文件，任何失败都不进入 rename 阶段。
	if werr := os.WriteFile(cfgTmp, []byte(content), 0o644); werr != nil {
		cleanup()
		return "", "", fmt.Errorf("写入编排文件失败: %w", werr)
	}
	writeEnv := envContent != ""
	if writeEnv {
		if werr := writeSecretFile(envTmp, envContent); werr != nil {
			cleanup()
			return "", "", fmt.Errorf("写入环境变量文件失败: %w", werr)
		}
	}

	// 第 2 步：rename compose.yml。
	if rerr := os.Rename(cfgTmp, cfgTarget); rerr != nil {
		cleanup()
		return "", "", fmt.Errorf("保存编排文件失败: %w", rerr)
	}

	// 第 3 步：提交 .env（写入或删除）。
	if writeEnv {
		if rerr := os.Rename(envTmp, envTarget); rerr != nil {
			_ = os.Remove(envTmp)
			// 回滚 compose.yml：不留下"新编排 + 旧 env"的半写状态。
			rbErr := restoreFile(cfgTarget, oldCfg, oldCfgErr == nil, 0o644)
			msg := fmt.Sprintf("保存环境变量文件失败: %v", rerr)
			if rbErr != nil {
				msg += fmt.Sprintf("；且回滚编排文件失败（%v），请人工核对 %s 与 .env 是否匹配", rbErr, cfgTarget)
			}
			return "", "", errors.New(msg)
		}
	} else {
		// 清空 .env：删除它，并回滚 compose.yml 失败时一并恢复。
		if rmErr := os.Remove(envTarget); rmErr != nil && !os.IsNotExist(rmErr) {
			rbErr := restoreFile(cfgTarget, oldCfg, oldCfgErr == nil, 0o644)
			msg := fmt.Sprintf("清除环境变量文件失败: %v", rmErr)
			if rbErr != nil {
				msg += fmt.Sprintf("；且回滚编排文件失败（%v），请人工核对 %s", rbErr, cfgTarget)
			}
			return "", "", errors.New(msg)
		}
	}

	if writeEnv {
		return cfgTarget, envTarget, nil
	}
	return cfgTarget, "", nil
}

// restoreFile 把文件恢复到给定内容；exists=false 表示恢复成"不存在"。
func restoreFile(path string, data []byte, exists bool, mode os.FileMode) error {
	if !exists {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	tmp := path + ".rollback"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// writeSecretFile 以 0600 创建并写满一个存密钥的文件。
//
// 与 os.WriteFile 的区别：先 O_EXCL 创建（保证 perm 生效），
// 再写入并 Sync。不能用"先写后 Chmod"——那中间有一瞬文件按 umask 是可读的，
// 对存密钥的文件，那一瞬就是泄漏窗口。
func writeSecretFile(path, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, envFileMode)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return err
	}
	// Sync 后再 rename：否则断电可能得到"文件存在但内容为空"的 .env，
	// 那会让 compose 用空变量启动（例如密码为空），比启动失败更危险。
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// WriteEnv 写入项目 .env；content 为空表示**删除**该文件。
//
// 返回实际写入的路径（未写入时为空串）与"是否写入了文件"。
//
// ============================ 为什么空内容要删除而不是写空文件 ============================
//
// 写一个空 .env 与"没有 .env"在 compose 看来几乎等价，但对用户的心理模型不同：
// 用户把 .env 清空，意思是"这台机器上不该再留这些密钥"。
// 若留下一个空文件（甚至留下旧文件不处理），旧密钥就仍在磁盘上，
// 而界面显示"已清空"——这是安全上最坏的一种不一致：用户以为删了，其实还在。
//
// ============================ 为什么单独一个函数而不是复用 Write ============================
//
// 权限不同（0600 vs 0644），且删除语义只对 .env 成立。
// 若给 Write 加一个 mode 参数，会诱使将来有人给 compose.yml 传 0600，
// 那会让容器内的非 root 进程读不到编排文件。
func (w *Workspace) WriteEnv(name, content string) (path string, written bool, err error) {
	target, err := w.EnvPath(name)
	if err != nil {
		return "", false, err
	}

	if content == "" {
		// 清空：删除文件。NotExist 不算错误（幂等）。
		if rmErr := os.Remove(target); rmErr != nil && !os.IsNotExist(rmErr) {
			return "", false, fmt.Errorf("清除环境变量文件失败: %w", rmErr)
		}
		return "", false, nil
	}

	dir, err := w.ProjectDir(name)
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, fmt.Errorf("创建编排目录失败: %w", err)
	}

	// 与 compose.yml 相同的"临时文件 + rename"原子写法，
	// 区别是权限 0600，并且**创建时就带 0600**。
	//
	// 关键细节：不能用 os.WriteFile(tmp, data, 0600) 之后就认为安全——
	// os.WriteFile 对已存在的文件不会改权限。因此这里先确保 tmp 不存在，
	// 再让 OpenFile 的 perm 生效，最后 rename。
	// 另一条路是先写再 Chmod，但那中间有一瞬文件是可读的（0644 默认按 umask），
	// 对于存密钥的文件，那一瞬就是泄漏窗口。
	tmp := target + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, envFileMode)
	if err != nil {
		return "", false, fmt.Errorf("创建环境变量文件失败: %w", err)
	}
	if _, werr := f.WriteString(content); werr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", false, fmt.Errorf("写入环境变量文件失败: %w", werr)
	}
	// Sync 后再 rename：否则断电可能得到"文件存在但内容为空"的 .env，
	// 那会让 compose 用空变量启动（例如密码为空），比启动失败更危险。
	if serr := f.Sync(); serr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", false, fmt.Errorf("刷新环境变量文件失败: %w", serr)
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(tmp)
		return "", false, fmt.Errorf("关闭环境变量文件失败: %w", cerr)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return "", false, fmt.Errorf("保存环境变量文件失败: %w", err)
	}
	return target, true, nil
}

// ReadEnv 读取项目 .env；不存在时返回空串（不报错）。
//
// 不报错是有意的：调用方（备份、哈希校验）需要区分"没有 .env"与"读取失败"，
// 前者是正常状态。真正的读取失败（权限、IO）应当报错，因此仍然返回 err。
func (w *Workspace) ReadEnv(name string) (string, error) {
	p, err := w.EnvPath(name)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("读取环境变量文件失败: %w", err)
	}
	return string(data), nil
}

// Read 读取某个项目的编排内容。
func (w *Workspace) Read(name string) (string, error) {
	p, err := w.ComposePath(name)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("编排 %s 在本机不存在", name)
		}
		return "", fmt.Errorf("读取编排文件失败: %w", err)
	}
	return string(data), nil
}

// Exists 判断本机是否已有该项目的编排文件。
func (w *Workspace) Exists(name string) bool {
	p, err := w.ComposePath(name)
	if err != nil {
		return false
	}
	_, statErr := os.Stat(p)
	return statErr == nil
}

// Project 是本机上的一个编排项目。
//
// 这是项目信息的唯一类型：workspace 负责填 name/dir/时间，
// manager 再补 hash/状态/服务列表，避免出现两个高度相似的结构体
// 在层间来回转换（那种转换正是字段漏拷的来源）。
type Project struct {
	Name      string
	Dir       string
	UpdatedAt time.Time
	// HasComposeFile 为 false 表示目录存在但没有 compose.yml（异常状态，
	// 可能是手工删了文件或写入中断），列表里要体现出来而不是静默忽略。
	HasComposeFile bool
}

// List 列出本机全部编排项目，按名称升序。
//
// 跳过非法目录名而不是报错：stacks 目录下可能有用户手工建的、名字不合规的目录，
// 一个坏目录不该让整个列表接口失败。跳过即可，真正的异常项目由日志暴露。
func (w *Workspace) List() ([]Project, error) {
	base := w.stacksDir()
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			// 目录还不存在意味着"没有任何编排"，是正常状态而不是错误。
			return nil, nil
		}
		return nil, fmt.Errorf("读取编排目录失败: %w", err)
	}

	out := make([]Project, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !IsValidName(name) {
			continue
		}
		p := Project{Name: name, Dir: filepath.Join(base, name)}
		if info, serr := os.Stat(filepath.Join(p.Dir, composeFileName)); serr == nil && !info.IsDir() {
			p.HasComposeFile = true
			p.UpdatedAt = info.ModTime().UTC()
		} else if dinfo, derr := e.Info(); derr == nil {
			// 没有 compose.yml 时用目录修改时间兜底，至少让界面有东西显示。
			p.UpdatedAt = dinfo.ModTime().UTC()
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Remove 删除某个项目的目录及其全部内容。
//
// 危险：会删掉目录里的一切（包括用户手工放的 .env、build 产物）。
// 调用方必须先执行 compose down，否则容器状态会残留。
func (w *Workspace) Remove(name string) error {
	dir, err := w.ProjectDir(name)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("删除编排目录失败: %w", err)
	}
	return nil
}
