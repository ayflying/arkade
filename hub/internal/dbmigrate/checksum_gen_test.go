package dbmigrate

// checksum_gen_test.go：生成校验和基线的工具。
//
// ============================ 这是"工具"不是"测试" ============================
// 它平时跳过，只在需要生成基线时运行：
//
//	go test ./internal/dbmigrate -run TestWriteChecksumBaseline
//
// 生成后**必须人工 review diff**，确认只有新增行、没有旧版本哈希变化。
// 若有旧版本哈希变化，说明有人改了已发布的迁移脚本——那是必须回退的改动，
// 而不是"更新一下基线"。

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteChecksumBaseline 生成/更新校验和基线文件。
func TestWriteChecksumBaseline(t *testing.T) {
	if os.Getenv("PROBE_WRITE_CHECKSUMS") != "1" {
		t.Skip("仅在 PROBE_WRITE_CHECKSUMS=1 时生成基线")
	}

	checksums, err := computeChecksums()
	if err != nil {
		t.Fatalf("计算校验和失败: %v", err)
	}

	// 已有基线时，先检查有没有旧版本的哈希变化——这种情况必须拒绝覆盖，
	// 否则这个工具就成了"悄悄允许修改已发布迁移"的后门。
	if existing, err := loadBaseline(); err == nil {
		for v, oldSum := range existing {
			if newSum, ok := checksums[v]; ok && oldSum != newSum {
				t.Fatalf("v%d 的脚本内容已被修改（基线 %s，当前 %s）；\n"+
					"已发布的迁移脚本不允许修改，请恢复原内容而不是更新基线",
					v, shortHash(oldSum), shortHash(newSum))
			}
		}
	}

	outPath := filepath.Join("migrations", "checksums.txt")
	if err := os.WriteFile(outPath, []byte(renderBaseline(checksums)), 0o644); err != nil {
		t.Fatalf("写入基线失败: %v", err)
	}
	t.Logf("已写入 %s（共 %d 个版本）", outPath, len(checksums))
}
