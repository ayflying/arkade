package dbmigrate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"testing"
)

// releasedLockPath 已发布迁移的指纹清单，由 scripts/lock-migrations.py 维护。
const releasedLockPath = "migrations/released.lock"

// normalizedChecksum 按 LF 计算指纹。
//
// Windows 检出是 CRLF、CI 是 LF，而生产镜像在 Linux 构建；统一按 LF 计算，
// 本地与 CI 才能给出同一个结论。
func normalizedChecksum(body string) string {
	sum := sha256.Sum256(bytes.ReplaceAll([]byte(body), []byte("\r\n"), []byte("\n")))
	return hex.EncodeToString(sum[:])
}

func readReleasedLock(t *testing.T) map[int]string {
	t.Helper()
	raw, err := os.ReadFile(releasedLockPath)
	if err != nil {
		t.Fatalf("读取已发布迁移指纹清单失败: %v", err)
	}
	locked := map[int]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		version, digest, ok := strings.Cut(line, " ")
		if !ok {
			t.Fatalf("指纹清单行格式错误: %q", line)
		}
		parsed, err := strconv.Atoi(version)
		if err != nil {
			t.Fatalf("指纹清单版本号无法解析: %q", line)
		}
		locked[parsed] = strings.TrimSpace(digest)
	}
	return locked
}

// TestReleasedMigrationsStayImmutable 守住「已发布迁移不可修改」这条规则。
//
// 迁移一旦发布，数据库里就记着它的 SHA256；再改文件内容，存量实例启动时
// verifyRecorded 会报 checksum mismatch 并拒绝启动。
//
// 2026-11-24 的线上事故正是这么造成的：v1 的 seed 从 90 改成 30，所有从旧版本
// 升级的实例全部进入崩溃循环。当时唯一的错误就是「改了一个已经发布的迁移」。
//
// 新增迁移请追加版本号并运行 python scripts/lock-migrations.py。
func TestReleasedMigrationsStayImmutable(t *testing.T) {
	files, err := scripts()
	if err != nil {
		t.Fatalf("读取内嵌迁移失败: %v", err)
	}
	locked := readReleasedLock(t)
	known := map[int]bool{}
	for _, f := range files {
		known[f.version] = true
		want, ok := locked[f.version]
		if !ok {
			t.Fatalf("迁移 v%d（%s）还没有登记指纹：新增迁移后请运行 python scripts/lock-migrations.py", f.version, f.name)
		}
		if got := normalizedChecksum(f.body); got != want {
			t.Fatalf("已发布迁移 v%d（%s）的内容被改动了：\n  清单: %s\n  当前: %s\n"+
				"已发布的迁移不能修改——数据库里记着旧指纹，改了会让存量实例启动报 checksum mismatch。\n"+
				"请恢复原内容，并用新的版本号追加迁移来改变行为。", f.version, f.name, want, got)
		}
	}
	for version := range locked {
		if !known[version] {
			t.Fatalf("指纹清单里的迁移 v%d 不存在（被删除或改名了）：已发布的迁移不能删", version)
		}
	}
}
