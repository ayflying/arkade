package dbmigrate

// files.go：内嵌迁移文件的读取。
//
// ============================ 不做 SQL 切分 ============================
// 早期版本自写了一个"按分号切语句"的解析器，理由是"驱动一次只能执行一条"。
// **这个前提是错的**：已经实测确认，驱动支持多语句 Exec
// （整个迁移脚本一次执行即可）。
//
// 自造 SQL 解析器的问题在于它必然会漏：
//   - 分号出现在字符串字面量里（'a;b'）；
//   - 触发器体里的 BEGIN...END 内部带分号；
//   - 注释里带分号。
//
// 每一个漏判都可能把一条合法语句切坏，而错误信息会指向"SQL 语法错误"，
// 让人往完全错误的方向排查。既然驱动直接支持，就不该有这层。
//
// 现在 verifySchemaAt 会把**整个 up 脚本**一次 Exec 到内存库，
// 与 golang-migrate 执行真实迁移的方式完全一致——这还顺带消除了
// "重建路径与迁移路径不一致"的隐患。

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// legacyFile 是一个已读入的 up 迁移文件。
type legacyFile struct {
	version int
	name    string
	body    string
}

// legacyUpFiles 读取版本 <= upto 的全部 up 迁移文件，按版本升序返回。
func legacyUpFiles(upto int) ([]legacyFile, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("读取内嵌迁移目录失败: %w", err)
	}

	var out []legacyFile
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		v := 0
		if _, err := fmt.Sscanf(name, "%d", &v); err != nil {
			return nil, fmt.Errorf("迁移文件名 %q 版本号无法解析: %w", name, err)
		}
		if v > upto {
			continue
		}
		body, err := fs.ReadFile(migrationsFS, "migrations/"+name)
		if err != nil {
			return nil, fmt.Errorf("读取迁移文件 %s 失败: %w", name, err)
		}
		out = append(out, legacyFile{version: v, name: name, body: string(body)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// ReadMigration 读取内嵌迁移文件的原始内容（供 parity 测试使用）。
//
// ============================ 为什么导出这个 ============================
// store 包需要拿内嵌脚本与旧 `migrations` 变量做同源对照，
// 证明"导出是忠实的"。它需要**逐字读取**原始内容，
// 而不是从 legacyUpFiles 拿解析后的结构。
func ReadMigration(name string) (string, error) {
	body, err := fs.ReadFile(migrationsFS, "migrations/"+name)
	if err != nil {
		return "", fmt.Errorf("读取迁移文件 %s 失败: %w", name, err)
	}
	return string(body), nil
}

// ListUpScriptNames 返回全部 up 脚本文件名，按版本升序（供 parity 测试使用）。
func ListUpScriptNames() ([]string, error) {
	files, err := legacyUpFiles(int(^uint(0) >> 1)) // 取 int 最大值 = 不限版本
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.name)
	}
	return out, nil
}
