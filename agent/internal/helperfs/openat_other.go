//go:build !linux

package helperfs

import (
	"errors"
	"os"
)

// 非 Linux 平台上不提供安全打开：openat(O_NOFOLLOW|O_NONBLOCK) 的等价物
// 在这些平台不存在或语义不同。Do 已在入口按 runtime.GOOS fail closed，
// 这里的实现只是为了让编译通过，任何调用都会得到明确的不支持错误。
var errUnsafeOpenUnsupported = errors.New("该平台不支持安全的卷文件打开")

func safeOpenFile(*os.Root, string, int) (*os.File, os.FileInfo, error) {
	return nil, nil, errUnsafeOpenUnsupported
}

func safeCreateFile(*os.Root, string) (*os.File, error) {
	return nil, errUnsafeOpenUnsupported
}

func openDirNoFollow(*os.Root, string) (*os.File, error) {
	return nil, errUnsafeOpenUnsupported
}
