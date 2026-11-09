package runtime

// container_ops_test.go 用 httptest 假引擎验证重启与删除的实际 HTTP 调用。
//
// 为什么必须测到 URL 层：force 与 v 都是查询参数，拼错一个字母
// 就是"静默不生效"——运行时不会报错，只是按默认行为执行。
// 对删除来说，"以为强制了其实没有"会让用户觉得按钮坏了；
// 对删卷来说，"以为没删其实删了"则直接丢数据。两者都必须用真实请求验证。
//
// 构造方式刻意与 runtime_test.go 的 fakeEngine 保持一致：走 probe 探测，
// 这样测到的 client 与生产环境完全同构（同一个 newClient、同一个超时配置），
// 而不是手搭一个结构体绕过探测逻辑。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// opCall 记录一次请求。
type opCall struct {
	method   string
	path     string
	rawQuery string
}

// hasParam 判断查询串里是否有给定的键值对。
//
// 写成独立函数而不是方法：测试里同时存在 opCall 与方法表两种结构体，
// 方法挂在谁身上容易混淆（这个坑刚刚踩过一次）。
func hasParam(rawQuery, kv string) bool {
	for _, p := range strings.Split(rawQuery, "&") {
		if p == kv {
			return true
		}
	}
	return false
}

// newOpEngine 构造一个只处理容器操作端点的假引擎。
//
// opHandler 为 nil 时所有操作返回 204（成功）。
// 返回的 *[]opCall 记录全部请求，便于断言参数。
func newOpEngine(t *testing.T, opHandler func(w http.ResponseWriter, r *http.Request)) (Runtime, *[]opCall) {
	t.Helper()
	calls := &[]opCall{}

	mux := http.NewServeMux()
	// probe 需要 /version。
	mux.HandleFunc(pathVersion, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Version":"27.3.1","ApiVersion":"1.47","Components":[{"Name":"Engine","Version":"27.3.1"}]}`))
	})
	// 容器操作端点：/containers/{id}/restart 与 DELETE /containers/{id}。
	mux.HandleFunc(pathContainerPrefix, func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, opCall{method: r.Method, path: r.URL.Path, rawQuery: r.URL.RawQuery})
		if opHandler != nil {
			opHandler(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	rt, err := probe(context.Background(), "tcp://"+strings.TrimPrefix(srv.URL, "http://"), false)
	if err != nil {
		t.Fatalf("探测失败: %v", err)
	}
	return rt, calls
}

// TestRestartCallsCorrectEndpoint 验证重启打到 /restart 且带 t 参数。
func TestRestartCallsCorrectEndpoint(t *testing.T) {
	rt, calls := newOpEngine(t, nil)
	if err := rt.Restart(context.Background(), "web"); err != nil {
		t.Fatalf("重启失败: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("应发出 1 个请求，实际 %d", len(*calls))
	}
	c := (*calls)[0]
	if c.method != "POST" {
		t.Errorf("方法应为 POST，实际 %s", c.method)
	}
	if c.path != "/containers/web/restart" {
		t.Errorf("路径应为 /containers/web/restart，实际 %s", c.path)
	}
	// 显式超时：让"等多久"成为我们的决策而不是引擎默认值。
	if !strings.HasPrefix(c.rawQuery, "t=") {
		t.Errorf("应带 t 超时参数，实际 %q", c.rawQuery)
	}
}

// TestRestartEscapesName 验证容器名被合理处理，不会拼出额外路径段。
func TestRestartEscapesName(t *testing.T) {
	rt, calls := newOpEngine(t, nil)
	if err := rt.Restart(context.Background(), "a.b_c-d"); err != nil {
		t.Fatalf("重启失败: %v", err)
	}
	if got := (*calls)[0].path; got != "/containers/a.b_c-d/restart" {
		t.Errorf("路径拼接异常: %s", got)
	}
}

// TestRemoveContainerParamMatrix 验证 force 与 removeVolumes 的四种组合。
//
// 这是本任务最核心的测试：两个开关都必须真的出现在 URL 上，
// 且缺省时不出现（不能让"没要求"被当成"要求了"）。
func TestRemoveContainerParamMatrix(t *testing.T) {
	cases := []struct {
		name          string
		force         bool
		removeVolumes bool
		wantForce     bool
		wantVolumes   bool
	}{
		{"都不带", false, false, false, false},
		{"只强制", true, false, true, false},
		{"只删卷", false, true, false, true},
		{"都带", true, true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rt, calls := newOpEngine(t, nil)
			if err := rt.RemoveContainer(context.Background(), "web", c.force, c.removeVolumes); err != nil {
				t.Fatalf("删除失败: %v", err)
			}
			if len(*calls) != 1 {
				t.Fatalf("应发出 1 个请求，实际 %d", len(*calls))
			}
			got := (*calls)[0]
			if got.method != "DELETE" {
				t.Errorf("方法应为 DELETE，实际 %s", got.method)
			}
			if got.path != "/containers/web" {
				t.Errorf("路径应为 /containers/web，实际 %s", got.path)
			}
			// 关键：force 必须真的发出去，否则运行中的容器删不掉。
			if hasParam(got.rawQuery, "force=1") != c.wantForce {
				t.Errorf("force 参数应为 %v，实际查询串 %q", c.wantForce, got.rawQuery)
			}
			// 关键：v=1 是数据丢失开关，只能显式要求时才出现。
			if hasParam(got.rawQuery, "v=1") != c.wantVolumes {
				t.Errorf("v 参数应为 %v，实际查询串 %q", c.wantVolumes, got.rawQuery)
			}
		})
	}
}

// TestRemoveContainerConflictTranslated 验证 409 被翻译成可操作中文。
func TestRemoveContainerConflictTranslated(t *testing.T) {
	rt, _ := newOpEngine(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"You cannot remove a running container abc"}`))
	})
	err := rt.RemoveContainer(context.Background(), "abc", false, false)
	if err == nil {
		t.Fatal("409 应返回错误")
	}
	// 必须能识别为"冲突"，上层据此决定提示方式（给可操作按钮而不是刷新列表）。
	if !errors.Is(err, ErrContainerConflict) {
		t.Errorf("应能被识别为 ErrContainerConflict，实际 %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "请先停止") || !strings.Contains(msg, "强制删除") {
		t.Errorf("应给出可操作的中文建议，实际 %q", msg)
	}
}

// TestRemoveContainerNotFoundTranslated 验证 404 映射成 ErrNotFound。
func TestRemoveContainerNotFoundTranslated(t *testing.T) {
	rt, _ := newOpEngine(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"No such container: ghost"}`))
	})
	err := rt.RemoveContainer(context.Background(), "ghost", true, false)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("应返回 ErrNotFound，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "容器不存在") {
		t.Errorf("应为中文说明，实际 %q", err.Error())
	}
}

// TestRemoveContainerServerError 验证 500 透出运行时原文。
//
// 500 的具体原因只有引擎知道，硬翻成中文反而丢失信息；
// 这里保留原文但加上中文前缀，兼顾可读与可排查。
func TestRemoveContainerServerError(t *testing.T) {
	rt, _ := newOpEngine(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`driver error: device busy`))
	})
	err := rt.RemoveContainer(context.Background(), "abc", true, false)
	if err == nil {
		t.Fatal("500 应返回错误")
	}
	msg := err.Error()
	if !strings.Contains(msg, "删除容器失败") {
		t.Errorf("应有中文前缀，实际 %q", msg)
	}
	if !strings.Contains(msg, "device busy") {
		t.Errorf("应保留运行时原文便于排查，实际 %q", msg)
	}
}

// TestRemoveContainerEmptyRef 验证空引用被拒且不发请求。
func TestRemoveContainerEmptyRef(t *testing.T) {
	rt, calls := newOpEngine(t, nil)
	if err := rt.RemoveContainer(context.Background(), "  ", true, false); err == nil {
		t.Fatal("空引用应被拒绝")
	}
	if len(*calls) != 0 {
		t.Errorf("空引用不应发出请求，实际 %d 个", len(*calls))
	}
}

// TestRemoveContainerRejectsSlash 验证含斜杠的引用被拒（防路径注入）。
func TestRemoveContainerRejectsSlash(t *testing.T) {
	rt, calls := newOpEngine(t, nil)
	if err := rt.RemoveContainer(context.Background(), "a/b", true, false); err == nil {
		t.Fatal("含斜杠的引用应被拒绝")
	}
	if len(*calls) != 0 {
		t.Errorf("非法引用不应发出请求，实际 %d 个", len(*calls))
	}
}
