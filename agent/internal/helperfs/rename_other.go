//go:build !linux

package helperfs

import "os"

// renameNoReplace 在非 Linux 平台**明确不支持**。
//
// 首批 helper 只在 Linux 容器里跑。其它平台没有 renameat2 的
// RENAME_NOREPLACE 等价物，也没有可靠的方式在不覆盖的前提下原子改名。
// 与其给一个"先检查再改"的有竞态实现（会静默覆盖并发目标），
// 不如直接 fail closed——让调用方得到明确的不支持错误。
func renameNoReplace(*os.Root, string, string) error {
	return ErrNoAtomicRename
}
