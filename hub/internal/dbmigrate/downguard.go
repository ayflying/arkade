package dbmigrate

// downguard.go：阻止"空 down 静默成功"。
//
// ============================ 要拦的是什么 ============================
// golang-migrate 的 source driver 在找不到某版本的 down 脚本时**不会报错**：
// 它把该版本的 Down 视为"读不到内容"，于是：
//
//   - 执行 0 条语句；
//   - 把版本号倒退到目标位置；
//   - 返回 nil 错误。
//
// 结果：`Steps(-1)` 返回成功，版本号从 10 变成 9，而 schema 一点没变。
// 任何依赖返回值判断的人都会以为回滚成功了——这比直接报错危险得多。
//
// ============================ 为什么在 source 层拦 ============================
// 试过两条错路：
//
//	1. 包装 database.Driver —— 它的 Run(io.Reader) 拿不到文件名，判不出方向；
//	2. 在调用 migrate.Steps() 之前检查 —— 但 Steps 是 *migrate.Migrate 的
//	   具体方法，无法从外部包装，调用方可以直接调用。
//
// 方向信息在 **source driver** 侧（ReadUp / ReadDown 两个独立方法），
// 因此包装 source 是唯一能覆盖所有回滚入口的位置：
// migrate 的 Down / Steps(负数) / Migrate(更低版本) 最终都会调到 ReadDown。
//
// ============================ 为什么不用"空 down 文件" ============================
// 另一种做法是给每个版本放一个空的 .down.sql。但那样 down 文件**存在**，
// migrate 会读到空内容并"成功"执行——问题原样复现，只是更隐蔽。
// 因此这里不放任何 down 文件，而是在 source 层直接返回错误。

import (
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/golang-migrate/migrate/v4/source"
)

// errDownUnsupported 表示本项目不提供回滚能力。
type errDownUnsupported struct {
	version uint
}

func (e errDownUnsupported) Error() string {
	return fmt.Sprintf(
		"拒绝回滚到 v%d：本项目没有提供 down 迁移脚本（旧迁移体系只增不改）。\n"+
			"golang-migrate 在缺少 down 脚本时会执行空脚本并推进版本号，"+
			"造成'已回滚'的假象而 schema 实际未变——因此这里直接报错。\n"+
			"如需回退，请人工确认数据影响后手工处理，不要依赖自动回滚",
		e.version)
}

// downGuardSource 包装 source.Driver，拦截所有 ReadDown。
type downGuardSource struct {
	source.Driver
}

// newDownGuardSource 用包装器套住原始 source。
func newDownGuardSource(inner source.Driver) source.Driver {
	return &downGuardSource{Driver: inner}
}

// ReadDown 一律拒绝：本项目没有 down 脚本。
func (g *downGuardSource) ReadDown(version uint) (io.ReadCloser, string, error) {
	return nil, "", errDownUnsupported{version: version}
}

// Open 透传并重新包装。
func (g *downGuardSource) Open(url string) (source.Driver, error) {
	inner, err := g.Driver.Open(url)
	if err != nil {
		return nil, err
	}
	return &downGuardSource{Driver: inner}, nil
}

// hasAnyDownScript 检查内嵌迁移源里是否存在 down 文件。
//
// 当前应为 false。若将来真的引入 down，这条守卫应当被移除或调整。
func hasAnyDownScript() (bool, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return false, fmt.Errorf("读取内嵌迁移目录失败: %w", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".down.sql") {
			return true, nil
		}
	}
	return false, nil
}
