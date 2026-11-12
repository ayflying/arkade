package store

// util.go 放仓储层共用的小工具，避免每个文件重复实现同一转换。
// 这里只允许放纯函数，不能有业务语义。

import "time"

// unixToTime 把数据库里的 Unix 秒转成 UTC 时间。
// 统一使用 UTC 存储、展示时再转本地时区，避免跨时区服务器之间互相看不明白。
func unixToTime(sec int64) time.Time {
	return time.Unix(sec, 0).UTC()
}

// timeToUnix 把时间转成数据库存储用的 Unix 秒。
func timeToUnix(t time.Time) int64 { return t.UTC().Unix() }

// boolToInt preserves the business schema integer flag representation (0/1).
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
