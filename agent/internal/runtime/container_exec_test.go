package runtime

// container_exec_test.go：用假引擎验证 exec 的 HTTP 契约与输出解帧。
//
// ============================ 断言的是"发出去的请求" ============================
//
// 这里最容易错的地方不是解析，而是请求形状：
//   - Tty=true 会让引擎改回纯文本流，stdout/stderr 就再也分不开；
//   - AttachStdin=true 而没有输入，某些引擎会一直等 stdin；
//   - Detach=true 会直接返回 200，输出与退出码全部丢失。
//
// 这三种错误在"调用没报错"的情况下完全看不出来，因此必须逐字段断言。

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// execFrame 构造一帧多路复用输出。
func execFrame(stream byte, payload string) []byte {
	out := make([]byte, logFrameHeaderLen+len(payload))
	out[0] = stream
	binary.BigEndian.PutUint32(out[4:8], uint32(len(payload)))
	copy(out[logFrameHeaderLen:], payload)
	return out
}

// execEngine 构造一个完整的假 exec 流程引擎。
func execEngine(t *testing.T, createBody *execCreateReq, startBody *execStartReq,
	stdout, stderr, exitJSON string) *engine {
	t.Helper()
	return newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/exec") && r.Method == http.MethodPost:
			var body execCreateReq
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			if createBody != nil {
				*createBody = body
			}
			_, _ = w.Write([]byte(`{"Id":"exec-1"}`))
		case strings.HasSuffix(r.URL.Path, "/start") && r.Method == http.MethodPost:
			var body execStartReq
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			if startBody != nil {
				*startBody = body
			}
			if stdout != "" {
				_, _ = w.Write(execFrame(logStreamStdOut, stdout))
			}
			if stderr != "" {
				_, _ = w.Write(execFrame(logStreamStdErr, stderr))
			}
		case strings.HasSuffix(r.URL.Path, "/json") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(exitJSON))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// TestExecRequestShapeIsOneShot 验证请求形状：非 TTY、不挂 stdin、命令原样传递。
func TestExecRequestShapeIsOneShot(t *testing.T) {
	var create execCreateReq
	var start execStartReq
	e := execEngine(t, &create, &start, "ok\n", "", `{"Running":false,"ExitCode":3}`)

	res, err := e.ExecContainer(context.Background(), "demo", ExecOptions{
		Command: []string{"/bin/sh", "-c", "echo ok; echo err >&2"},
		User:    "1000",
		WorkDir: "/srv",
	})
	if err != nil {
		t.Fatal(err)
	}
	if create.Tty || start.Tty {
		t.Fatal("必须用非 TTY：TTY 模式没有多路复用帧，stdout/stderr 无法分开")
	}
	if create.AttachStdin {
		t.Fatal("一次性执行不应挂 stdin")
	}
	if start.Detach {
		t.Fatal("必须 Detach=false，否则拿不到输出与退出码")
	}
	if !create.AttachStdout || !create.AttachStderr {
		t.Fatalf("两路输出都需要: %+v", create)
	}
	if len(create.Cmd) != 3 || create.Cmd[0] != "/bin/sh" || create.Cmd[2] != "echo ok; echo err >&2" {
		t.Fatalf("命令必须原样按向量传递，不能被拆开或拼接: %+v", create.Cmd)
	}
	if create.User != "1000" || create.WorkingDir != "/srv" {
		t.Fatalf("用户与工作目录不符: %+v", create)
	}
	if res.ExitCode != 3 {
		t.Fatalf("退出码应来自 exec inspect，实际 %d", res.ExitCode)
	}
	if res.Stdout != "ok\n" {
		t.Fatalf("stdout 不符: %q", res.Stdout)
	}
	if res.TimedOut || res.Truncated {
		t.Fatalf("不应标记超时/截断: %+v", res)
	}
}

// TestExecSeparatesStderr 验证 stdout 与 stderr 分别落到两个字段。
func TestExecSeparatesStderr(t *testing.T) {
	e := execEngine(t, nil, nil, "out\n", "err\n", `{"Running":false,"ExitCode":0}`)
	res, err := e.ExecContainer(context.Background(), "demo", ExecOptions{Command: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "out\n" || res.Stderr != "err\n" {
		t.Fatalf("两路必须分开: stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}
}

// TestExecPlainTextFallback 验证兼容实现直接返回纯文本时仍能给出输出。
func TestExecPlainTextFallback(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/exec"):
			_, _ = w.Write([]byte(`{"Id":"exec-1"}`))
		case strings.HasSuffix(r.URL.Path, "/start"):
			_, _ = w.Write([]byte("hello from plain text\n"))
		default:
			_, _ = w.Write([]byte(`{"Running":false,"ExitCode":0}`))
		}
	})
	res, err := e.ExecContainer(context.Background(), "demo", ExecOptions{Command: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "hello from plain text\n" {
		t.Fatalf("纯文本响应应整段作为 stdout，实际 %q", res.Stdout)
	}
}

// TestExecTruncatesLargeOutput 验证输出上限生效且置 Truncated。
func TestExecTruncatesLargeOutput(t *testing.T) {
	// 单帧超过上限：必须被截断，且不能把整段原样返回。
	huge := strings.Repeat("x", maxExecOutputBytes+1024)
	e := execEngine(t, nil, nil, huge, "", `{"Running":false,"ExitCode":0}`)
	res, err := e.ExecContainer(context.Background(), "demo", ExecOptions{Command: []string{"cat"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatal("超上限必须标记 Truncated")
	}
	if len(res.Stdout) > maxExecOutputBytes {
		t.Fatalf("输出未被截断: %d 字节", len(res.Stdout))
	}
}

// TestExecTimeoutIsHonest 验证超时如实回答：TimedOut=true 且退出码未知（-1）。
//
// 绝不编造一个 0 或 1：超时并不代表命令失败，它可能还在跑。
func TestExecTimeoutIsHonest(t *testing.T) {
	release := make(chan struct{})
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/exec"):
			_, _ = w.Write([]byte(`{"Id":"exec-1"}`))
		case strings.HasSuffix(r.URL.Path, "/start"):
			_, _ = w.Write(execFrame(logStreamStdOut, "partial\n"))
			w.(http.Flusher).Flush()
			<-release // 一直不结束，模拟长命令
		default:
			_, _ = w.Write([]byte(`{"Running":true,"ExitCode":0}`))
		}
	})
	// 必须在 newTestEngine 之后注册：t.Cleanup 是后进先出，
	// 先放行 handler，httptest.Server.Close 才不会一直等在读请求上。
	t.Cleanup(func() { close(release) })
	begin := time.Now()
	res, err := e.ExecContainer(context.Background(), "demo", ExecOptions{
		Command: []string{"sleep", "600"},
		Timeout: 120 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("超时不应变成错误（输出已部分拿到）: %v", err)
	}
	if !res.TimedOut {
		t.Fatal("必须标记 TimedOut")
	}
	if res.ExitCode != -1 {
		t.Fatalf("超时后退出码应未知(-1)，实际 %d", res.ExitCode)
	}
	if elapsed := time.Since(begin); elapsed > 5*time.Second {
		t.Fatalf("超时没有真正生效，耗时 %v", elapsed)
	}
}

// TestExecCreateFailureTranslated 验证容器不存在时给出可读错误而不是引擎原文。
func TestExecCreateFailureTranslated(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"No such container: demo"}`))
	})
	_, err := e.ExecContainer(context.Background(), "demo", ExecOptions{Command: []string{"sh"}})
	if err == nil {
		t.Fatal("容器不存在必须报错")
	}
	if !strings.Contains(err.Error(), "容器不存在") {
		t.Fatalf("错误应翻译成中文可读文案，实际 %v", err)
	}
}

// TestExecEmptyCommandRejected 验证空命令在运行时层就被挡下。
func TestExecEmptyCommandRejected(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("空命令不应发出任何请求")
	})
	if _, err := e.ExecContainer(context.Background(), "demo", ExecOptions{}); err == nil {
		t.Fatal("空命令必须报错")
	}
}

// TestSplitExecFramesTruncatesAtUTF8Boundary 验证截断不会切坏多字节字符。
func TestSplitExecFramesTruncatesAtUTF8Boundary(t *testing.T) {
	// 每个汉字 3 字节，上限若不是 3 的倍数就会切在字符中间。
	body := strings.Repeat("中", maxExecOutputBytes)
	raw := execFrame(logStreamStdOut, body)
	stdout, _, truncated := splitExecFrames(raw)
	if !truncated {
		t.Fatal("应标记截断")
	}
	if len(stdout) > maxExecOutputBytes {
		t.Fatalf("超过上限: %d", len(stdout))
	}
	if strings.Contains(stdout, "\uFFFD") {
		t.Fatal("截断点落在多字节字符中间，返回了替换字符")
	}
}
