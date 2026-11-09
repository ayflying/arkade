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
