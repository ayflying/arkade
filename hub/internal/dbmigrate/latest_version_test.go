package dbmigrate

import "testing"

// latestVersion 返回当前迁移集的最高版本。
//
// 所有断言都用它而不是写死数字：每加一份迁移就要改四处测试，改的次数多了
// 一定会有人直接把上一版的数字复制过来，测试就变成了自我实现。
func latestVersion(t *testing.T) int {
	t.Helper()
	v, err := LatestVersion()
	if err != nil {
		t.Fatalf("读取最新迁移版本失败: %v", err)
	}
	return v
}
