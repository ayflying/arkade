package httpx

// typed_bind_test.go 验证 GoFrame 规范路由绑定（group.Bind）在本项目可用。
//
// ## 为什么需要这些测试
//
// gfChain 把标准库中间件链塞进 GF 中间件，入口与链底的 writer 处理
// 稍有差错就会形成 **start → statusWriter → start** 的自我引用，
// 表现为请求无限递归直到栈溢出。
//
// 这类缺陷的症状是"测试卡住"而不是"断言失败"，非常难定位。
// 因此这里用**明确的短超时**发请求：一旦递归，测试在超时后立刻失败
// 并说明原因，而不是让整个 suite 卡几分钟。

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

// doTyped 向服务发请求，带硬超时。
//
// 超时即失败（不是跳过）：卡住说明中间件链递归了，
// 那是必须立刻修的真缺陷，不能当成"环境慢"掩盖。
func doTyped(t *testing.T, base, method, path string) (int, []byte) {
	t.Helper()

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(method, base+path, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		// 超时是最可能的失败：说明请求没能在 5s 内返回，
		// 通常意味着中间件链递归或死锁。
		t.Fatalf("请求 %s %s 失败（超时或连接错误，若为超时请检查 gfChain 是否递归）: %v",
			method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应体失败: %v", err)
	}
	return resp.StatusCode, body
}

// TestBindHealthReturnsBareJSON 验证 group.Bind 路由可用且响应是顶层裸 JSON。
//
// 两个断言缺一不可：
//   - 路由能命中（证明 g.Meta 的 path 被框架正确读取）；
//   - 响应是裸 JSON（证明没有走默认的 MiddlewareHandlerResponse，
//     否则会变成 {"code":0,"message":"","data":{...}}）。
func TestBindHealthReturnsBareJSON(t *testing.T) {
	e := newTestEnv(t)

	status, body := doTyped(t, e.wsURL, http.MethodGet, "/healthz")
	if status != http.StatusOK {
		t.Fatalf("/healthz 应返回 200，实际 %d，body=%s", status, body)
	}

	// 必须是顶层裸 JSON：直接解析成有 status 字段的结构。
	var got struct {
		Status       string `json:"status"`
		Version      string `json:"version"`
		AgentsOnline int    `json:"agents_online"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v，body=%s", err, body)
	}

	// 关键断言：顶层必须有 status 字段。
	// 若被 MiddlewareHandlerResponse 包了一层，这里会是空串，
	// 因为真实的 status 在 data.status 里。
	if got.Status != "ok" {
		t.Fatalf("顶层 status 应为 ok（若为响应被包了一层，说明用错了中间件）: body=%s", body)
	}
	if got.Version != "test" {
		t.Errorf("version 应为 test，实际 %q", got.Version)
	}

	// 反向断言：响应体里不应出现 code/message/data 那层包装。
	var wrapped map[string]json.RawMessage
	if err := json.Unmarshal(body, &wrapped); err != nil {
		t.Fatalf("响应不是 JSON 对象: %v", err)
	}
	if _, ok := wrapped["data"]; ok {
		t.Fatalf("响应被 MiddlewareHandlerResponse 包装了（出现 data 字段）: body=%s", body)
	}
}

// TestBindHealthNotRecursive 是死循环的专项防护。
//
// gfChain 若在入口用 `start` 而不是 `start.ResponseWriter`，
// 会形成 start → statusWriter → start 自我引用，请求无限递归。
// 这里连续发多次请求并在超时后立刻失败，确保不会把 suite 拖死。
func TestBindHealthNotRecursive(t *testing.T) {
	e := newTestEnv(t)

	// 多次往返：递归通常在第一次就爆栈，但连续请求能同时
	// 覆盖"defer 恢复后第二次请求状态被污染"的可能。
	for i := 0; i < 3; i++ {
		status, body := doTyped(t, e.wsURL, http.MethodGet, "/healthz")
		if status != http.StatusOK {
			t.Fatalf("第 %d 次请求失败：状态 %d，body=%s", i+1, status, body)
		}
	}
}

// TestBindHealthLogsStatus 验证 statusWriter 在 Bind 路由上仍生效。
//
// 若 bareJSON 的写入发生在 gfChain 恢复 writer 之后，
// requestLogger 记到的状态码会是 0 或错误值。这里通过
// 响应头与状态码的组合间接确认链路完整。
func TestBindHealthLogsStatus(t *testing.T) {
	e := newTestEnv(t)

	status, _ := doTyped(t, e.wsURL, http.MethodGet, "/healthz")
	if status != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", status)
	}

	// 安全头由 securityHeaders 中间件设置：它能出现在响应里，
	// 说明整条标准库链（含外层包装）确实执行了。
	resp, err := http.Get(e.wsURL + "/healthz")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)

	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("securityHeaders 未生效（X-Content-Type-Options=%q），"+
			"说明 Bind 路由上的标准库中间件链没有正确执行", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control 应为 no-store，实际 %q", got)
	}
}
