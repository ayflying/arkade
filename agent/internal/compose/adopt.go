package compose

// adopt.go 实现"把已发现的外部编排显式纳管"（compose.preview / compose.adopt）。
//
// ============================ 边界（这些边界就是本功能的安全定义） ============================
//
//  1. **只读引擎与工作区，不读宿主文件**。Agent 跑在容器里，
//     宿主机的编排目录它根本读不到。凡是"Agent 自己去读宿主 .env / compose.yml"
//     的设计在真实部署里都不成立，因此内容必须由用户提供。
//
//  2. **绝不执行 up/down/pull/stop**。外部项目正在跑生产负载，
//     导入的正确动作是"把配置存下来并声明归属"，任何"顺手 up 一下"
//     都会重建容器，那是用户没有要求的破坏。
//
//  3. **绝不写外部原始 compose**。只写管理区 <data_dir>/stacks/<name>/。
//     用户机器上那份原始文件属于别人的管辖权，我们连碰都不碰。
//
//  4. **内容必须与哈希一致**。expected_content_hash 由 Hub 计算、用户确认过，
//     落盘前校验，避免"预览看到的"和"实际导入的"不是同一份东西。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PreviewInput 是预检输入。
type PreviewInput struct {
	Name       string
	Content    string
	EnvContent string
}

// PreviewResult 是预检结论。
type PreviewResult struct {
	Name            string
	Discovered      bool
	EngineExists    bool
	WorkspaceExists bool
	Source          string
	Running         int
	Total           int
	// CanImport 是唯一开关：发现外部同名项目 且 工作区无冲突 且 内容齐备 且 无阻塞项。
	CanImport  bool
	ConfigHash string
	Blockers   []string
	Warnings   []string
}

// Preview 判断某个外部编排能否被导入。
//
// 这个函数会**真实执行安全校验**（YAML 扫描 + 交给 compose 校验配置），
// 所以 can_import=true 是一个有依据的结论，不是"材料看起来齐了"。
//
// 它虽然只读，仍**持项目锁**：判定要读工作区已有内容并与本次内容比较，
// 若与并发的 Apply/Adopt 交错，就可能得出"内容相同"的错误结论，
// 而这个结论会被前端拿去直接执行导入。只读 + 持锁时间短，代价可接受。
func (m *Manager) Preview(ctx context.Context, in PreviewInput) (PreviewResult, error) {
	n, err := cleanName(in.Name)
	if err != nil {
		return PreviewResult{}, err
	}

	var res PreviewResult
	err = m.withProject(ctx, n, func() error {
		var perr error
		res, perr = m.previewLocked(ctx, n, in)
		return perr
	})
	return res, err
}

// previewLocked 是 Preview 的加锁内实现（调用方必须已持有项目锁）。
func (m *Manager) previewLocked(ctx context.Context, n string, in PreviewInput) (PreviewResult, error) {
	res := PreviewResult{Name: n}

	// 事实：引擎与工作区两侧分别是否存在。
	_, engineFound := m.findExternal(ctx, n)
	res.EngineExists = engineFound
	res.WorkspaceExists = m.ws.Exists(n)
	switch {
	case res.EngineExists && res.WorkspaceExists:
		res.Source = "both"
	case res.EngineExists:
		res.Source = "engine"
	case res.WorkspaceExists:
		res.Source = "workspace"
	default:
		res.Source = "none"
	}
	res.Discovered = res.EngineExists

	if ext, ok := m.findExternal(ctx, n); ok {
		res.Running = ext.Running
		res.Total = ext.Total
	}

	// 结论逐步收紧，每条阻塞都说清"该怎么办"。
	content := strings.TrimSpace(in.Content)
	envGiven := in.EnvContent != ""

	if content == "" {
		res.Blockers = append(res.Blockers,
			"需要用户提供 compose 文件内容：Agent 运行在容器内，读不到宿主机上的编排文件")
	}

	if !res.EngineExists {
		res.Blockers = append(res.Blockers,
			fmt.Sprintf("引擎里没有名为 %q 的编排项目，没有可导入的对象", n))
	}

	// 工作区已存在：允许"同内容重试"，拒绝"不同内容"。
	if res.WorkspaceExists {
		same, sameErr := m.sameAsWorkspace(n, content, in.EnvContent)
		switch {
		case sameErr != nil:
			res.Blockers = append(res.Blockers,
				"无法读取本机已有的同名编排以判断是否同一份内容，请稍后重试")
		case same:
			res.Warnings = append(res.Warnings,
				"该编排此前已导入且内容相同；本次导入不会改动正在运行的服务")
		default:
			res.Blockers = append(res.Blockers,
				"本机工作区里已存在同名编排且内容不同；请使用「更新」而不是「导入」")
		}
	}

	// 安全扫描：相对路径与外部文件依赖。
	if content != "" {
		for _, b := range ScanImportSafety(content) {
			res.Blockers = append(res.Blockers, b.String())
		}
		if envGiven {
			if err := ValidateEnvContent(in.EnvContent); err != nil {
				res.Blockers = append(res.Blockers, "环境变量内容不合法："+err.Error())
			}
		}
		if len(res.Blockers) == 0 {
			// 只有没有其它阻塞时才值得跑一次真实校验（它要起一个 compose 进程）。
			pc := m.checkConfig(ctx, n, in.Content, in.EnvContent)
			if !pc.OK {
				res.Blockers = append(res.Blockers, pc.Reason)
			}
		}
		res.ConfigHash = HashProject(in.Content, in.EnvContent)
	}

	// 未纳管提示：让用户明白"导入"是在改变归属关系。
	if res.EngineExists && !res.WorkspaceExists {
		res.Warnings = append(res.Warnings,
			"该编排当前由 Arkade 之外的方式管理；导入后仍不会自动改动它的容器")
	}

	res.CanImport = len(res.Blockers) == 0
	return res, nil
}

// AdoptInput 是导入输入。
type AdoptInput struct {
	Name       string
	Content    string
	EnvContent string
	ExpectHash string
	Confirmed  bool
}

// AdoptResult 是导入结果。
type AdoptResult struct {
	Name       string
	ConfigHash string
	Adopted    bool
	EnvWritten bool
}

// Adopt 把外部编排显式纳管：只落盘，不启动。
//
// 重复调用是安全的（幂等）：内容相同视为重试并成功，内容不同则拒绝。
// 这样 Hub 侧"导入失败保留记录、允许重试"的流程能真正走通，
// 而不会退化成"重试即接管"。
func (m *Manager) Adopt(ctx context.Context, in AdoptInput) (AdoptResult, error) {
	n, err := cleanName(in.Name)
	if err != nil {
		return AdoptResult{}, err
	}

	// **持项目锁**：与 Apply 同一把锁。
	//
	// 理由：Adopt 会写管理区并与工作区比较内容，
	// 若与并发的 Apply 交错，就会出现"导入判定为内容相同所以不写，
	// 而另一个请求恰好把它改掉了"——结果是导入报告成功但实际没生效。
	// 同名预检（Preview）也取同一把锁，见下。
	var out AdoptResult
	err = m.withProject(ctx, n, func() error {
		var aerr error
		out, aerr = m.adoptLocked(ctx, n, in)
		return aerr
	})
	return out, err
}

// adoptLocked 是 Adopt 的加锁内实现（调用方必须已持有项目锁）。
func (m *Manager) adoptLocked(ctx context.Context, n string, in AdoptInput) (AdoptResult, error) {
	// 显式确认：用布尔而不是"省略即默认"，
	// 让"误调接口"不可能完成导入。
	if !in.Confirmed {
		return AdoptResult{}, errors.New("导入需要显式确认（confirmation=true）：该操作会让 Arkade 接管一个正在运行的外部编排")
	}

	if strings.TrimSpace(in.Content) == "" {
		return AdoptResult{}, errors.New("compose 内容不能为空：Agent 无法读取宿主机上的编排文件，内容必须由用户提供")
	}
	if err := ValidateEnvContent(in.EnvContent); err != nil {
		return AdoptResult{}, err
	}

	localHash := HashProject(in.Content, in.EnvContent)
	if in.ExpectHash != "" && in.ExpectHash != localHash {
		return AdoptResult{}, fmt.Errorf(
			"内容哈希与 Hub 声明不一致（本机算得 %s，Hub 声明 %s），已拒绝导入，本机文件未被改动",
			localHash, in.ExpectHash)
	}

	// 目标：引擎里有同名项目（否则没有可导入的对象）。
	if _, ok := m.findExternal(ctx, n); !ok {
		return AdoptResult{}, fmt.Errorf(
			"引擎里没有名为 %q 的编排项目，没有可导入的对象", n)
	}

	// 工作区冲突判定。
	if m.ws.Exists(n) {
		same, sameErr := m.sameAsWorkspace(n, in.Content, in.EnvContent)
		if sameErr != nil {
			return AdoptResult{}, fmt.Errorf("无法判断本机已有同名编排是否相同: %w", sameErr)
		}
		if !same {
			return AdoptResult{}, fmt.Errorf(
				"本机工作区里已存在同名编排 %q 且内容不同，请使用「更新」而不是「导入」", n)
		}
		// 同内容重试：什么都不写，直接报告成功。
		// 不重写文件是有意的——重写会把 mtime 改掉，
		// 让我们失去"这份文件是什么时候导入的"这一信息。
		return AdoptResult{
			Name:       n,
			ConfigHash: localHash,
			Adopted:    true,
			EnvWritten: m.ws.EnvExists(n),
		}, nil
	}

	// 安全扫描必须在写盘前：相对路径的编排导入后行为会变，
	// 不能"先导入再说"。
	if blockers := ScanImportSafety(in.Content); len(blockers) > 0 {
		msgs := make([]string, 0, len(blockers))
		for _, b := range blockers {
			msgs = append(msgs, b.String())
		}
		return AdoptResult{}, fmt.Errorf("该编排包含导入后无法保证正确的引用，已拒绝导入：%s",
			strings.Join(msgs, "；"))
	}

	// 落盘前让 compose 校验一遍（与 ApplyProject 同一套规则）：
	// 导入一份解析不了的配置，会让这个项目之后所有命令都失败。
	pc := m.checkConfig(ctx, n, in.Content, in.EnvContent)
	if !pc.OK {
		return AdoptResult{}, errors.New(pc.Reason)
	}

	// 只写管理区。用 CommitProject 保证 compose.yml 与 .env 一起提交，
	// 不会留下半写状态。
	_, envPath, cerr := m.ws.CommitProject(n, in.Content, in.EnvContent)
	if cerr != nil {
		return AdoptResult{}, cerr
	}

	return AdoptResult{
		Name:       n,
		ConfigHash: localHash,
		Adopted:    true,
		EnvWritten: envPath != "",
	}, nil
}

// findExternal 在已发现的外部项目里查找指定名字。
//
// 找不到不算错误：没有外部项目是"不能导入"的正常原因，不是故障。
func (m *Manager) findExternal(ctx context.Context, name string) (ExternalProject, bool) {
	if m.labelSource == nil {
		return ExternalProject{}, false
	}
	found, err := DiscoverExternal(ctx, m.labelSource)
	if err != nil {
		return ExternalProject{}, false
	}
	for _, p := range found {
		if p.Name == name {
			return p, true
		}
	}
	return ExternalProject{}, false
}

// sameAsWorkspace 判断本机已有编排与给定内容是否完全一致（含 .env）。
//
// 用哈希比较而不是逐字段比较：前者与"配置是否一致"的口径完全相同，
// 不会出现"哈希说不同但字段都一样"这种两套判断打架的情况。
func (m *Manager) sameAsWorkspace(name, content, envContent string) (bool, error) {
	cfgPath, err := m.ws.ComposePath(name)
	if err != nil {
		return false, err
	}
	existing, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, fmt.Errorf("编排文件不存在")
		}
		return false, err
	}
	existingEnv, err := m.ws.ReadEnv(name)
	if err != nil {
		return false, err
	}
	return HashProject(string(existing), existingEnv) == HashProject(content, envContent), nil
}

// workspaceProjectHash 供外部（handler）读取某个项目的当前哈希。
func (m *Manager) workspaceProjectHash(name string) string {
	n, err := cleanName(name)
	if err != nil {
		return ""
	}
	cfgPath, err := m.ws.ComposePath(n)
	if err != nil {
		return ""
	}
	return m.projectHash(n, filepath.Join(filepath.Dir(cfgPath), composeFileName))
}
