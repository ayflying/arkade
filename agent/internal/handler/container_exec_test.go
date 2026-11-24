package handler

// container_exec_test.go：container.exec 处理器的契约测试。
//
// 重点在两件事：
//  1. 参数校验的错误必须是 bad_request（参数问题），不是 runtime_failed；
//  2. 超时秒数必须被夹到安全区间——这是"用户能影响服务端行为"的唯一入口。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// execFake 是实现了容器内执行能力的假运行时。
type execFake struct {
	fakeRuntime
	res     runtime.ExecResult
	err     error
	lastID  string
	lastOpt runtime.ExecOptions
}

func (f *execFake) ExecContainer(_ context.Context, id string, opts runtime.ExecOptions) (runtime.ExecResult, error) {
	f.lastID = id
	f.lastOpt = opts
	if f.err != nil {
		return runtime.ExecResult{}, f.err
	}
	return f.res, nil
}

// runExec 通过注册表派发一次 container.exec。
func runExec(t *testing.T, rt runtime.Runtime, p envelope.ContainerExecPayload) *envelope.Envelope {
	t.Helper()
	reg := NewRegistry()
	reg.Register(envelope.MethodContainerExec, newExecHandler(rt))
	raw, _ := json.Marshal(p)
	return reg.Dispatch(context.Background(), &envelope.Envelope{
		V: envelope.Version, ID: "t", Type: envelope.TypeRequest,
		Method: envelope.MethodContainerExec, Payload: raw,
	})
}

// TestExecHandlerMapsResult 验证结果字段按协议搬运。
func TestExecHandlerMapsResult(t *testing.T) {
	f := &execFake{res: runtime.ExecResult{
		ExitCode: 2, Stdout: "out", Stderr: "err",
		Truncated: true, TimedOut: false, Duration: 1500 * time.Millisecond,
	}}
	resp := runExec(t, f, envelope.ContainerExecPayload{
		ContainerID: "web", Command: []string{"sh", "-c", "exit 2"}, User: "1000", WorkDir: "/srv",
	})
	if resp.Error != nil {
		t.Fatalf("不应失败: %+v", resp.Error)
	}
	var out envelope.ContainerExecResult
	if err := envelope.DecodePayload(resp, &out); err != nil {
		t.Fatal(err)
	}
	if out.ExitCode != 2 || out.Stdout != "out" || out.Stderr != "err" || !out.Truncated {
		t.Fatalf("字段搬运不符: %+v", out)
	}
	if out.DurationMs != 1500 {
		t.Fatalf("耗时换算不符: %d", out.DurationMs)
	}
	if f.lastID != "web" || f.lastOpt.User != "1000" || f.lastOpt.WorkDir != "/srv" {
		t.Fatalf("参数未原样传到运行时: %+v", f.lastOpt)
	}
	if len(f.lastOpt.Command) != 3 || f.lastOpt.Command[2] != "exit 2" {
		t.Fatalf("命令向量被改动: %+v", f.lastOpt.Command)
	}
}

// TestExecHandlerClampsTimeout 验证超时被夹到 [默认, 上限] 区间。
//
// 这是安全边界：请求里的 timeout_sec 直接决定 Agent 等待多久，
// 不夹就等于让调用方决定"把一条控制通道占住多久"。
func TestExecHandlerClampsTimeout(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want time.Duration
	}{
		{"未指定用默认", 0, 30 * time.Second},
		{"超上限夹到 120 秒", 9999, 120 * time.Second},
		{"区间内原样保留", 45, 45 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &execFake{}
			resp := runExec(t, f, envelope.ContainerExecPayload{
				ContainerID: "web", Command: []string{"sh"}, TimeoutSec: c.in,
			})
			if resp.Error != nil {
				t.Fatalf("不应失败: %+v", resp.Error)
			}
			if f.lastOpt.Timeout != c.want {
				t.Fatalf("超时应为 %v，实际 %v", c.want, f.lastOpt.Timeout)
			}
		})
	}
}

// TestExecHandlerRejectsBadInput 验证非法参数返回 bad_request。
//
// 每种都必须是 bad_request 而不是 runtime_failed：
// 前者告诉用户"改输入"，后者会让用户去查容器/引擎，方向完全错了。
func TestExecHandlerRejectsBadInput(t *testing.T) {
	tooMany := make([]string, maxExecArgs+1)
	for i := range tooMany {
		tooMany[i] = "x"
	}
	cases := []struct {
		name string
		p    envelope.ContainerExecPayload
	}{
		{"空容器 ID", envelope.ContainerExecPayload{ContainerID: "  ", Command: []string{"sh"}}},
		{"空命令", envelope.ContainerExecPayload{ContainerID: "web"}},
		{"空命令首项", envelope.ContainerExecPayload{ContainerID: "web", Command: []string{"  "}}},
		{"参数过多", envelope.ContainerExecPayload{ContainerID: "web", Command: tooMany}},
		{"参数含 NUL", envelope.ContainerExecPayload{ContainerID: "web", Command: []string{"sh", "a\x00b"}}},
		{"用户含换行", envelope.ContainerExecPayload{ContainerID: "web", Command: []string{"sh"}, User: "roo\nt"}},
		{"工作目录含换行", envelope.ContainerExecPayload{ContainerID: "web", Command: []string{"sh"}, WorkDir: "/srv\n"}},
		{"工作目录非绝对路径", envelope.ContainerExecPayload{ContainerID: "web", Command: []string{"sh"}, WorkDir: "srv"}},
		{"工作目录过长", envelope.ContainerExecPayload{ContainerID: "web", Command: []string{"sh"}, WorkDir: "/" + strings.Repeat("a", maxExecDirBytes)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &execFake{}
			resp := runExec(t, f, c.p)
			if resp.Error == nil {
				t.Fatal("必须报错")
			}
			if resp.Error.Code != envelope.CodeBadRequest {
				t.Fatalf("期望 bad_request，实际 %s (%s)", resp.Error.Code, resp.Error.Message)
			}
			if f.lastID != "" {
				t.Fatal("非法参数不应到达运行时")
			}
		})
	}
}

// TestExecHandlerUnsupportedEngine 验证引擎不支持时返回 unsupported。
func TestExecHandlerUnsupportedEngine(t *testing.T) {
	resp := runExec(t, &fakeRuntime{}, envelope.ContainerExecPayload{ContainerID: "web", Command: []string{"sh"}})
	if resp.Error == nil || resp.Error.Code != envelope.CodeUnsupported {
		t.Fatalf("期望 unsupported，实际 %+v", resp.Error)
	}
}

// TestExecHandlerPropagatesError 验证底层错误由统一映射处理。
func TestExecHandlerPropagatesError(t *testing.T) {
	f := &execFake{err: runtime.ErrNotFound}
	resp := runExec(t, f, envelope.ContainerExecPayload{ContainerID: "web", Command: []string{"sh"}})
	if resp.Error == nil || resp.Error.Code != envelope.CodeNotFound {
		t.Fatalf("期望 not_found，实际 %+v", resp.Error)
	}
}
