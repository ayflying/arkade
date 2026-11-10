package compose

// operation.go：编排动作的统一定义与执行分发。
//
// ============================ 设计要点 ============================
//
// 八个动作全部通过**原 Manager 方法**执行，绝不裸调 run("up")：
//   - ApplyProject 内部有哈希校验、原子 commit、env 排除、外部项目探测；
//   - 裸跑会把这些保护全丢掉，是最危险的"为了日志而降低安全性"。
//
// 观察者通过 ctx 传入（见 observer.go），因此这些方法一行都不用改。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
)

// 支持的编排动作。
const (
	ActionUp      = "up"
	ActionDown    = "down"
	ActionStop    = "stop"
	ActionRestart = "restart"
	ActionPull    = "pull"
	ActionRebuild = "rebuild"
	ActionRemove  = "remove"
	ActionBackup  = "backup"
)

// 领域错误。
var (
	// ErrUnknownAction 动作不在支持范围内。
	ErrUnknownAction = errors.New("不支持的编排动作")
	// ErrActionParams 动作参数不合法（缺 spec、多余参数等）。
	ErrActionParams = errors.New("动作参数不合法")
)

// OperationSpec 是一次编排动作的全部参数。
//
// 用结构体而不是 map：字段是白名单的，fingerprint 也基于它做规范序列化，
// 不会因为多传了未定义字段而产生"看起来不同实际相同"的任务。
type OperationSpec struct {
	Action string
	// Config 与 Env 仅 up 使用（沿用既有 ProjectSpec 的字段语义）。
	Config     string
	Env        string
	ExpectHash string
	// Services 仅 stop/restart 使用。
	Services []string
	// RemoveVolumes 仅 down 使用。
	//
	// 注意：remove 动作**固定**连带删除卷（与既有 Manager.Remove 一致），
	// 这里不提供开关，避免出现"remove 但不删卷"这种半删状态。
	RemoveVolumes bool
	// IncludeVolumes 仅 backup 使用。
	IncludeVolumes bool
}

// validActions 是允许的动作集合。
var validActions = map[string]struct{}{
	ActionUp: {}, ActionDown: {}, ActionStop: {}, ActionRestart: {},
	ActionPull: {}, ActionRebuild: {}, ActionRemove: {}, ActionBackup: {},
}

// normalizeSpec 校验并规范化参数。
//
// 规范化（而非拒绝）的只有 services 排序：compose 不关心服务顺序，
// 但"stop web db"与"stop db web"若指纹不同就会被误判为冲突。
// 排序前先做**去重**，且保留重复项没有意义（compose 会执行两次 stop）。
func normalizeSpec(spec OperationSpec) (OperationSpec, error) {
	spec.Action = strings.ToLower(strings.TrimSpace(spec.Action))
	if _, ok := validActions[spec.Action]; !ok {
		return OperationSpec{}, ErrUnknownAction
	}

	// services：去重 + 排序（只影响指纹，不影响实际执行时传给 compose 的顺序？
	// 实际上会影响——因此这里同时规范化实际参数，保持"指纹与执行一致"）。
	if len(spec.Services) > 0 {
		seen := make(map[string]struct{}, len(spec.Services))
		out := make([]string, 0, len(spec.Services))
		for _, s := range spec.Services {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if _, dup := seen[s]; dup {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
		sort.Strings(out)
		spec.Services = out
	}

	switch spec.Action {
	case ActionUp:
		if strings.TrimSpace(spec.Config) == "" {
			return OperationSpec{}, errors.New("启动编排必须提供编排内容")
		}
		// up 不使用这些参数，清空以免混进指纹。
		spec.Services = nil
		spec.RemoveVolumes = false
		spec.IncludeVolumes = false
	case ActionBackup:
		spec.Services = nil
		spec.RemoveVolumes = false
	default:
		spec.IncludeVolumes = false
		// ============================ RemoveVolumes 的适用范围 ============================
		// down 与 remove **都要**保留这个开关：
		//   - down：是否删卷；
		//   - remove：down 之后再删本机编排文件，同样有是否删卷的选择。
		//
		// 只认 ActionDown 会把 remove 的 true 静默丢掉，
		// 导致用户明确勾了"同时删除数据卷"却只得到一个普通 remove。
		// 其余动作（stop/restart/pull/rebuild）才清空。
		if spec.Action != ActionDown && spec.Action != ActionRemove {
			spec.RemoveVolumes = false
		}
		if spec.Action != ActionStop && spec.Action != ActionRestart {
			spec.Services = nil
		}
	}
	return spec, nil
}

// fingerprint 计算参数指纹。
//
// ============================ 为什么用 json.Marshal 而不是拼字符串 ============================
//
// 拼接容易出现歧义（"a|b" 与 "a" + "|b"），规范化 JSON 则保证
// 同一组参数得到同一个串、不同参数得到不同串。
//
// 注意：**不含 .env 的明文**。Env 内容用哈希参与，
// 这样即便指纹被记录也不泄露密码。
func (spec OperationSpec) fingerprint(name string) string {
	envHash := ""
	if spec.Env != "" {
		sum := sha256.Sum256([]byte(spec.Env))
		envHash = hex.EncodeToString(sum[:])
	}
	payload := struct {
		Name          string
		Action        string
		Services      []string
		RemoveVolumes bool
		Include       bool
		ExpectHash    string
		EnvHash       string
		ConfigHash    string
	}{
		Name:          name,
		Action:        spec.Action,
		Services:      spec.Services,
		RemoveVolumes: spec.RemoveVolumes,
		Include:       spec.IncludeVolumes,
		ExpectHash:    spec.ExpectHash,
		EnvHash:       envHash,
	}
	if spec.Config != "" {
		sum := sha256.Sum256([]byte(spec.Config))
		payload.ConfigHash = hex.EncodeToString(sum[:])
	}
	b, err := json.Marshal(payload)
	if err != nil {
		// 结构体固定且字段均可序列化，实际不可能失败；
		// 兜底返回动作名，宁可保守地"每个请求都当成同一任务"也不要 panic。
		return spec.Action
	}
	return string(b)
}

// relativeBackupPath 把备份绝对路径转成相对工作区的路径。
//
// 转换失败（路径不在工作区内）时只回末级目录名：
// 宁可给出一个信息量少的相对标识，也不要回显绝对路径。
func (m *Manager) relativeBackupPath(abs string) string {
	base := m.ws.backupsDir()
	if rel, err := filepath.Rel(base, abs); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(filepath.Join("backups", rel))
	}
	return filepath.ToSlash(filepath.Base(abs))
}

// execute 执行动作，返回退出码、备份相对路径与错误。
//
// 全程通过 ctx 里的 observer 广播真实命令与输出；
// 每个动作都调用**原 Manager 方法**，语义与同步接口完全一致。
func (m *Manager) execute(ctx context.Context, name string, spec OperationSpec) (int, string, error) {
	switch spec.Action {
	case ActionUp:
		res, err := m.ApplyProject(ctx, ProjectSpec{
			Name:       name,
			Content:    spec.Config,
			EnvContent: spec.Env,
			ExpectHash: spec.ExpectHash,
		})
		return res.ExitCode, "", err

	case ActionDown:
		res, err := m.Down(ctx, name, spec.RemoveVolumes)
		return res.ExitCode, "", err

	case ActionStop:
		res, err := m.Stop(ctx, name, spec.Services)
		return res.ExitCode, "", err

	case ActionRestart:
		res, err := m.Restart(ctx, name, spec.Services)
		return res.ExitCode, "", err

	case ActionPull:
		res, err := m.Pull(ctx, name)
		return res.ExitCode, "", err

	case ActionRebuild:
		res, err := m.Rebuild(ctx, name)
		return res.ExitCode, "", err

	case ActionRemove:
		// 是否删卷由请求显式决定（RemoveWithVolumes），**不默认删卷**：
		// 删卷不可逆，绝不能因为用户点了"删除编排"就永久丢数据。
		res, err := m.RemoveWithVolumes(ctx, name, spec.RemoveVolumes)
		return res.ExitCode, "", err

	case ActionBackup:
		// backup 内部是文件复制 + 裸 exec（tar / volume ls），
		// 无法用单一 argv 如实展示；它的真实命令与阶段由 backup 自身
		// 通过 observer 上报（见 backup.go 的注入点）。
		br, err := m.Backup(ctx, name, spec.IncludeVolumes)
		if err != nil {
			return -1, "", err
		}
		// 只回**相对**工作区路径：绝对路径暴露 Agent 磁盘布局，
		// 而用户需要的信息只是"备份放在哪儿"。
		rel := m.relativeBackupPath(br.Path)
		if br.VolumeFailed {
			// 卷备份失败必须让任务 failed：编排文件虽然备份成功，
			// 但用户点的是"备份（含数据卷）"，置 succeeded 就是误报。
			return -1, rel, errors.New(br.VolumeError)
		}
		return 0, rel, nil
	}
	return -1, "", ErrUnknownAction
}
