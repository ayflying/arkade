package runtime

// parse_time.go 放时间解析这类小工具。

import "time"

// parseRFC3339 把 Docker 返回的 RFC3339 时间字符串转成 Unix 秒。
//
// Docker 各端点的时间字段格式不统一：容器的 Created 是 Unix 秒整数，
// 而网络/卷的 Created 是 RFC3339 字符串（且卷的 CreatedAt 可能缺失）。
// 解析失败统一返回 0 而不是报错——时间只是展示信息，
// 为了一个时间字段让整个列表失败不值得。
func parseRFC3339(s string) int64 {
	s = trimSpace(s)
	if s == "" {
		return 0
	}
	// Docker 有时返回带纳秒的格式（2024-01-01T00:00:00.123456789Z），
	// time.RFC3339Nano 能覆盖；失败时再退回普通精度。
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC().Unix()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Unix()
	}
	return 0
}

// trimSpace 是 strings.TrimSpace 的极小封装，避免为一次调用引入整个 strings
// （本包别处已在用 strings，这里保留封装是为了让本文件可独立阅读）。
func trimSpace(s string) string {
	start := 0
	for start < len(s) && isSpaceByte(s[start]) {
		start++
	}
	end := len(s)
	for end > start && isSpaceByte(s[end-1]) {
		end--
	}
	return s[start:end]
}

// isSpaceByte 判断是否空白字符。
func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
