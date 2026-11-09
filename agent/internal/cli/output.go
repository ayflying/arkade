package cli

import (
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"arkade/protocol/envelope"
)

// reportError 打印错误并按类型给退出码。
//
// 参数类错误（bad_request）返回用法错误码 2，其余返回 1：
// 脚本可以据此区分"我调用错了"与"机器环境有问题"。
func reportError(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "错误: %v\n", err)
	var ee *envelope.Error
	if errors.As(err, &ee) && ee.Code == envelope.CodeBadRequest {
		return exitUsage
	}
	return exitFail
}

// newTabWriter 统一表格输出风格（两空格列间距，与 docker CLI 接近）。
func newTabWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
}

// emptyDash 把空字符串显示为 "-"，避免表格出现空洞。
func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// displayRef 优先展示运行时返回的短 ID，其次回落到用户输入。
func displayRef(id, ref string) string {
	if id != "" {
		return id
	}
	return ref
}

// 位置参数与选项的混合解析统一由 args.go 的 parseArgs / parsedArgs 负责，
// 这里不再保留第二套"取第一个非选项参数"的实现（避免两套规则互相打架）。
