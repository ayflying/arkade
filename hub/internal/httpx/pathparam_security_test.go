package httpx

// pathparam_security_test.go 是路径参数来源的回归测试。
//
// ## 背景
//
// 迁移前 chi 的 `chi.URLParam` 只从路由匹配结果取值；迁移到 GoFrame 后
// 一个常见的错误写法是用 `r.Get("agentID")`——而 ghttp 的 `r.Get` 是
// "路由参数 + query + 表单"的统一入口，**同名时后者的值会覆盖前者**。
//
// 本包的 wrapToGF 一律用 `r.GetRouter(name)` 取参并写入 context，
// 本文件用测试把这个性质钉住。
//
// ## 测试方式
//
// 全部走**真实监听 + 真实 HTTP 请求**。原因见 router_start.go：
// ghttp 的完整生命周期由 Start 建立，不启动直接 ServeHTTP 会 nil panic。
//
// ## 表述边界（重要）
//
// 下面的测试只证明"取值来源是路径，不会被 query 覆盖"这一件事。
// 它**不构成越权证明**：是否真的越权取决于 handler 后续怎么用这个值
// （拿去查的是不是请求者本来就有权的资源）。本项目没有做越权演练，
// 不应据此声称"已实测越权"。

import (
	"io"
	"net/http"
	"testing"

	"github.com/gogf/gf/v2/frame/g"
)

// newParamServer 建一个只注册一条路由的最小 ghttp 服务并真实启动。
//
// 必须真实 Start：ghttp 的 session 生命周期在 Start() 里建立，
// 未启动直接 ServeHTTP 会在请求收尾时 nil panic（见 router_start.go）。
// 返回访问基址。
func newParamServer(t *testing.T, method, pattern string, fn httpHandlerFunc) string {
	t.Helper()
	s := g.Server("param-test-" + itoa(nextServerSeq()))
	s.SetDumpRouterMap(false)
	s.BindHandler(method+":"+pattern, wrapToGF(pattern, fn))
	return StartForTest(t, s)
}

// doRaw 发一次真实请求（支持自定义 URL），返回状态码与原始响应体。
func doRaw(t *testing.T, method, fullURL string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, fullURL, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求 %s 失败: %v", fullURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// TestPathParamNotOverriddenByQuery 验证同名 query 不能覆盖路径参数。
//
// 这是迁移中最容易引入的隐性缺陷：如果实现改成读 `r.Get`，
// 那么 `?agentID=别人的ID` 就会让 handler 拿到别的 ID。
// 本测试保证实现不会退化成那样。
func TestPathParamNotOverriddenByQuery(t *testing.T) {
	var got struct {
		fromPath   string
		param      string
		patternGot string
	}

	base := newParamServer(t, http.MethodGet, "/api/agents/{agentID}/containers/{containerID}",
		func(w http.ResponseWriter, r *http.Request) {
			got.fromPath = pathParam(r, "agentID")
			got.param = pathParam(r, "containerID")
			got.patternGot = routePattern(r)
			w.WriteHeader(http.StatusOK)
		})

	// 路径里是 A-1，query 里是 A-2。取到的必须是路径里的 A-1。
	code, _ := doRaw(t, http.MethodGet,
		base+"/api/agents/A-1/containers/C-9?agentID=A-2&containerID=C-X")

	if code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", code)
	}
	if got.fromPath != "A-1" {
		t.Fatalf("路径参数被 query 覆盖了：期望 A-1，实际 %q", got.fromPath)
	}
	if got.param != "C-9" {
		t.Fatalf("containerID 被 query 覆盖：期望 C-9，实际 %q", got.param)
	}
	// 路由模板必须保留占位符，审计靠它查动作映射表。
	if got.patternGot != "/api/agents/{agentID}/containers/{containerID}" {
		t.Fatalf("routePattern 应为模板，实际 %q", got.patternGot)
	}
}

// TestPathParamAcrossAllParamNames 覆盖 Hub 实际用到的全部参数名。
//
// Hub 的路径参数共有 agentID / containerID / name / taskID / userID /
// groupID / imageID / networkID 八种。任何一个取值链路出问题都会让
// 对应的一整组接口静默失效（取到空串 → 404 "不存在"），
// 所以逐个验证，而不是抽查一两个。
func TestPathParamAcrossAllParamNames(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		param   string
		want    string
	}{
		{"/api/agents/{agentID}", "/api/agents/A-1", "agentID", "A-1"},
		{
			"/api/agents/{agentID}/containers/{containerID}",
			"/api/agents/A-1/containers/C-1", "containerID", "C-1",
		},
		{"/api/agents/{agentID}/stacks/{name}", "/api/agents/A-1/stacks/web", "name", "web"},
		{"/api/image-tasks/{taskID}", "/api/image-tasks/T-1", "taskID", "T-1"},
		{"/api/users/{userID}", "/api/users/U-1", "userID", "U-1"},
		{"/api/groups/{groupID}", "/api/groups/G-1", "groupID", "G-1"},
		{"/api/agents/{agentID}/images/{imageID}", "/api/agents/A-1/images/I-1", "imageID", "I-1"},
		{
			"/api/agents/{agentID}/networks/{networkID}",
			"/api/agents/A-1/networks/N-1", "networkID", "N-1",
		},
	}

	for _, c := range cases {
		t.Run(c.param, func(t *testing.T) {
			var got string
			base := newParamServer(t, http.MethodGet, c.pattern,
				func(w http.ResponseWriter, r *http.Request) {
					got = pathParam(r, c.param)
					w.WriteHeader(http.StatusOK)
				})

			code, _ := doRaw(t, http.MethodGet, base+c.path)
			if code != http.StatusOK {
				t.Fatalf("路由 %s 未匹配 %s（状态 %d）", c.pattern, c.path, code)
			}
			if got != c.want {
				t.Fatalf("参数 %s 期望 %q，实际 %q", c.param, c.want, got)
			}
		})
	}
}

// TestPathParamDecodesSegment 验证参数段内的百分号编码被正确解码。
//
// 编排名、卷名可以含空格、斜杠、中文（用户写什么就是什么），
// 前端用 encodeURIComponent 编码。迁移后必须保证解码与 chi 一致，
// 否则名字里带特殊字符的编排会全部打不开。
func TestPathParamDecodesSegment(t *testing.T) {
	cases := []struct {
		name string
		enc  string // 编码后的段内容
		want string
	}{
		{"空格", "my%20stack", "my stack"},
		// 编码后的斜杠属于**段内字符**，不能当路径分隔符
		{"编码斜杠", "a%2Fb", "a/b"},
		{"中文", "%E4%B8%AD%E6%96%87", "中文"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got string
			base := newParamServer(t, http.MethodGet, "/api/agents/{agentID}/stacks/{name}",
				func(w http.ResponseWriter, r *http.Request) {
					got = pathParam(r, "name")
					w.WriteHeader(http.StatusOK)
				})

			code, _ := doRaw(t, http.MethodGet, base+"/api/agents/A-1/stacks/"+c.enc)
			if code != http.StatusOK {
				t.Fatalf("编码 %s 未匹配（状态 %d）", c.enc, code)
			}
			if got != c.want {
				t.Fatalf("编码 %s 期望解码为 %q，实际 %q", c.enc, c.want, got)
			}
		})
	}
}

// TestPathParamEmptyForUnknownKey 验证取不存在的参数名返回空串。
//
// 这不是缺陷：handler 里若把参数名写错，会取到空串而不是 panic，
// 行为与迁移前的 chi 一致。audit_routes_test.go 的
// TestAuditTargetParamExists 正是为捕获这类笔误而存在。
func TestPathParamEmptyForUnknownKey(t *testing.T) {
	var got = "sentinel"
	base := newParamServer(t, http.MethodGet, "/api/agents/{agentID}",
		func(w http.ResponseWriter, r *http.Request) {
			got = pathParam(r, "typoName")
			w.WriteHeader(http.StatusOK)
		})

	code, _ := doRaw(t, http.MethodGet, base+"/api/agents/A-1")
	if code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", code)
	}
	if got != "" {
		t.Fatalf("未知参数名应返回空串，实际 %q", got)
	}
}
