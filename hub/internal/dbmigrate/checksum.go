package dbmigrate

// checksum.go：迁移脚本的校验和基线。
//
// ============================ 为什么需要校验和 ============================
// 迁移的一条铁律是"已发布的脚本只增不改"。但这条规矩靠人记不住：
// 有人为了修一个小问题，直接改了 v3 的 SQL——本地测试全过，
// 新库建出来是对的，而**老库还是旧结构**。两边从此永久分叉，
// 而且没有任何地方会报警。
//
// 结构比对能发现分叉，但那是"事后"：等它发现时，老库已经启动失败了，
// 运维面对的是一个打不开的程序，和一句"结构不一致"。
//
// 校验和基线把发现时机**提前到脚本被改的那一刻**：
//
//	checksums.txt 里记着每个版本的 SHA256。
//	脚本一改，哈希就对不上，启动直接报"v3 的脚本被修改过"。
//
// 这比结构比对更直接：它指出的是**原因**，而不是症状。
//
// ============================ 校验和的保护边界（必读） ============================
// 当前实现只比对 **内嵌脚本** 与 **内嵌基线**。这能防住：
//
//	✔ 有人改了 .up.sql 但忘了更新基线 → 启动立刻报"v3 的脚本被修改过"。
//
// 但它**防不住**这一种：
//
//	✘ 有人同时改了已发布的 .up.sql 和 checksums.txt 里的对应哈希。
//	  此时内嵌脚本与内嵌基线"自洽"，检查通过。而已部署的库是用旧脚本建的，
//	  结构从此与新库分叉。
//
// 为什么这一点必须写清楚：内嵌基线是**随代码一起分发**的，
// 它和被检查的脚本在同一个版本库里，改脚本的人同样能改基线。
// 这不是实现缺陷，而是"离线校验"的固有边界。
//
// 真正要闭合这个缺口，需要**把已执行脚本的哈希记进数据库**：
// 每个版本执行时写入其哈希，下次启动与记录比对。那是 schema 演进的一部分，
// 需要新增一张表，属于后续阶段的工作，不在当前范围内。
//
// 现阶段靠两道防线覆盖：
//  1. 本文件的内嵌基线——挡住"改了脚本忘了改基线"这种最常见的手误；
//  2. CI 检查 old-hash-immutable：任何提交如果修改了已存在的
//     migrations/*.up.sql 或 checksums.txt 中旧版本的哈希，直接失败。
//     （CI 规则由 Lead 添加，见交接说明。）
//
// 数据库侧的结构比对（verifySchemaAt）是第三道兜底：
// 即便脚本被悄悄改过，已部署的库在启动时会因为结构对不上而被拒绝，
// 不会带着错误结构继续跑。
//
// ============================ 基线怎么更新 ============================
// 只有在**确实要新增版本**时才追加新行；已有版本的哈希**永不更新**。
// 若基线缺失（首次引入），运行：
//
//	PROBE_WRITE_CHECKSUMS=1 go test ./internal/dbmigrate -run TestWriteChecksumBaseline
//
// 生成后必须人工 review diff，确认**只有新增行**、没有旧版本哈希变化。
// 生成工具本身也会拒绝"旧版本哈希发生变化"的覆盖（见 checksum_gen_test.go）。

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// checksumBaselineFS 内嵌校验和基线文件。
//
//go:embed migrations/checksums.txt
var checksumBaselineFS embed.FS

// baselinePath 是基线文件在 embed 中的路径。
const baselinePath = "migrations/checksums.txt"

// loadBaseline 读取校验和基线，返回 version -> sha256 的映射。
func loadBaseline() (map[int]string, error) {
	raw, err := fs.ReadFile(checksumBaselineFS, baselinePath)
	if err != nil {
		return nil, fmt.Errorf("读取校验和基线失败（若为首次引入请先生成基线）: %w", err)
	}

	out := make(map[int]string)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// 格式：<version>  <sha256>
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("校验和基线格式错误（应为 '<version> <sha256>'）: %q", line)
		}
		var v int
		if _, err := fmt.Sscanf(fields[0], "%d", &v); err != nil {
			return nil, fmt.Errorf("校验和基线版本号无法解析: %q", line)
		}
		out[v] = fields[1]
	}
	return out, nil
}

// computeChecksums 计算内嵌迁移脚本（**仅 up**）的 SHA256。
//
// ============================ 只算 up 是有意的 ============================
// 本项目不提供 down 脚本（见 dbmigrate.go 的说明）。
// 若将来加了 down，这里也要一并纳入，否则改 down 不会报警。
func computeChecksums() (map[int]string, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("读取内嵌迁移目录失败: %w", err)
	}

	out := make(map[int]string)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		var v int
		if _, err := fmt.Sscanf(name, "%d", &v); err != nil {
			return nil, fmt.Errorf("迁移文件名 %q 版本号无法解析: %w", name, err)
		}
		body, err := fs.ReadFile(migrationsFS, "migrations/"+name)
		if err != nil {
			return nil, fmt.Errorf("读取 %s 失败: %w", name, err)
		}
		sum := sha256.Sum256(body)
		out[v] = hex.EncodeToString(sum[:])
	}
	return out, nil
}

// verifyChecksums 比对当前脚本与基线的校验和。
//
// ============================ 三种不一致都要报 ============================
//   - 基线与脚本都有，但哈希不同  → 脚本被改过（最严重）；
//   - 脚本有而基线没有            → 新增版本但忘了更新基线；
//   - 基线有而脚本没有            → 脚本被删了。
//
// 三种都拒绝启动：任何一种都会让不同时期的库产生分叉。
func verifyChecksums() error {
	baseline, err := loadBaseline()
	if err != nil {
		return err
	}
	current, err := computeChecksums()
	if err != nil {
		return err
	}

	// 版本号排序，保证错误信息稳定（便于测试断言与人工排查）。
	var versions []int
	for v := range current {
		versions = append(versions, v)
	}
	for v := range baseline {
		if _, ok := current[v]; !ok {
			versions = append(versions, v)
		}
	}
	sort.Ints(versions)

	var problems []string
	for _, v := range versions {
		cur, hasCur := current[v]
		base, hasBase := baseline[v]
		switch {
		case hasCur && !hasBase:
			problems = append(problems, fmt.Sprintf(
				"v%d 存在于脚本中但基线里没有（新增版本后请更新校验和基线）", v))
		case !hasCur && hasBase:
			problems = append(problems, fmt.Sprintf(
				"v%d 存在于基线中但脚本已被删除", v))
		case cur != base:
			problems = append(problems, fmt.Sprintf(
				"v%d 的脚本被修改过：基线 %s，当前 %s", v, shortHash(base), shortHash(cur)))
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf(
		"迁移脚本校验和不匹配（共 %d 处），拒绝启动：\n  - %s\n"+
			"迁移脚本一经发布就不允许修改：改了会让已部署的库与新库结构永久分叉。\n"+
			"请改为新增一个版本（追加 v%d 而不是改旧的），或确认是误改后恢复原内容。",
		len(problems), strings.Join(problems, "\n  - "), highestVersion(current)+1)
}

// shortHash 截断哈希用于错误信息显示。
func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12] + "..."
	}
	return h
}

// highestVersion 返回映射中的最大版本号（空映射返回 0）。
func highestVersion(m map[int]string) int {
	max := 0
	for v := range m {
		if v > max {
			max = v
		}
	}
	return max
}

// renderBaseline 生成基线文件内容（供生成工具使用）。
func renderBaseline(checksums map[int]string) string {
	var versions []int
	for v := range checksums {
		versions = append(versions, v)
	}
	sort.Ints(versions)

	var b strings.Builder
	b.WriteString("# 迁移脚本校验和基线 —— 由工具生成，请勿手改\n")
	b.WriteString("#\n")
	b.WriteString("# 每一行是 <版本号> <up 脚本的 SHA256>。\n")
	b.WriteString("# 已有版本的哈希**永不更新**：改了它等于宣布'允许修改已发布的迁移'，\n")
	b.WriteString("# 而那会让已部署的库与新库结构永久分叉。新增版本时只追加新行。\n")
	b.WriteString("#\n")
	b.WriteString("# 重新生成： go test ./internal/dbmigrate -run TestWriteChecksumBaseline\n\n")
	for _, v := range versions {
		fmt.Fprintf(&b, "%d  %s\n", v, checksums[v])
	}
	return b.String()
}
