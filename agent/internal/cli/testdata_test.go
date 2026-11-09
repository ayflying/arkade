package cli

import "os"

// writeFile 是测试用的写文件小工具，避免在测试文件里重复 os.WriteFile 调用。
func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
