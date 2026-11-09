package link

import (
	"crypto/tls"
	"strconv"
)

// insecureTLSConfig 返回跳过证书校验的 TLS 配置。
//
// 安全边界说明：仅在配置项 insecure_tls=true 时使用，用于自签名证书的内网测试环境。
// 这里显式写出 InsecureSkipVerify 并注释原因，而不是让调用方随手写一行——
// 这样 grep 这个字段就能看清整个项目里有哪些地方放弃了证书校验。
// 生产环境应当由运维把 Hub 证书加入系统信任链（/etc/ssl/certs 或 CA bundle）。
func insecureTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec // 仅测试环境，见函数注释
	}
}

// itoa 是 strconv.Itoa 的极小别名，统一本包的整数转换入口。
func itoa(v int) string {
	return strconv.Itoa(v)
}
