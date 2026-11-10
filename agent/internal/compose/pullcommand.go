package compose

// pullcommand.go：命令展示串的生成与脱敏器构造。
//
// 两者放一起的理由：它们都在回答同一个问题——"这条命令和它的输出里，
// 哪些是用户该看到的，哪些必须挡住"。分开写容易出现
// "展示用一套路径规则、脱敏用另一套"的偏差。

import (
	"errors"
	"os"
	"strings"
)

// ============================ 显示的是"真的执行了什么" ============================
//
// 要求是"真实命令"，因此这里显示**实际使用的绝对路径**（--env-file 的真实位置），
// 而不是一个好看但可能指向别处的相对路径。
//
// 注意**不要**声称它可以原样复制执行：compose 会按 cmd.Dir（项目目录）去读
// compose.yml，而单看这一行并不知道工作目录。工作目录由另一个字段
// working_dir 单独给出（见 pullWorkDir）。
//
// 路径不是密码；密码在 .env 的**内容**里，而那从不展示。
func (m *Manager) pullCommandDisplay(name string) string {
	r := m.runnerFor(name)
	if dir, err := m.ws.ProjectDir(name); err == nil {
		r.Dir = dir
	}
	argv := append([]string{r.binary()}, composeArgs(name, r.EnvFile, "pull")...)
	return shellQuoteArgv(argv)
}

// pullWorkDir 返回命令的**真实工作目录**（compose 在此目录下找 compose.yml）。
//
// 单独一个字段而不是塞进 command：把 "cd X && ..." 拼进命令行会让人以为
// 我们在 shell 里执行它，而实际始终是 exec(argv) + cmd.Dir。
func (m *Manager) pullWorkDir(name string) string {
	dir, err := m.ws.ProjectDir(name)
	if err != nil {
		return ""
	}
	return dir
}

// shellQuoteArgv 用 shell 引号风格把 argv 拼成一行**仅供展示**的字符串。
//
// 注意：这只用于显示。执行始终走 exec(argv)，不经过 shell，
// 因此这里的引号不会引入任何注入面。
func shellQuoteArgv(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, a := range argv {
		parts = append(parts, shellQuoteOne(a))
	}
	return strings.Join(parts, " ")
}

// shellQuoteOne 对单个参数做展示用引号包裹。
func shellQuoteOne(s string) string {
	if s == "" {
		return "''"
	}
	// 安全字符直接输出，便于阅读。
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' || strings.ContainsRune("-_.=/:@,", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// buildRedactor 为某个项目构造脱敏器。
//
// **fail-closed**：只要读不到或解析不了 .env，就返回错误，
// 调用方必须据此隐藏全部输出（而不是跳过脱敏继续跑）。
func (m *Manager) buildRedactor(name string) (*redactor, error) {
	r := m.runnerFor(name)
	if dir, derr := m.ws.ProjectDir(name); derr == nil {
		r.Dir = dir
	}
	return m.buildRedactorWith(r)
}

// buildRedactorWith 用**给定的 runner** 构造脱敏器。
//
// 存在的理由：调用方已经为执行构造好了一个 runner，脱敏必须读**同一个**
// runner 指向的 .env。若这里再调一次 runnerFor，排队期间并发的 Apply
// 可能让两次读到的 .env 不同——遮的和执行的不是同一份。
func (m *Manager) buildRedactorWith(r *Runner) (*redactor, error) {
	// 项目名从 .env 路径反推不可靠，因此由调用方在 name 字段里带上。
	name := r.ProjectName
	secrets := make([]string, 0, 16)

	// 继承环境里的敏感值：compose 插值优先级 shell > .env，
	// 因此这些值同样可能出现在输出里。
	secrets = append(secrets, inheritedSecrets(os.Environ())...)

	envPath, err := m.ws.EnvPath(name)
	if err != nil {
		return nil, err
	}
	content, rerr := os.ReadFile(envPath)
	switch {
	case rerr == nil:
		values, ok := envValues(string(content))
		if !ok {
			return nil, errors.New("项目 .env 格式无法解析")
		}
		secrets = append(secrets, values...)
	case errors.Is(rerr, os.ErrNotExist):
		// 项目没有 .env：这是正常情况，**不是**提前收工的理由。
		// 继续往下走内联扫描——编排正文里的 environment: 同样会泄。
	default:
		// 文件存在但读不了（权限/IO）：无法确认里面有什么，
		// 不能假设"没有敏感值"→ fail-closed。
		return nil, rerr
	}

	// 编排正文里**内联写的**敏感值。
	//
	// 只遮 .env 是不够的：environment: DB_PASSWORD: hunter2 这种写法，
	// compose 解析出错时会把整行原文打到 stderr，而 .env 脱敏器完全不知情。
	// 读不到正文**不能**跳过：compose 解析失败时回显的正是这份正文里的行，
	// 跳过等于在最需要遮的场景（配置有错）关掉保护。
	cfg, cerr := m.readProjectConfig(name)
	if cerr != nil {
		return nil, cerr
	}
	inline, yerr := composeInlineSecrets(cfg)
	if yerr != nil {
		// 正文自身解析失败：compose 会回显原文行，我们无法预判会回显什么，
		// 因此 fail-closed（不执行命令，只给通用文案）。
		return nil, errors.New("编排内容无法解析，已停止以避免泄露配置内容")
	}
	secrets = append(secrets, inline...)
	return newRedactor(secrets), nil
}

// readProjectConfig 读取项目的编排正文（用于提取内联敏感值）。
func (m *Manager) readProjectConfig(name string) (string, error) {
	return m.ws.Read(name)
}
