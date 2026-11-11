package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// hashFile 计算文件的大小与 sha256（一块一块读，不整文件进内存）。
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("打开卷归档失败: %w", err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	n, cerr := io.Copy(h, f)
	if cerr != nil {
		return "", 0, fmt.Errorf("读取卷归档失败: %w", cerr)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// filterDockerError 把 docker 的原始输出转成中文说明。
//
// ============================ 必须过滤 ============================
// docker 的错误文本常含镜像地址、registry 认证细节、内网主机名。
// 这些信息会随"任务失败原因"流到 Hub、浏览器与日志里。
// 因此这里只保留**可操作**的部分，其余一律替换成固定文案。
func filterDockerError(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "No such volume"):
		return fmt.Errorf("源服务器上不存在该数据卷")
	case strings.Contains(msg, "permission denied"):
		return fmt.Errorf("无权限读取该数据卷（请检查 Agent 的 docker 权限）")
	case strings.Contains(msg, "no space left"):
		return fmt.Errorf("磁盘空间不足，无法写出卷归档")
	case strings.Contains(msg, "pull access denied"), strings.Contains(msg, "not found"):
		// 打包用的 alpine 镜像拉不到（离线环境）。
		return fmt.Errorf("无法获取打包用的临时镜像，请确认服务器可访问镜像源")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("打包数据卷超时或被取消")
	}
	return fmt.Errorf("打包数据卷失败（请查看服务器上的 docker 日志）")
}
