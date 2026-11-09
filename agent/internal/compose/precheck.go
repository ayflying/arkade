package compose

// precheck.go 是"写盘之前先确认这份配置能被 compose 接受"的公共能力。
//
// 为什么必须有这一步（真实风险，不是洁癖）：
// 若把一份 compose 解析不了的 compose.yml 写进项目目录，
// 这个项目之后**所有**命令都会失败——包括 down / stop / logs。
// 也就是说，用户本来只是想更新配置，结果连"把它停掉"都做不到，
// 只能登机器手工删文件。校验必须在正式文件被替换之前完成。
//
// 另一个恒定的约束：校验过程**绝不回显解析结果**。
// `docker compose config` 会把 ${PASSWORD} 展开成真实值再打印，
// 一旦把它塞进错误信息或日志，密钥就直接落到了 Hub 的审计记录和浏览器里。
// 因此这里只用 -q（安静模式），并且只回传我们自己写的说明文字。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// precheckTimeout 是校验用的超时。
//
// 用 TimeoutShort 而不是 TimeoutLong：config 只做解析与合并，不拉镜像、
// 不启容器，正常在亚秒级完成。给 30 秒已经非常宽裕；
// 若它卡住，说明环境本身有问题，继续等下去只会让用户以为界面卡死。
func precheckTimeout() time.Duration { return TimeoutShort }

// PrecheckResult 是校验结论。
type PrecheckResult struct {
	// OK 为真表示 compose 接受这份配置。
	OK bool
	// Reason 是给用户看的中文原因（OK 时为空）。
	//
	// 刻意**不包含** compose 的原始输出：那份输出可能含插值后的密钥。
	// 我们只说明"哪一类问题"，细节请用户在本机用 compose config 自查。
	Reason string
	// Skipped 表示本次没有真正执行校验（compose 不可用且允许跳过）。
	//
	// 生产环境**永远不允许**跳过：Applier 默认强制校验。
	// 这个字段只让调用方能在返回结果里如实说明"这次没校验"，
	// 而不是假装校验过了。
	Skipped bool
}

// PrecheckFunc 是可替换的校验实现（仅测试注入）。
//
// 为什么需要这个缝：单元测试环境里没有 docker，若强制跑 `config -q`
// 就会让所有"落盘顺序/文件状态"的测试全部失败——那不是被测逻辑坏了，
// 而是测试机器上没装 compose。
//
// 但"可替换"很容易变成"生产也随便跳过"，所以约束写死在类型上：
// Manager.applier 默认是"强制真实校验"，只有测试通过 SetPrecheckHook 覆盖。
type PrecheckFunc func(ctx context.Context, project, content, envContent string) PrecheckResult

// checkConfig 是落盘前的校验入口。
//
// 默认走真实校验（precheckProject）。测试可注入 hook，但**生产路径
// 没有任何"跳过"开关**：applier 为 nil 时就是真实校验，不用配置、
// 不用环境变量，避免"某次部署忘了打开校验"这种静默降级。
func (m *Manager) checkConfig(ctx context.Context, project, content, envContent string) PrecheckResult {
	if m.applier != nil {
		return m.applier(ctx, project, content, envContent)
	}
	return m.precheckProject(ctx, project, content, envContent)
}

// precheckProject 在**临时目录**里验证一份配置能否被 compose 解析。
//
// 关键点：
//   - 绝不在项目目录里试写：正式文件尚未更新，若先写再校验再回滚，
//     中间那一瞬正是本项目"绝不允许半写"所禁止的状态；
//   - 临时目录用完即删（defer），不残留可能含密钥的 .env；
//   - 只执行 `config -q`，不执行 up，也不打印任何解析结果。
//
// project 名沿用目标项目名：compose 会用它做项目级别的校验
// （例如项目名合法性），让校验结论与真实运行一致。
func (m *Manager) precheckProject(ctx context.Context, project, content, envContent string) PrecheckResult {
	tmpDir, err := os.MkdirTemp("", "arkade-precheck-")
	if err != nil {
		return PrecheckResult{Reason: "无法创建临时校验目录，已拒绝写入（避免落盘未校验的配置）"}
	}
	// 临时目录里会放一份 .env（可能含密钥），必须始终清理。
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfgPath := filepath.Join(tmpDir, composeFileName)
	if werr := os.WriteFile(cfgPath, []byte(content), 0o600); werr != nil {
		return PrecheckResult{Reason: "无法写入临时校验文件，已拒绝写入"}
	}
	if envContent != "" {
		if werr := writeSecretFile(filepath.Join(tmpDir, envFileName), envContent); werr != nil {
			return PrecheckResult{Reason: "无法写入临时环境变量文件，已拒绝写入"}
		}
	}

	// 直接指定 -f 临时文件，不依赖工作目录，也不需要碰项目目录。
	r := *m.runner
	r.Dir = tmpDir
	r.EnvFile = ""
	if envContent != "" {
		r.EnvFile = filepath.Join(tmpDir, envFileName)
	}
	// .env 的键同样要从继承环境剔除，否则校验看到的插值结果
	// 与将来真实运行时不同（真实运行会读 .env）。
	r.EnvExcludeKeys = ParseEnvKeys(envContent)

	// -q：只回报成功/失败，不打印展开后的配置。
	res := r.Run(ctx, project, precheckTimeout(), "-f", cfgPath, "config", "-q")
	if res.ExitCode == 0 && !res.TimedOut {
		return PrecheckResult{OK: true}
	}
	if res.TimedOut {
		return PrecheckResult{Reason: "配置校验超时，已拒绝写入"}
	}
	return PrecheckResult{Reason: "compose 无法解析这份配置，已拒绝写入（为避免泄漏环境变量值，不展示解析输出；可本机运行 docker compose config 查看详情）"}
}

// looksLikeComposeFailure 判断 compose 的输出是否表示"版本不支持某选项"。
//
// 只用于把"机器上的 compose 太旧"与"配置本身有问题"区分开，
// 让拒绝理由更可行动。匹配空串永远为 false。
func looksLikeComposeFailure(output string, needles ...string) bool {
	low := strings.ToLower(output)
	for _, n := range needles {
		if n != "" && strings.Contains(low, strings.ToLower(n)) {
			return true
		}
	}
	return false
}
