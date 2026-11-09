package cli

import (
	"flag"
	"strings"
)

// parsedArgs 是"位置参数 + 选项"解析结果。
type parsedArgs struct {
	// Positional 是按原顺序保留的非选项参数（容器引用）。
	Positional []string
}

// First 返回第一个位置参数。
func (p *parsedArgs) First() string {
	if p == nil || len(p.Positional) == 0 {
		return ""
	}
	return p.Positional[0]
}

// parseArgs 解决 Go flag 包的一个常见痛点：
//
//	flag 遇到第一个非选项参数就停止解析，于是 `logs web -n 100` 里的 -n 100
//	会被当成位置参数，用户得不到"只显示 100 行"的效果，而且不会报错——这类
//	静默失效最难排查。
//
// 做法是把"长什么样是选项"的判断前置：值形如 -x / --xx / -n=100 的项以及紧随
// 其后的值（布尔选项除外）归入选项组，其余归入位置参数组；再把选项组先交给
// flag.Parse，位置参数单独返回。这样选项在前在后都能生效。
func parseArgs(fs *flag.FlagSet, args []string) (*parsedArgs, error) {
	boolFlags := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		if bv, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bv.IsBoolFlag() {
			boolFlags[f.Name] = true
		}
	})

	opts := make([]string, 0, len(args))
	pos := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			continue
		}
		opts = append(opts, a)
		name := strings.TrimLeft(a, "-")
		// 已用 = 形式给出值的，不需要再吃掉下一个参数。
		if strings.Contains(name, "=") {
			continue
		}
		if boolFlags[name] {
			continue
		}
		// 非布尔选项且未带 =，下一个参数就是它的值（即使以 - 开头也照收，
		// 让 flag 自己去报"缺少参数"这类错误，行为与标准语义一致）。
		if i+1 < len(args) {
			opts = append(opts, args[i+1])
			i++
		}
	}

	if err := fs.Parse(opts); err != nil {
		return nil, err
	}
	return &parsedArgs{Positional: pos}, nil
}
