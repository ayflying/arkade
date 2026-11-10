package compose

// runner_stream_test.go：流式输出的行边界与旧 Run 的字节语义。
//
// ============================ 两组断言的目的 ============================
//
//  1. **旧 Run 的字节语义不能变**：compose ps --format json 输出一段末尾无换行
//     的 JSON，且可能长到几十 KB。Run 必须原样给出这些字节。
//
//  2. **超长行绝不泄漏前缀**：按行推送的输出里，若"到上限就先发一段"，
//     跨边界的敏感值就会被切成两半送出去。因此超限必须整行丢弃。

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// collectSink 收集流式输出的每一行。
type collectSink struct {
	lines []string
}

func (c *collectSink) Stdout(line string) { c.lines = append(c.lines, line) }
func (c *collectSink) Stderr(line string) { c.lines = append(c.lines, line) }

// TestRunPreservesExactBytes 验证旧 Run 不改写字节（尤其末尾无换行的 JSON）。
func TestRunPreservesExactBytes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	// 输出无尾换行的 JSON，内容约 100KB（远超旧的 4000 字符截断）。
	// 用 head/tr 生成，避免嵌套命令替换在不同 shell 下的差异。
	script := writeScript(t,
		"printf '{\"a\":\"'\nhead -c 100000 /dev/zero | tr '\\0' 'x'\nprintf '\"}'\n")
	r := &Runner{Binary: script}
	res := r.Run(context.Background(), "demo", TimeoutShort, "ps", "--format", "json")

	if res.ExitCode != 0 {
		t.Fatalf("应成功，退出码 %d: %s", res.ExitCode, res.Output)
	}
	// 关键：Stdout 必须是**完整原始字节**，不能被行扫描截断。
	if !strings.HasPrefix(res.Stdout, `{"a":"xxx`) {
		t.Fatalf("Stdout 开头不符: %.60s", res.Stdout)
	}
	if !strings.HasSuffix(res.Stdout, `"}`) {
		t.Fatalf("Stdout 结尾不符（可能被截断）: ...%.60s", res.Stdout)
	}
	if strings.Contains(res.Stdout, "\n") {
		t.Fatalf("无尾换行的输出不应被加入换行: %q", res.Stdout[len(res.Stdout)-10:])
	}
	// 100KB 的 JSON 必须完整保留，不能被 maxOutputRunes 之外的东西破坏。
	if n := strings.Count(res.Stdout, "x"); n != 100000 {
		t.Fatalf("应保留 100000 个 x，实际 %d", n)
	}
}

// TestRunStreamSplitsLines 验证流式版按行推送且不含行尾换行。
func TestRunStreamSplitsLines(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	script := writeScript(t, "echo one\necho two\n")
	r := &Runner{Binary: script}
	sink := &collectSink{}
	res := r.RunStream(context.Background(), "demo", TimeoutShort, sink, "pull")
	if res.ExitCode != 0 {
		t.Fatalf("应成功: %+v", res)
	}
	if len(sink.lines) != 2 || sink.lines[0] != "one" || sink.lines[1] != "two" {
		t.Fatalf("行切分不符: %#v", sink.lines)
	}
}

// TestRunStreamEmitsTrailingLineWithoutNewline 验证不以换行结尾的尾行不丢。
func TestRunStreamEmitsTrailingLineWithoutNewline(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	script := writeScript(t, "printf 'last-line-no-newline'\n")
	r := &Runner{Binary: script}
	sink := &collectSink{}
	r.RunStream(context.Background(), "demo", TimeoutShort, sink, "pull")
	if len(sink.lines) != 1 || sink.lines[0] != "last-line-no-newline" {
		t.Fatalf("尾行应被补发: %#v", sink.lines)
	}
}

// TestStreamWriterDropsOversizeLineWithoutPrefix 验证超长行整行丢弃、不泄漏前缀。
//
// ============================ 这是安全断言 ============================
//
// 构造一行 "SECRET_START" + 大量填充 + "SECRET_END"，长度远超单行上限。
// 若实现"到上限就先发一段"，那么前半段（含 SECRET_START）会被送出去，
// 这就是跨边界泄漏。正确实现只发一个固定标记，**一个字节原文都不给**。
func TestStreamWriterDropsOversizeLineWithoutPrefix(t *testing.T) {
	var got []string
	w := newStreamWriter(func(line string) { got = append(got, line) })

	// 分块写入，模拟管道分段到达（这会真实跨越内部缓冲上限）。
	head := "SECRET_START" + strings.Repeat("A", maxLineBytes)
	tail := strings.Repeat("B", 1000) + "SECRET_END\n"
	if _, err := w.Write([]byte(head)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(tail)); err != nil {
		t.Fatal(err)
	}
	w.Close()

	for _, line := range got {
		if strings.Contains(line, "SECRET_START") {
			t.Fatalf("超长行的前缀被送出（跨边界泄漏）: %.80s", line)
		}
		if strings.Contains(line, "SECRET_END") {
			t.Fatalf("超长行的后缀被送出: %.80s", line)
		}
		if strings.Contains(line, "AAAA") || strings.Contains(line, "BBBB") {
			t.Fatalf("超长行的内容片段被送出: %.80s", line)
		}
	}
	// 必须给出明确的截断标记，让用户知道这里有内容缺失。
	found := false
	for _, line := range got {
		if line == longLineMarker {
			found = true
		}
	}
	if !found {
		t.Fatalf("应上报截断标记，实际 %#v", got)
	}
}

// TestStreamWriterKeepsSecretIntactWithinLine 验证正常长度的敏感值能被整行匹配。
//
// 脱敏的前提是"看到完整行"。这条测试确认行缓冲不会把一行切断
// （若切断，脱敏器就匹配不上完整密码，反而漏遮）。
func TestStreamWriterKeepsSecretIntactWithinLine(t *testing.T) {
	var got []string
	w := newStreamWriter(func(line string) { got = append(got, line) })

	// 分段写入同一行（模拟管道分包）。
	w.Write([]byte("prefix hunter"))
	w.Write([]byte("2 suffix\n"))
	w.Close()

	if len(got) != 1 {
		t.Fatalf("应得到 1 行，实际 %#v", got)
	}
	if !strings.Contains(got[0], "hunter2") {
		t.Fatalf("整行应完整到达（否则脱敏匹配不上）: %q", got[0])
	}
}

// TestStreamWriterHandlesCRLF 验证 \r\n 被正确剥离（compose 进度重绘常见）。
func TestStreamWriterHandlesCRLF(t *testing.T) {
	var got []string
	w := newStreamWriter(func(line string) { got = append(got, line) })
	w.Write([]byte("progress 50%\r\nprogress 100%\r\n"))
	w.Close()
	if len(got) != 2 || got[0] != "progress 50%" || got[1] != "progress 100%" {
		t.Fatalf("CRLF 处理不符: %#v", got)
	}
}

// TestPullOversizeOutputNeverLeaks 端到端验证：命令输出超长行时任务不泄漏内容。
func TestPullOversizeOutputNeverLeaks(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	// 打印一行超长内容，其中埋一个"密码"。
	script := writeScript(t,
		"printf 'SECRETPASS'\nhead -c 200000 /dev/zero | tr '\\0' 'Z'\nprintf '\\n'\necho done\n")
	m, p := newPullTestManager(t, script)
	if _, _, err := m.ws.CommitProject("demo", "services:\n  web:\n    image: nginx\n",
		"PASSWORD=SECRETPASS\n"); err != nil {
		t.Fatal(err)
	}

	snap, _ := p.StartPull("demo", "oversize-req")
	final := waitTerminal(t, p, snap.TaskID)
	joined := joinEvents(final.Output)
	if strings.Contains(joined, "SECRETPASS") {
		t.Fatalf("超长行内容泄漏: %.120s", joined)
	}
	if strings.Contains(joined, "ZZZZ") {
		t.Fatalf("超长行填充内容泄漏: %.120s", joined)
	}
	if !strings.Contains(joined, longLineMarker) {
		t.Fatalf("应有截断标记: %q", joined)
	}
}

// TestStreamWriterReturnsInputLength 验证 Write 返回的是**输入长度**。
//
// 这是 io.Writer 契约：所有返回点都必须是入口的 len(p)。
// 丢弃态里若返回 0（"我什么都没收"），os/exec 的 copy goroutine 会认为
// 发生短路写并报 io.ErrShortWrite，整条命令因此失败——
// 用户看到"拉取失败"，实际是我们自己的 bug。
func TestStreamWriterReturnsInputLength(t *testing.T) {
	w := newStreamWriter(func(string) {})

	// 第一块超限 → 进入丢弃态。
	big := strings.Repeat("A", maxLineBytes*2)
	if n, err := w.Write([]byte(big)); err != nil || n != len(big) {
		t.Fatalf("超限块应返回输入长度: n=%d want=%d err=%v", n, len(big), err)
	}

	// 第二块含换行且后面还有剩余内容 → 必须返回**整块**长度。
	second := strings.Repeat("B", 100) + "\nrest-after-newline"
	if n, err := w.Write([]byte(second)); err != nil || n != len(second) {
		t.Fatalf("含换行的块应返回输入长度: n=%d want=%d err=%v", n, len(second), err)
	}

	// 仅含换行结束丢弃态（换行之后无内容）→ 仍需返回整块长度。
	w2 := newStreamWriter(func(string) {})
	w2.Write([]byte(strings.Repeat("C", maxLineBytes*2)))
	third := "tail\n"
	if n, err := w2.Write([]byte(third)); err != nil || n != len(third) {
		t.Fatalf("换行结尾的块应返回输入长度: n=%d want=%d err=%v", n, len(third), err)
	}
}

// 确保 os/filepath 在未使用的平台上也被引用（避免不同平台下 import 报错）。'
var _ = os.Remove
var _ = filepath.Join
