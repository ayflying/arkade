package httpx

// audit_routes_test.go 是映射表的守护测试。
//
// 存在意义：中间件保证了"挂了路由的接口都会被记录"，
// 但如果某条新路由没在 audit_routes.go 里登记，它会被记成
// unknown.POST —— 审计表里有一条记录，但动作看不出来是什么。
// 这比"完全没记录"更难发现：数量对，内容没意义。
//
// 所以这里用 chi 的 WalkRoutes 遍历真实路由表，逐条检查
// 每个有副作用的接口都能解析出**已登记**的动作。
// 新增接口忘记补映射时，这条测试会失败并指名是哪条路径。

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"arkade/hub/internal/audit"
)

// TestEveryMutatingRouteHasAuditAction 遍历真实路由，确保每条写接口都有映射。
//
// 这是本任务最重要的一条结构性测试：它把"审计必须覆盖所有写接口"
// 从一句约定变成了机器可验证的约束。
func TestEveryMutatingRouteHasAuditAction(t *testing.T) {
	e := newTestEnv(t)

	// NewRouter 返回的是 http.Handler，需要断言成 chi.Routes 才能遍历。
	routes, ok := e.router.(chi.Routes)
	if !ok {
		t.Fatal("根路由应实现 chi.Routes 接口")
	}

	var missing []string
	err := chi.Walk(routes, func(method, routePattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !isMutatingMethod(method) {
			return nil
		}
		// 登录接口在未认证分组外，auditMiddleware 覆盖不到，
		// 且它是"获取凭证"而非业务操作，不纳入审计范围。
		if routePattern == "/api/auth/login" {
			return nil
		}
		resolved := resolveAuditRoute(routePattern, method, routePattern)
		if resolved.Action == audit.Unknown(method) {
			missing = append(missing, method+" "+routePattern)
			return nil
		}
		// 光有映射还不够：动作必须真的有中文名，
		// 否则审计列表上会显示原始标识。
		if !audit.Known(resolved.Action) {
			missing = append(missing, method+" "+routePattern+"（动作 "+resolved.Action+" 缺少中文名）")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历路由失败: %v", err)
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
	routes, ok := e.router.(chi.Routes)
	if !ok {
		t.Fatal("根路由应实现 chi.Routes 接口")
	}

	// 收集真实存在的路径模式。
	real := map[string]bool{}
	err := chi.Walk(routes, func(_, routePattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		real[routePattern] = true
		return nil
	})
	if err != nil {
		t.Fatalf("遍历路由失败: %v", err)
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
// 参数名写错的后果非常隐蔽：chi.URLParam 取不到值只返回空串，
// 不报错，审计照记，只是 target 列永远为空——没有任何信号。
// 这类问题只能靠机械校验发现。
func TestAuditTargetParamExists(t *testing.T) {
	e := newTestEnv(t)
	routes, ok := e.router.(chi.Routes)
	if !ok {
		t.Fatal("根路由应实现 chi.Routes 接口")
	}

	// 路径模式 → 该模式里出现过的参数名。
	paramsOf := map[string]map[string]bool{}
	err := chi.Walk(routes, func(_, routePattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		paramsOf[routePattern] = extractRouteParams(routePattern)
		return nil
	})
	if err != nil {
		t.Fatalf("遍历路由失败: %v", err)
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
