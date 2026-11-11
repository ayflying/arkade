package httpx

// audit_routes_test.go 校验"审计覆盖所有写接口"这条结构性约束。
//
// 迁移说明：路由遍历方式从 chi.Walk 换成 GoFrame 的 Server.GetRoutes()，
// **所有断言逻辑保持原样**，没有放宽任何一条。
//
// GetRoutes 返回的是 GF 注册过的真实路由（含方法与模板），
// 因此它和 chi.Walk 一样能证明"真实注册了哪些接口"。

import (
	"strings"
	"testing"

	"github.com/gogf/gf/v2/net/ghttp"

	"arkade/hub/internal/audit"
)

// serverOf 从 NewRouter 的返回值取出 ghttp.Server 以便遍历路由。
func serverOf(t *testing.T, h interface{}) *ghttp.Server {
	t.Helper()
	s, ok := h.(*ghttp.Server)
	if !ok {
		t.Fatalf("NewRouter 应返回 *ghttp.Server，实际 %T", h)
	}
	return s
}

// gfRoutes 把 GF 的路由表规整成 (方法, 模板) 列表。
//
// GF 的 RouterItem 里 Method 已是大写（源码 strings.ToUpper），
// Route 是含 `{param}` 的模板，与 chi 的 RoutePattern 形式一致。
//
// ## ALL 方法的处理
//
// `BindHandler("/*", …)` 会被 GF 记为 Method=="ALL"。目前只有兜底路由用它。
// 但不能一概跳过——将来若有人用 `BindHandler("/api/x", …)`（不带方法前缀）
// 而忘了写具体方法，那条业务路由会被**静默排除**在审计覆盖检查之外，
// 正是这类测试最该抓住的错误。
//
// 因此这里不跳过，而是原样返回 Method=="ALL" 的记录，
// 由 TestOnlyFallbackUsesUnqualifiedRoute 断言"ALL 只允许出现在 /*"。
func gfRoutes(s *ghttp.Server) []routeEntry {
	var out []routeEntry
	for _, item := range s.GetRoutes() {
		method := strings.ToUpper(strings.TrimSpace(item.Method))
		for _, m := range strings.Split(method, ",") {
			m = strings.TrimSpace(m)
			if m == "" || m == "*" {
				continue
			}
			out = append(out, routeEntry{Method: m, Pattern: item.Route})
		}
	}
	return out
}

// routeEntry 是路由的可观测信息。
type routeEntry struct {
	Method  string
	Pattern string
}

// TestOnlyFallbackUsesUnqualifiedRoute 确保"未指定方法"的路由只有兜底那一条。
//
// `BindHandler("/*", …)` 被 GF 记为 Method=="ALL"，这是兜底路由的正常形态。
// 但业务路由**必须**写成 `BindHandler("GET:/api/x", …)`：
// 少了方法前缀，GF 会把它当成 ALL，等于对所有方法都响应——
// 一条本该只接受 POST 的写接口会连 GET 都能进，且不会被
// TestEveryMutatingRouteHasAuditAction 覆盖到（ALL 不等于具体方法）。
//
// 这条测试把上述约束钉死：ALL 只允许出现在 `/*` 这一条兜底路由上。
func TestOnlyFallbackUsesUnqualifiedRoute(t *testing.T) {
	e := newTestEnv(t)
	s := serverOf(t, e.router)

	for _, ri := range gfRoutes(s) {
		if ri.Method != "ALL" {
			continue
		}
		if ri.Pattern != "/*" {
			t.Errorf("路由 %q 未指定 HTTP 方法（GF 记为 ALL）。"+
				"业务路由必须写成 \"METHOD:/path\" 形式，否则会响应所有方法。",
				ri.Pattern)
		}
	}
}

// isFallbackRoute 判断是否为兜底路由（未限定方法）。
func isFallbackRoute(method string) bool { return method == "ALL" }

// TestEveryMutatingRouteHasAuditAction 遍历真实路由，确保每条写接口都有映射。
//
// 这是本任务最重要的一条结构性测试：它把"审计必须覆盖所有写接口"
// 从一句约定变成了机器可验证的约束。
func TestEveryMutatingRouteHasAuditAction(t *testing.T) {
	e := newTestEnv(t)

	// NewRouter 返回 ghttp.Server，用 GetRoutes 遍历真实注册的路由。
	s := serverOf(t, e.router)

	var missing []string
	for _, ri := range gfRoutes(s) {
		method, routePattern := ri.Method, ri.Pattern
		// 兜底路由不参与这条检查（它没有具体方法，也不承载业务），
		// 但它"必须是唯一的不限定方法路由"由上一条测试保证。
		if isFallbackRoute(method) {
			continue
		}
		if !isMutatingMethod(method) {
			continue
		}
		// 登录接口在未认证分组外，auditMiddleware 覆盖不到，
		// 且它是"获取凭证"而非业务操作，不纳入审计范围。
		if routePattern == "/api/auth/login" {
			continue
		}
		resolved := resolveAuditRoute(routePattern, method, routePattern)
		if resolved.Action == audit.Unknown(method) {
			missing = append(missing, method+" "+routePattern)
			continue
		}
		// 光有映射还不够：动作必须真的有中文名，
		// 否则审计列表上会显示原始标识。
		if !audit.Known(resolved.Action) {
			missing = append(missing, method+" "+routePattern+"（动作 "+resolved.Action+" 缺少中文名）")
		}
	}

	if len(missing) > 0 {
		t.Errorf("以下写接口缺少审计动作映射，请在 audit_routes.go 中补登记：")
		for _, m := range missing {
			t.Errorf("  - %s", m)
		}
	}
}

// TestAuditRoutesHaveNoDanglingEntries 反向检查映射表里没有多余条目。
//
// 如果某个路径在两处都不存在了（路由被删），映射表里留着它
// 会误导后来人以为那个接口还在，而且改名时容易连带改错。
// 这里只告警不失败过于宽松，直接失败更好——死配置就该清掉。
func TestAuditRoutesHaveNoDanglingEntries(t *testing.T) {
	e := newTestEnv(t)
	s := serverOf(t, e.router)

	// 收集真实存在的路径模式。
	real := map[string]bool{}
	for _, ri := range gfRoutes(s) {
		real[ri.Pattern] = true
	}

	for p := range auditRoutes {
		if !real[p] {
			t.Errorf("映射表里的 %q 在真实路由中不存在，应删除", p)
		}
	}
	for p := range auditDeleteOverrides {
		if !real[p] {
			t.Errorf("DELETE 覆盖表里的 %q 在真实路由中不存在，应删除", p)
		}
	}
	for p := range auditPutOverrides {
		if !real[p] {
			t.Errorf("PUT 覆盖表里的 %q 在真实路由中不存在，应删除", p)
		}
	}
}

// TestAuditTargetParamExists 确保 TargetParam 与路由里的参数名一致。
//
// 参数名写错的后果非常隐蔽：pathParam 取不到值只返回空串，
// 不报错，审计照记，只是 target 列永远为空——没有任何信号。
// 这类问题只能靠机械校验发现。
func TestAuditTargetParamExists(t *testing.T) {
	e := newTestEnv(t)
	s := serverOf(t, e.router)

	// 路径模式 → 该模式里出现过的参数名。
	paramsOf := map[string]map[string]bool{}
	for _, ri := range gfRoutes(s) {
		paramsOf[ri.Pattern] = extractRouteParams(ri.Pattern)
	}

	check := func(tableName string, table map[string]auditRoute) {
		for p, r := range table {
			if r.TargetParam == "" {
				continue
			}
			params, ok := paramsOf[p]
			if !ok {
				continue // 路径不存在的问题由上一条测试报告
			}
			if !params[r.TargetParam] {
				t.Errorf("%s: 路径 %q 的 TargetParam=%q 不是该路由的参数（实际参数: %v）",
					tableName, p, r.TargetParam, keysOf(params))
			}
		}
	}
	check("auditRoutes", auditRoutes)
	check("auditDeleteOverrides", auditDeleteOverrides)
	check("auditPutOverrides", auditPutOverrides)
}

// extractRouteParams 从路径模式里抽出 {name} 形式的参数名。
func extractRouteParams(pattern string) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < len(pattern); i++ {
		if pattern[i] != '{' {
			continue
		}
		j := i + 1
		for j < len(pattern) && pattern[j] != '}' {
			j++
		}
		if j < len(pattern) {
			out[pattern[i+1:j]] = true
		}
		i = j
	}
	return out
}

// keysOf 返回 map 的键，便于在错误信息里展示。
func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
