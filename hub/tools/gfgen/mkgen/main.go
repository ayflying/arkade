// Command mkgen 用一个空库跑 hub 的真实迁移，供 DAO 生成使用。
//
// ============================ 为什么单独一个程序 ============================
// 生成 DAO 需要一个"结构正确"的库。结构必须来自**权威来源**——
// 也就是 hub 自己的迁移代码，而不是手写的建表 SQL。
//
// 手写建表 SQL 会与真实迁移脱节，生成出的 DAO 就和线上结构不一致，
// 而这种不一致不会在任何地方报错。
//
// 本程序通过导入 arkade/hub/internal/dbmigrate 复用同一套迁移，
// 保证临时库的结构与生产库经过完全相同的路径。
//
// 用法：
//
//	go run ./tools/gfgen/mkgen <目标库路径>
package main

import (
	"context"
	"fmt"
	"os"

	"arkade/hub/internal/dbmigrate"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法: mkgen <目标库路径>")
		os.Exit(2)
	}
	path := os.Args[1]

	res, err := dbmigrate.Run(context.Background(), path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "迁移失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("已迁移到 v%d（本次执行 %d 个版本），库: %s\n",
		res.ToVersion, res.Applied, path)
}
