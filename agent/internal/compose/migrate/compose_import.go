package migrate

// compose_import.go：把编排配置落盘到目标服务器（**不启动**）。
//
// ============================ 为什么不复用 up ============================
// up 的语义是"写配置并立刻启动"。迁移的最后一步要不要启动由
// start_after 决定，默认是 **false**（用户不勾就不启动）——
// 若复用 up，默认路径就得绕过它自己写文件，
// 那条路径会漏掉 up 内部已有的哈希校验、原子提交、env 写入保护。
//
// 因此这里提供一个"只落盘"的实现，**复用同一个 Workspace.CommitProject**
// （它本身就是原子写 + 失败回滚），只是不调用 compose up。
//
// ============================ 哈希必须两端一致 ============================
// 目标端落盘前会自己算一次哈希并与调用方给的值比对：
//   - 不一致 → 说明传输过程损坏，或两端算法不一致，**绝不能落盘**；
//     覆盖一份内容不确定的配置，比不迁移更糟；
//   - 一致   → 正常落盘，并把目标端算出的哈希回传，供 Hub 侧入库。
//
// 算法与 Hub 的 composeProjectHash 完全一致（含长度前缀与分隔符），
// 任何一个字节不同都会让 drift 判定永远为真。

import (
	"arkade/protocol/composehash"
	"errors"
	"fmt"
	"strings"
)

// 哈希长度：与 Hub 侧 composeHashHexLen 一致（sha256 十六进制前 16 位）。
const composeHashHexLen = 16

// Errors。
var (
	// ErrComposeExists 目标上已存在同名编排。
	ErrComposeExists = errors.New("目标服务器上已存在同名编排")
	// ErrComposeHashMismatch 传过来的哈希与内容不符。
	ErrComposeHashMismatch = errors.New("编排配置哈希与内容不一致")
	// ErrComposeContentInvalid 内容不合法。
	ErrComposeContentInvalid = errors.New("编排配置不合法")
	// ErrComposeNameInvalid 项目名不合法。
	ErrComposeNameInvalid = errors.New("编排项目名不合法")
)

// CompositionImporter 把编排配置落到本机。
//
// 用接口而不是直接依赖 *compose.Manager：本包不该反过来依赖
// compose 包（那会形成 compose → migrate → compose 的循环）。
// 由 handler 层注入一个薄适配器。
type CompositionImporter interface {
	// Exists 判断本机是否已有该编排。
	Exists(name string) bool
	// Commit 原子落盘配置与 env，返回错误。
	Commit(name, content, env string) error
}

// ComposeArchive 是导入编排所需的全部内容。
type ComposeArchive struct {
	Name   string
	Config string
	Env    string
	// ExpectHash 是调用方算的哈希；为空表示不校验。
	ExpectHash string
	// Overwrite 允许覆盖已存在的同名编排。
	//
	// **迁移流程永远传 false**：产品决策是"目标已有同名编排则拒绝并提示改名"。
	Overwrite bool
}

// ComposeImportResult 是导入结果。
type ComposeImportResult struct {
	Name          string
	ConfigHash    string
	AlreadyExists bool
}

// ImportComposition 落盘编排配置。
func ImportComposition(importer CompositionImporter, req ComposeArchive) (ComposeImportResult, error) {
	name := strings.TrimSpace(req.Name)
	if !validComposeName(name) {
		return ComposeImportResult{}, fmt.Errorf("%w: %q", ErrComposeNameInvalid, name)
	}
	if strings.TrimSpace(req.Config) == "" {
		return ComposeImportResult{}, fmt.Errorf("%w: 内容不能为空", ErrComposeContentInvalid)
	}
	// 空字节会破坏 YAML 与后续的哈希比对，在入口就拒绝。
	if strings.ContainsRune(req.Config, '\x00') || strings.ContainsRune(req.Env, '\x00') {
		return ComposeImportResult{}, fmt.Errorf("%w: 内容包含非法空字符", ErrComposeContentInvalid)
	}

	// ============================ 哈希先校验，再落盘 ============================
	// 顺序不能反：落盘之后再发现哈希不符，目标上就已经有了一份错误配置。
	hash := ComposeProjectHash(req.Config, req.Env)
	if req.ExpectHash != "" && !equalHex(req.ExpectHash, hash) {
		return ComposeImportResult{}, ErrComposeHashMismatch
	}

	if importer.Exists(name) && !req.Overwrite {
		// 不覆盖是产品决策：覆盖会毁掉目标上正在运行的编排配置。
		return ComposeImportResult{Name: name, ConfigHash: hash, AlreadyExists: true}, ErrComposeExists
	}

	if err := importer.Commit(name, req.Config, req.Env); err != nil {
		return ComposeImportResult{}, err
	}
	return ComposeImportResult{Name: name, ConfigHash: hash}, nil
}

// ComposeProjectHash 复用共享项目协议，迁移与普通编排使用完全相同的哈希。
func ComposeProjectHash(content, envContent string) string {
	return composehash.Project(content, envContent)
}

// validComposeName 校验编排项目名。
//
// 规则 [a-z0-9][a-z0-9_-]*，与 Hub 侧 validComposeName 完全一致。
//
// ============================ 为什么这么严 ============================
// 项目名会直接拼进本机文件路径（<data_dir>/stacks/<name>/compose.yml）
// 以及 docker compose -p 参数。一旦允许 "."、".."、"/" 或空格，
// 就能构造出 ../ 越界写到数据目录之外，甚至注入额外的命令行参数。
// 逐字符白名单比正则黑名单更难写错。
func validComposeName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
			// 首字符不能是分隔符：否则 "." / "-" 开头的目录名在某些
			// 系统里会被当成选项或隐藏文件。
			if i == 0 {
				return false
			}
		default:
			// 大写、点、斜杠、空格等一律拒绝。
			return false
		}
	}
	return true
}
